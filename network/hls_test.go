package network

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
)

// encPlaylistFor 生成加密媒体播放列表正文（供解析测试）。
func encPlaylistFor(name, keyURI string, n int) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:7\n")
	fmt.Fprintf(&b, "#EXT-X-KEY:METHOD=AES-128,URI=%q,IV=0x%X\n", keyURI, bytes.Repeat([]byte{0xAB}, 16))
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "#EXTINF:10.0,\n/seg/%s-%d.ts\n", name, i)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

func TestParsePlaylist_MediaAndKeys(t *testing.T) {
	t.Parallel()
	u, _ := url.Parse("http://127.0.0.1:1/vod/index.m3u8")
	body := encPlaylistFor("s", "key.bin", 3)
	p, err := parsePlaylist(u, []byte(body))
	if err != nil {
		t.Fatalf("parsePlaylist: %v", err)
	}
	if p.master || !p.endlist || !p.encrypted || len(p.segments) != 3 {
		t.Fatalf("parsed: master=%v endlist=%v enc=%v n=%d", p.master, p.endlist, p.encrypted, len(p.segments))
	}
	seg := p.segments[0]
	if seg.seq != 7 { // MEDIA-SEQUENCE 基准
		t.Errorf("seq=%d, want 7", seg.seq)
	}
	if seg.url != "http://127.0.0.1:1/seg/s-0.ts" {
		t.Errorf("相对段解析错误: %s", seg.url)
	}
	if seg.key == nil || !seg.key.ivSet || seg.key.uri != "http://127.0.0.1:1/vod/key.bin" {
		t.Fatalf("key 解析错误: %+v", seg.key)
	}
	if seg.key.iv[0] != 0xAB || seg.key.iv[15] != 0xAB {
		t.Errorf("IV 解析错误: %x", seg.key.iv)
	}
	// 缺省 IV = 媒体序列号 128 位大端（RFC 8216 §5.2）
	iv := hlsIV(&hlsKey{uri: "k"}, 7)
	if binary.BigEndian.Uint64(iv[8:]) != 7 || binary.BigEndian.Uint64(iv[:8]) != 0 {
		t.Errorf("缺省 IV 错误: %x", iv)
	}
}

func TestParsePlaylist_MasterAndByterange(t *testing.T) {
	t.Parallel()
	u, _ := url.Parse("http://127.0.0.1:1/master.m3u8")
	master := "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=400000\n/low.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=4000000\n/high.m3u8\n"
	p, err := parsePlaylist(u, []byte(master))
	if err != nil || !p.master || len(p.variants) != 2 {
		t.Fatalf("master 解析失败: %v %+v", err, p)
	}
	best := p.variants[0]
	for _, v := range p.variants[1:] {
		if v.bandwidth > best.bandwidth {
			best = v
		}
	}
	if best.url != "http://127.0.0.1:1/high.m3u8" {
		t.Errorf("选流错误: %s", best.url)
	}

	// BYTERANGE：显式偏移与缺省偏移（同资源累进）
	u2, _ := url.Parse("http://127.0.0.1:1/media.m3u8")
	br := "#EXTM3U\n#EXT-X-ENDLIST\n" +
		"#EXTINF:1.0,\n#EXT-X-BYTERANGE:1000@500\n/data.bin\n" +
		"#EXTINF:1.0,\n#EXT-X-BYTERANGE:2000\n/data.bin\n"
	p2, err := parsePlaylist(u2, []byte(br))
	if err != nil {
		t.Fatalf("byterange 解析: %v", err)
	}
	if p2.segments[0].byteStart != 500 || p2.segments[0].byteLen != 1000 {
		t.Errorf("显式偏移错误: %+v", p2.segments[0])
	}
	if p2.segments[1].byteStart != 1500 || p2.segments[1].byteLen != 2000 {
		t.Errorf("缺省偏移应=前段终点 1500: %+v", p2.segments[1])
	}
	// 跨资源的缺省偏移非法 → 解析报错
	brBad := "#EXTM3U\n#EXT-X-ENDLIST\n" +
		"#EXTINF:1.0,\n#EXT-X-BYTERANGE:1000@0\n/data.bin\n" +
		"#EXTINF:1.0,\n#EXT-X-BYTERANGE:1000\n/other.bin\n"
	if _, err := parsePlaylist(u2, []byte(brBad)); err == nil {
		t.Error("跨资源缺省偏移应报错")
	}
}

