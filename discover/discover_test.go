// discover 包单元测试：链接提取、书签解析、文本提取、bencode/torrent。
package discover

import (
	"context"
	"strings"
	"testing"
)

// fakeFetcher 假页面抓取器。
type fakeFetcher struct {
	body string
	err  error
}

func (f *fakeFetcher) Get(_ context.Context, _ string, _ int64) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.body), nil
}

func TestParsePageLinks(t *testing.T) {
	page := `<html><head><base href="http://mirror.example.com/root/"></head><body>
<a href="/file/a.bin">a</a>
<a href="b.mp4">rel</a>
<a href="https://cdn.example.com/x.zip">abs</a>
<img src="pic.jpg">
<a href="#top">anchor</a>
<a href="javascript:void(0)">js</a>
<script src="/app.js"></script>
<link href="/style.css">
<a href="ftp://ftp.example.com/pub/c.iso">ftp</a>
</body></html>`
	hits := ParsePageLinks([]byte(page), "http://page.example.com/index.html", nil)
	got := strings.Join(hits.Links, "\n")
	for _, want := range []string{
		"http://mirror.example.com/file/a.bin",
		"http://mirror.example.com/root/b.mp4",
		"https://cdn.example.com/x.zip",
		"http://mirror.example.com/root/pic.jpg",
		"ftp://ftp.example.com/pub/c.iso",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("缺链接 %s; got:\n%s", want, got)
		}
	}
	for _, bad := range []string{"#top", "javascript", "app.js", "style.css"} {
		if strings.Contains(got, bad) {
			t.Errorf("不应包含 %s; got:\n%s", bad, got)
		}
	}
	if hits.Base != "http://mirror.example.com/root/" {
		t.Errorf("base 应为 <base href>，got %s", hits.Base)
	}
}

func TestParsePageLinksExtFilter(t *testing.T) {
	page := `<a href="/a.mp4">a</a><a href="/b.mkv">b</a><a href="/c.txt">c</a>`
	hits := ParsePageLinks([]byte(page), "http://x/", ExtFilter{".mp4", ".mkv"})
	if len(hits.Links) != 2 {
		t.Fatalf("过滤后应 2 条，got %d: %v", len(hits.Links), hits.Links)
	}
}

func TestFindLinksInPageError(t *testing.T) {
	f := &fakeFetcher{err: context.Canceled}
	if _, err := FindLinksInPage(context.Background(), f, "http://x/", 0, nil); err == nil {
		t.Fatal("抓取失败应返回错误")
	}
}

func TestParseBookmarks(t *testing.T) {
	data := `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<DL><p>
  <DT><H3>Movies</H3>
  <DL><p>
    <DT><A HREF="http://example.com/a.mp4" ADD_DATE="1700000000">Movie A</A>
    <DT><A HREF="https://example.com/b.mkv">Movie B</A>
    <DT><A HREF="http://example.com/a.mp4">duplicate</A>
    <DT><A HREF="javascript:void(0)">skip</A>
  </DL><p>
</DL><p>`
	bms := ParseBookmarks([]byte(data))
	if len(bms) != 2 {
		t.Fatalf("应 2 条去重书签，got %d: %+v", len(bms), bms)
	}
	if bms[0].URL != "http://example.com/a.mp4" || bms[0].Title != "Movie A" {
		t.Errorf("书签 0 解析错误: %+v", bms[0])
	}
}

func TestExtractURLs(t *testing.T) {
	text := `看这里 http://a.com/x.mp4 和 https://b.org/y.zip?q=1。
还有 ftp://c.net/f.iso，尾标点 https://d.com/z.mkv. 应剥离。`
	urls := ExtractURLs(text)
	joined := strings.Join(urls, "|")
	for _, want := range []string{
		"http://a.com/x.mp4", "https://b.org/y.zip?q=1",
		"ftp://c.net/f.iso", "https://d.com/z.mkv",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺 %s; got %v", want, urls)
		}
	}
	if len(urls) != 4 {
		t.Errorf("应 4 条，got %d: %v", len(urls), urls)
	}
}

func TestParseTorrentSingleFile(t *testing.T) {
	// 手工构造单文件 torrent（键序：announce < info < url-list）
	// announce: "http://tracker.example.com/ann" = 30 字节
	torrent := "d8:announce30:http://tracker.example.com/ann4:infod6:lengthi1024e4:name9:movie.bin12:piece lengthi16384e6:pieces20:01234567890123456789e8:url-listl33:http://seed.example.com/movie.binee"
	tor, err := ParseTorrent([]byte(torrent))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if tor.Name != "movie.bin" || tor.Length != 1024 {
		t.Errorf("元数据错误: %+v", tor)
	}
	if len(tor.InfoHash) != 40 {
		t.Errorf("info_hash 应为 40 位 hex，got %q", tor.InfoHash)
	}
	if len(tor.Announce) != 1 || tor.Announce[0] != "http://tracker.example.com/ann" {
		t.Errorf("announce 错误: %v", tor.Announce)
	}
	if len(tor.WebSeeds) != 1 || tor.WebSeeds[0] != "http://seed.example.com/movie.bin" {
		t.Errorf("webseed 错误: %v", tor.WebSeeds)
	}
}

func TestParseTorrentMultiFile(t *testing.T) {
	// 多文件：files 列表（f1=2 字节，f2=2 字节）
	torrent := "d4:infod5:filesld6:lengthi10e4:pathl4:dir12:f1eed6:lengthi20e4:pathl2:f2eeee4:name7:archivee"
	tor, err := ParseTorrent([]byte(torrent))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if tor.Length != -1 || len(tor.Files) != 2 {
		t.Fatalf("多文件解析错误: %+v", tor)
	}
	if tor.Files[0].Path != "dir1/f1" || tor.Files[1].Path != "f2" {
		t.Errorf("文件路径错误: %+v", tor.Files)
	}
}

func TestParseTorrentBadData(t *testing.T) {
	if _, err := ParseTorrent([]byte("not a torrent")); err == nil {
		t.Fatal("垃圾数据应报错")
	}
}

func TestParseMagnet(t *testing.T) {
	m, err := ParseMagnet("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Test&tr=http://t1.com/ann")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if m.InfoHash != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("hash 错误: %s", m.InfoHash)
	}
	if m.Name != "Test" || len(m.Trackers) != 1 {
		t.Errorf("元数据错误: %+v", m)
	}
	if _, err := ParseMagnet("http://x.com"); err == nil {
		t.Fatal("非磁力应报错")
	}
	if _, err := ParseMagnet("magnet:?xt=urn:btih:short"); err == nil {
		t.Fatal("错误长度 hash 应报错")
	}
}

func TestBencode(t *testing.T) {
	n, err := bdecode([]byte("d3:fooi42e4:listli1ei2eee"))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if n.kind != 'd' || n.dictGet("foo").asInt() != 42 {
		t.Errorf("字典解析错误: %+v", n)
	}
	lst := n.dictGet("list")
	if lst == nil || lst.kind != 'l' || len(lst.list) != 2 {
		t.Fatalf("列表解析错误")
	}
}
