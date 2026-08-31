// torrent.go BitTorrent 种子元数据解析与磁力链接解析。
// 能力边界（零依赖约束，见 README/BENCHMARK 取舍）：
//   - 解析 .torrent：名称 / 文件清单 / info_hash / tracker / webseed（url-list）
//   - 解析磁力链接：info hash / 显示名 / tracker 参数
//   - 提取 webseed 直链 → 可直接交给 porter 主链路下载（HTTP 种子）
// 不实现完整 BT 协议（对等发现/分块传输）——那是独立传输引擎，超出零依赖范围。
package discover

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// TorrentFile 多文件种子中的单文件条目。
type TorrentFile struct {
	Path   string // 相对路径（join("/") 拼接）
	Length int64
}

// Torrent 解析后的种子元数据。
type Torrent struct {
	Name      string
	Length    int64 // 单文件长度；多文件为 -1（各文件见 Files）
	Files     []TorrentFile
	InfoHash  string // hex（小写）
	Announce  []string
	WebSeeds  []string // url-list（HTTP 直链种子）
	IsPrivate int64
}

// ParseTorrent 解析 .torrent 文件字节。返回错误包含友好提示。
func ParseTorrent(data []byte) (*Torrent, error) {
	root, err := bdecode(data)
	if err != nil {
		return nil, fmt.Errorf("bencode 解析失败（非 .torrent 文件?）: %w", err)
	}
	info := root.dictGet("info")
	if info == nil {
		return nil, fmt.Errorf("torrent 缺少 info 字典")
	}
	t := &Torrent{}
	if b := info.dictGet("name.utf-8"); b != nil {
		t.Name = string(b.asString())
	} else if b := info.dictGet("name"); b != nil {
		t.Name = string(b.asString())
	}
	t.IsPrivate = info.dictGet("private").asInt()

	// 单文件 vs 多文件
	if f := info.dictGet("length"); f != nil {
		t.Length = f.asInt()
	} else if fl := info.dictGet("files"); fl != nil && fl.kind == 'l' {
		t.Length = -1
		for _, f := range fl.list {
			var p string
			if pu := f.dictGet("path.utf-8"); pu != nil && pu.kind == 'l' {
				p = joinPath(pu)
			} else if ps := f.dictGet("path"); ps != nil && ps.kind == 'l' {
				p = joinPath(ps)
			}
			t.Files = append(t.Files, TorrentFile{Path: p, Length: f.dictGet("length").asInt()})
		}
	} else {
		return nil, fmt.Errorf("torrent info 既无 length 也无 files")
	}

	// info_hash = SHA1(info 原始 bencode 字节)
	sum := sha1.Sum(info.raw)
	t.InfoHash = hex.EncodeToString(sum[:])

	// tracker：announce（单）与 announce-list（多）
	if a := root.dictGet("announce"); a != nil {
		if s := a.asString(); len(s) > 0 {
			t.Announce = append(t.Announce, string(s))
		}
	}
	if al := root.dictGet("announce-list"); al != nil && al.kind == 'l' {
		for _, grp := range al.list {
			if grp.kind == 'l' {
				for _, tr := range grp.list {
					if s := tr.asString(); len(s) > 0 {
						t.Announce = append(t.Announce, string(s))
					}
				}
			}
		}
	}
	t.Announce = Dedupe(t.Announce)

	// webseed：url-list（BEP 19）与 nodes 无关
	if ul := root.dictGet("url-list"); ul != nil {
		switch ul.kind {
		case 's':
			if len(ul.s) > 0 {
				t.WebSeeds = append(t.WebSeeds, string(ul.s))
			}
		case 'l':
			for _, e := range ul.list {
				if s := e.asString(); len(s) > 0 {
					t.WebSeeds = append(t.WebSeeds, string(s))
				}
			}
		}
	}
	t.WebSeeds = Dedupe(t.WebSeeds)
	return t, nil
}

// joinPath 拼接多文件 path 列表（转义名中的分隔符不拆）。
func joinPath(p *bnode) string {
	var parts []string
	for _, e := range p.list {
		if s := e.asString(); len(s) > 0 {
			parts = append(parts, string(s))
		}
	}
	return strings.Join(parts, "/")
}

// Magnet 磁力链接解析结果。
type Magnet struct {
	InfoHash  string
	Name      string
	Trackers  []string
	Raw       string
}

// ParseMagnet 解析磁力链接（magnet:?xt=urn:btih:<hash>&dn=...&tr=...）。
// 非 BTIH 的 xt（如 btih 外的 urn:btmh）返回错误。
func ParseMagnet(raw string) (*Magnet, error) {
	if !strings.HasPrefix(raw, "magnet:") {
		return nil, fmt.Errorf("非磁力链接")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	m := &Magnet{Raw: raw}
	q := u.Query()
	xt := q.Get("xt")
	if !strings.HasPrefix(xt, "urn:btih:") {
		return nil, fmt.Errorf("磁力链接缺少 urn:btih 信息哈希（xt=%q）", xt)
	}
	m.InfoHash = strings.ToLower(strings.TrimPrefix(xt, "urn:btih:"))
	// 允许 40 位 hex 或 32 字符 base32
	if len(m.InfoHash) != 40 {
		return nil, fmt.Errorf("info hash 长度 %d != 40（仅支持 hex 形态）", len(m.InfoHash))
	}
	m.Name = q.Get("dn")
	for _, tr := range q["tr"] {
		if tr != "" {
			m.Trackers = append(m.Trackers, tr)
		}
	}
	return m, nil
}

// MagnetToTorrentURL 生成 tracker 查询 URL 的占位提示（不实际发起协议请求）。
// 返回的只有提示文本，供 CLI 输出「下一步」指引。
func (m *Magnet) Hint() string {
	base := path.Base
	_ = base
	if m.Name != "" {
		return fmt.Sprintf("info_hash=%s name=%q：需 BT 客户端（如 aria2/qBittorrent）完成对等下载；"+
			"porter 可接力其下载结果或使用 WebSeed 直链", m.InfoHash, m.Name)
	}
	return fmt.Sprintf("info_hash=%s：需 BT 客户端完成对等下载", m.InfoHash)
}