func TestParsePlaylist_Rejects(t *testing.T) {
	t.Parallel()
	u, _ := url.Parse("http://127.0.0.1:1/x.m3u8")
	// 非 m3u8
	if _, err := parsePlaylist(u, []byte("<html></html>")); err == nil {
		t.Error("缺 #EXTM3U 应拒绝")
	}
	// 直播流（无 ENDLIST）
	live := "#EXTM3U\n#EXTINF:10.0,\n/seg-0.ts\n"
	if _, err := parsePlaylist(u, []byte(live)); err != nil {
		t.Fatalf("live 解析本身应成功（拒绝在 buildPlan）: %v", err)
	}
	// SAMPLE-AES：显式拒绝（不做 DRM 绕过）
	drm := "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"k\"\n#EXT-X-ENDLIST\n"
	if _, err := parsePlaylist(u, []byte(drm)); err == nil {
		t.Error("SAMPLE-AES 应拒绝")
	}
}

func TestCBCReader_RoundTripAndPKCS7(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{7}, 16)
	var iv [16]byte
	binary.BigEndian.PutUint64(iv[8:], 3)

	for _, plainLen := range []int{1, 15, 16, 17, 1000, 65535, 65536, 65537} {
		plain := make([]byte, plainLen)
		for i := range plain {
			plain[i] = byte(i*31 + 5)
		}
		pad := 16 - plainLen%16
		padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
		block, _ := aes.NewCipher(key)
		ct := make([]byte, len(padded))
		cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(ct, padded)

		// 以 7 字节小块喂入，覆盖块对齐滞留路径
		var out bytes.Buffer
		cr, err := newCBCReader(io.NopCloser(&slowReader{r: bytes.NewReader(ct), n: 7}), [16]byte(key), iv)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(&out, cr); err != nil {
			t.Fatalf("plainLen=%d: %v", plainLen, err)
		}
		if !bytes.Equal(out.Bytes(), plain) {
			t.Fatalf("plainLen=%d: 解密不一致 (got %d bytes)", plainLen, out.Len())
		}
	}
	// 填充损坏应报错（明文尾字节 0xFF > 16 → PKCS7 非法）
	ct := make([]byte, 32)
	block, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(ct, bytes.Repeat([]byte{0xFF}, 32))
	cr, _ := newCBCReader(io.NopCloser(bytes.NewReader(ct)), [16]byte(key), iv)
	if _, err := io.ReadAll(cr); err == nil {
		t.Error("PKCS7 填充非法应报错")
	}
}

// slowReader 每次 Read 最多返回 n 字节（打乱块对齐边界）。
type slowReader struct {
	r io.Reader
	n int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if len(p) > s.n {
		p = p[:s.n]
	}
	return s.r.Read(p)
}

