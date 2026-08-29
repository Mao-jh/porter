// hls.go 为 testserver 提供 HLS / Metalink4 测试端点（第 13 轮）。
// 全部内容基于 cfg.Dir 下的确定性 PatternFill 文件派生，sha256 可闭环比对：
//   /hls/<file>.m3u8          媒体播放列表（1MiB 分段，/hls/<file>/seg-<i>.ts）
//   /hls/<file>.enc.m3u8      AES-128 加密变体（密钥 /key/<file>，IV=媒体序列号缺省）
//   /hls/<file>.live.m3u8     直播形态（无 #EXT-X-ENDLIST，供拒绝测试）
//   /hls/<file>.master.m3u8   主播放列表（低码率指向 tiny.bin，验证选流取最高带宽）
//   /meta4/<file>.meta4       Metalink4（priority=1 指向 404，priority=2 指向真文件）
//   /meta4/<file>.bad.meta4   哈希故意错误（供「期望值校验失败」负例）
package testserver

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const hlsSegSize = int64(1 << 20) // 1 MiB 每段

// registerProtocolRoutes 挂载 HLS/Metalink 端点（New 内调用）。
func (s *Server) registerProtocolRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/hls/", s.handleHLS)
	mux.HandleFunc("/key/", s.handleKey)
	mux.HandleFunc("/meta4/", s.handleMeta4)
}

// hlsKey 由文件名确定性派生 16 字节 AES-128 密钥（服务端加密与客户端解密共用）。
func hlsKeyOf(name string) []byte {
	sum := sha256.Sum256([]byte("porter-hls-key:" + name))
	return sum[:16]
}

// hlsSegmentPath 段在磁盘上对应的源文件区间 [start,end)。
func hlsSegBounds(size, segSize int64, idx int) (start, end int64, total int, ok bool) {
	total = int((size + segSize - 1) / segSize)
	if idx < 0 || idx >= total {
		return 0, 0, 0, false
	}
	start = int64(idx) * segSize
	end = start + segSize
	if end > size {
		end = size
	}
	return start, end, total, true
}

// serveSegment 把文件区间 [start,end) 作为独立资源服务：
// 无 Range（含 HEAD）→ 200 + 区间长 Content-Length；有 Range → 相对区间起点解释 → 206。
// 客户端探测（HEAD 与 Range GET 0-0）因此都能取到**段长**而非全文件长度。
func (s *Server) serveSegment(w http.ResponseWriter, r *http.Request, name string, start, end int64) {
	segLen := end - start
	rs, re := int64(0), segLen
	hadRange := r.Header.Get("Range") != ""
	if rng := r.Header.Get("Range"); rng != "" {
		spec := strings.TrimPrefix(rng, "bytes=")
		if dash := strings.IndexByte(spec, '-'); dash >= 0 {
			if n, err := strconv.ParseInt(spec[:dash], 10, 64); err == nil && n >= 0 {
				rs = n
			}
			if ePart := spec[dash+1:]; ePart == "" {
				re = segLen
			} else if n, err := strconv.ParseInt(ePart, 10, 64); err == nil && n >= rs {
				re = n + 1
			}
		}
		if rs > segLen {
			rs = segLen
		}
		if re > segLen {
			re = segLen
		}
		if re <= rs {
			http.Error(w, "empty range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
	}

	f, err := os.Open(filepath.Join(s.cfg.Dir, name))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer f.Close()
	w.Header().Set("Accept-Ranges", "bytes")
	if hadRange { // 带 Range 一律 206（全段 Range 也算，客户端按 sentRange 严格校验）
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rs, re-1, segLen))
		w.WriteHeader(http.StatusPartialContent)
	}
	w.Header().Set("Content-Length", strconv.FormatInt(re-rs, 10))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := f.Seek(start+rs, io.SeekStart); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var dst io.Writer = w
	if rate := s.currentLimit(); rate > 0 {
		dst = &throttleWriter{w: w, rate: rate, start: time.Now()}
	}
	n, _ := io.CopyBuffer(dst, io.LimitReader(f, re-rs), make([]byte, 64<<10))
	s.served.Add(n)
}

