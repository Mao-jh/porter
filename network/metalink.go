// metalink.go 实现 Metalink4（RFC 5854）解析（第 13 轮）。
// .meta4/.metalink 文件列出候选下载 URL（priority 升序 failover）、大小与哈希；
// 哈希交给 cli 做「期望值校验」——下载后与实际值比对，不一致判失败并删除产物。
// 合规边界：元文件 ≤1MiB、候选 ≤32、仅取首个 <file>（单任务模型）；仅 sha-256/sha-1/md5。
// 解析用 token 遍历（元素名取 Local）：对有无 xmlns 的文档同等兼容。
package network

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// 资源上限。
const (
	maxMetalinkBytes = 1 << 20 // 元文件上限 1 MiB
	maxMetalinkURLs  = 32      // 候选 URL 上限
)

// IsMetalinkURL 判断 URL 是否按 Metalink 处理：http(s) 且路径以 .meta4/.metalink 结尾。
func IsMetalinkURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	p := strings.ToLower(u.Path)
	return strings.HasSuffix(p, ".meta4") || strings.HasSuffix(p, ".metalink")
}

// MetalinkURL 一个候选下载地址（已按 priority 升序排序）。
type MetalinkURL struct {
	URL      string
	Priority int
}

// Metalink 解析结果（首个 <file>）。
type Metalink struct {
	Name     string        // 文件名属性（输出命名建议）
	Size     int64         // <size>（0=未给出）
	HashAlgo string        // "sha256"/"sha1"/"md5"（空=未给出可用哈希）
	HashSum  string        // 十六进制小写
	URLs     []MetalinkURL // priority 升序
}

// FetchMetalink 下载并解析 Metalink 文件（候选 URL 相对路径基于元文件 URL 解析）。
func FetchMetalink(ctx context.Context, t *Transport, raw string) (*Metalink, error) {
	body, err := t.getBounded(ctx, raw, maxMetalinkBytes)
	if err != nil {
		return nil, fmt.Errorf("metalink: 拉取元文件失败: %w", err)
	}
	ml, err := ParseMetalink(body)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	for i := range ml.URLs {
		ref, err := url.Parse(ml.URLs[i].URL)
		if err != nil {
			return nil, fmt.Errorf("metalink: 候选 URL 非法 %q: %w", ml.URLs[i].URL, err)
		}
		ml.URLs[i].URL = base.ResolveReference(ref).String()
	}
	return ml, nil
}

// ParseMetalink 解析 Metalink4 正文（命名空间无关，仅取首个 <file>）。
func ParseMetalink(body []byte) (*Metalink, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	ml := &Metalink{}
	var (
		inFile   bool
		depth    int
		inURL    bool
		urlPrio  int
		inHash   bool
		hashType string
		inSize   bool
		text     strings.Builder
		hashRank int
	)
	// RFC 5854 type 属性为连字符形式；rank: sha-256 > sha-1 > md5
	rankOf := map[string]int{"sha-256": 3, "sha-1": 2, "md5": 1}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("metalink: XML 解析失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !inFile {
				if t.Name.Local == "file" {
					inFile, depth = true, 0
					ml = &Metalink{}
					hashRank = 0
					for _, a := range t.Attr {
						if a.Name.Local == "name" {
							ml.Name = a.Value
						}
					}
				}
				continue
			}
			depth++
			text.Reset()
			switch t.Name.Local {
			case "url":
				inURL, urlPrio = true, 0
				for _, a := range t.Attr {
					if a.Name.Local == "priority" {
						if n, e := strconv.Atoi(a.Value); e == nil {
							urlPrio = n
						}
					}
				}
			case "hash":
				inHash, hashType = true, ""
				for _, a := range t.Attr {
					if a.Name.Local == "type" {
						hashType = a.Value
					}
				}
			case "size":
				inSize = true
			}
		case xml.CharData:
			if inFile {
				text.Write(t)
			}
		case xml.EndElement:
			if !inFile {
				continue
			}
			if t.Name.Local == "file" && depth == 0 {
				inFile = false
				continue
			}
			depth--
			switch t.Name.Local {
			case "url":
				if inURL {
					inURL = false
					v := strings.TrimSpace(text.String())
					if v != "" {
						p := urlPrio
						if p <= 0 {
							p = 999999 // RFC 5854：priority 缺省最低；1 最高
						}
						ml.URLs = append(ml.URLs, MetalinkURL{URL: v, Priority: p})
					}
				}
			case "hash":
				if inHash {
					inHash = false
					if r, ok := rankOf[strings.ToLower(strings.TrimSpace(hashType))]; ok && r > hashRank {
						v := strings.ToLower(strings.TrimSpace(text.String()))
						if validHexLen(v) {
							hashRank, ml.HashSum = r, v
						}
					}
				}
			case "size":
				if inSize {
					inSize = false
					if n, e := strconv.ParseInt(strings.TrimSpace(text.String()), 10, 64); e == nil && n >= 0 {
						ml.Size = n
					}
				}
			}
			text.Reset()
		}
	}

	if len(ml.URLs) == 0 {
		return nil, fmt.Errorf("metalink: 无 <file> 条目或候选 URL")
	}
	if len(ml.URLs) > maxMetalinkURLs {
		return nil, fmt.Errorf("metalink: 候选 URL 数 %d 超过上限 %d", len(ml.URLs), maxMetalinkURLs)
	}
	sort.SliceStable(ml.URLs, func(i, j int) bool { return ml.URLs[i].Priority < ml.URLs[j].Priority })
	switch hashRank {
	case 3:
		ml.HashAlgo = "sha256"
	case 2:
		ml.HashAlgo = "sha1"
	case 1:
		ml.HashAlgo = "md5"
	}
	return ml, nil
}

func validHexLen(v string) bool {
	switch len(v) {
	case 64, 40, 32:
		_, err := hex.DecodeString(v)
		return err == nil
	}
	return false
}