// TestHLS_E2E_ParallelAndEncrypted 端到端：明文（并行分片）与 AES-128（顺序解密）
// 两条路径经虚拟映射下载，内容与源文件 sha256 一致。
func TestHLS_E2E_ParallelAndEncrypted(t *testing.T) {
	srv, cleanup := startTestServer(t)
	defer cleanup()

	const size = 3 << 20 // 3 MiB → 3 段（虚拟映射 ≥5MiB 才分片，这里验证段序与拼接）
	name := srv.CreateFile(t, "video.bin", size)

	tr := NewTransport(false)
	ctx := context.Background()

	// 明文：虚拟 Probe → 各区间独立 sink（Fetcher 契约：dst 从逻辑 0 写）→ 按序拼接
	u := srv.s.BaseURL() + "/hls/video.bin.m3u8"
	h2 := NewHLSTransport(tr) // 独立实例避免缓存串扰
	vsize, ranged, err := h2.Probe(ctx, u)
	if err != nil || !ranged || vsize != size {
		t.Fatalf("Probe: size=%d ranged=%v err=%v", vsize, ranged, err)
	}
	fetch := func(s, e int64) []byte {
		t.Helper()
		w := &fileSinkWriterAt{}
		if err := h2.FetchRange(ctx, u, s, e, w); err != nil {
			t.Fatalf("FetchRange [%d,%d): %v", s, e, err)
		}
		return w.buf
	}
	mid := fetch(2<<20, 3<<20)
	head := fetch(0, 1<<20)
	tail := fetch(1<<20, 2<<20)
	var got bytes.Buffer
	got.Write(head)
	got.Write(tail)
	got.Write(mid)
	want, _ := srv.s.Checksum(name)
	sum := sha256.Sum256(got.Bytes())
	if hexStr(sum[:]) != want {
		t.Fatalf("明文 HLS 内容不一致: %s != %s", hexStr(sum[:]), want)
	}

	// 加密：Probe 退化为流式 → 顺序全量下载自动解密
	h3 := NewHLSTransport(tr)
	ue := srv.s.BaseURL() + "/hls/video.bin.enc.m3u8"
	esize, eranged, err := h3.Probe(ctx, ue)
	if err != nil || esize != 0 || eranged {
		t.Fatalf("加密 Probe 应返回 (0,false): size=%d ranged=%v err=%v", esize, eranged, err)
	}
	w2 := &fileSinkWriterAt{}
	if err := h3.FetchRange(ctx, ue, 0, 0, w2); err != nil {
		t.Fatalf("加密全量下载: %v", err)
	}
	got2 := sha256.Sum256(w2.buf)
	if hexStr(got2[:]) != want {
		t.Fatalf("AES-128 HLS 解密内容不一致")
	}
}

func hexStr(b []byte) string { return strings.ToLower(fmt.Sprintf("%x", b)) }

// TestHLS_LiveRejected 端到端：直播流（无 ENDLIST）在 Probe 阶段拒绝。
func TestHLS_LiveRejected(t *testing.T) {
	srv, cleanup := startTestServer(t)
	defer cleanup()
	srv.CreateFile(t, "v.bin", 2<<20)
	tr := NewTransport(false)
	h := NewHLSTransport(tr)
	_, _, err := h.Probe(context.Background(), srv.s.BaseURL()+"/hls/v.bin.live.m3u8")
	if err == nil || !strings.Contains(err.Error(), "ENDLIST") {
		t.Fatalf("直播流应被拒绝且提示 ENDLIST: %v", err)
	}
}

// TestHLS_MasterSelectsHighestBandwidth 端到端：主播放列表应选高码率变体。
func TestHLS_MasterSelectsHighestBandwidth(t *testing.T) {
	srv, cleanup := startTestServer(t)
	defer cleanup()
	srv.CreateFile(t, "video.bin", 2<<20)
	srv.CreateFile(t, "tiny.bin", 1<<20) // 低码率变体内容
	tr := NewTransport(false)
	h := NewHLSTransport(tr)
	w := &fileSinkWriterAt{}
	u := srv.s.BaseURL() + "/hls/video.bin.master.m3u8"
	if err := h.FetchRange(context.Background(), u, 0, 0, w); err != nil {
		t.Fatalf("master 下载: %v", err)
	}
	if int64(len(w.buf)) != 2<<20 {
		t.Fatalf("应选中高码率变体（2MiB），实际 %d 字节", len(w.buf))
	}
}