// handleHLS 分发 /hls/ 下的播放列表与段请求。
func (s *Server) handleHLS(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/hls/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		name, seg := rest[:i], rest[i+1:]
		switch {
		case strings.HasPrefix(seg, "seg-"):
			var idx int
			if _, err := fmt.Sscanf(strings.TrimSuffix(seg, ".ts"), "seg-%d", &idx); err != nil {
				http.NotFound(w, r)
				return
			}
			info, err := os.Stat(filepath.Join(s.cfg.Dir, name))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			start, end, _, ok := hlsSegBounds(info.Size(), hlsSegSize, idx)
			if !ok {
				http.NotFound(w, r)
				return
			}
			// 段自包含服务：把 [start,end) 当作完整资源——
			// HEAD/无 Range → 200 + 段长 Content-Length；Range 相对段起点 → 206。
			// （不能复用 handleFile：/file/ 无 Range 时报告的是全文件长度，探测会失真。）
			s.serveSegment(w, r, name, start, end)
		case strings.HasPrefix(seg, "enc-"):
			var idx int
			if _, err := fmt.Sscanf(strings.TrimSuffix(seg, ".ts"), "enc-%d", &idx); err != nil {
				http.NotFound(w, r)
				return
			}
			s.serveEncryptedSegment(w, r, name, idx)
		default:
			http.NotFound(w, r)
		}
		return
	}
	base := strings.TrimSuffix(rest, ".m3u8")
	switch {
	case strings.HasSuffix(base, ".master"):
		s.serveMaster(w, r, strings.TrimSuffix(base, ".master"))
	case strings.HasSuffix(base, ".live"):
		s.serveMediaPlaylist(w, r, strings.TrimSuffix(base, ".live"), false, true)
	case strings.HasSuffix(base, ".enc"):
		s.serveMediaPlaylist(w, r, strings.TrimSuffix(base, ".enc"), true, false)
	default:
		s.serveMediaPlaylist(w, r, base, false, false)
	}
}

// mediaPlaylist 生成媒体播放列表正文。
func mediaPlaylist(name string, total int, enc bool, endlist bool) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:0\n")
	if enc {
		fmt.Fprintf(&b, "#EXT-X-KEY:METHOD=AES-128,URI=\"/key/%s\"\n", name)
	}
	prefix := "seg-"
	if enc {
		prefix = "enc-"
	}
	for i := 0; i < total; i++ {
		fmt.Fprintf(&b, "#EXTINF:10.0,\n/hls/%s/%s%d.ts\n", name, prefix, i)
	}
	if endlist {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.String()
}

// serveMediaPlaylist 输出媒体播放列表（enc=AES-128 变体；live=去掉 ENDLIST）。
func (s *Server) serveMediaPlaylist(w http.ResponseWriter, r *http.Request, name string, enc, live bool) {
	info, err := os.Stat(filepath.Join(s.cfg.Dir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _, total, ok := hlsSegBounds(info.Size(), hlsSegSize, 0)
	if !ok && info.Size() > 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	fmt.Fprint(w, mediaPlaylist(name, total, enc, !live))
}

// serveMaster 输出主播放列表：低码率变体指向 tiny.bin（须已创建），高码率指向 name。
func (s *Server) serveMaster(w http.ResponseWriter, r *http.Request, name string) {
	if _, err := os.Stat(filepath.Join(s.cfg.Dir, name)); err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(filepath.Join(s.cfg.Dir, "tiny.bin")); err != nil {
		http.Error(w, "master 变体需要预先创建 tiny.bin", 500)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	fmt.Fprintf(w, "#EXTM3U\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=400000\n/hls/tiny.bin.m3u8\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=4000000\n/hls/%s.m3u8\n", name)
}

// serveEncryptedSegment 输出 AES-128-CBC 加密段（PKCS7 填充，IV=媒体序列号 128 位大端）。
func (s *Server) serveEncryptedSegment(w http.ResponseWriter, r *http.Request, name string, idx int) {
	info, err := os.Stat(filepath.Join(s.cfg.Dir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	start, end, _, ok := hlsSegBounds(info.Size(), hlsSegSize, idx)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(s.cfg.Dir, name))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer f.Close()
	plain := make([]byte, end-start)
	if _, err := f.ReadAt(plain, start); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	pad := 16 - len(plain)%16
	padded := append(plain, make([]byte, pad)...)
	for i := 0; i < pad; i++ {
		padded[len(plain)+i] = byte(pad)
	}
	block, err := aes.NewCipher(hlsKeyOf(name))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var iv [16]byte
	binary.BigEndian.PutUint64(iv[8:], uint64(idx)) // IV=序列号，与客户端缺省规则一致
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(out, padded)
	w.Header().Set("Content-Length", fmt.Sprint(len(out)))
	_, _ = w.Write(out)
}

// handleKey 输出 16 字节 AES-128 密钥（/key/<name>）。
func (s *Server) handleKey(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/key/")
	if _, err := os.Stat(filepath.Join(s.cfg.Dir, name)); err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(hlsKeyOf(name))
}

// handleMeta4 输出 Metalink4 元文件：<name>.meta4（failover 正例）、
// <name>.bad.meta4（哈希错误负例）。
func (s *Server) handleMeta4(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/meta4/"), ".meta4")
	bad := strings.HasSuffix(base, ".bad")
	if bad {
		base = strings.TrimSuffix(base, ".bad")
	}
	path := filepath.Join(s.cfg.Dir, base)
	info, err := os.Stat(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sum, err := s.Checksum(base)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if bad {
		sum = strings.Repeat("0", 64) // 故意错误
	}
	name := "porter-meta4-out.bin"
	w.Header().Set("Content-Type", "application/metalink4+xml")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<metalink xmlns="urn:ietf:params:xml:ns:metalink">
  <files>
    <file name="%s">
      <size>%d</size>
      <hash type="sha-256">%s</hash>
      <url priority="1">/missing/%s</url>
      <url priority="2">/file/%s</url>
    </file>
  </files>
</metalink>
`, name, info.Size(), sum, base, base)
}
