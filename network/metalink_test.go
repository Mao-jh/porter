package network

import (
	"context"
	"strings"
	"testing"
)

func TestParseMetalink_PriorityAndHash(t *testing.T) {
	t.Parallel()
	body := `<?xml version="1.0" encoding="UTF-8"?>
<metalink xmlns="urn:ietf:params:xml:ns:metalink">
  <files>
    <file name="app.iso">
      <size>1048576</size>
      <hash type="md5">0123456789abcdef0123456789abcdef</hash>
      <hash type="sha-1">0123456789abcdef0123456789abcdef01234567</hash>
      <hash type="sha-256">aabbccdd00112233aabbccdd00112233aabbccdd00112233aabbccdd00112233</hash>
      <url priority="3">http://m3/iso</url>
      <url priority="1">http://m1/iso</url>
      <url>http://no-priority/iso</url>
      <url priority="2">http://m2/iso</url>
    </file>
  </files>
</metalink>`
	ml, err := ParseMetalink([]byte(body))
	if err != nil {
		t.Fatalf("ParseMetalink: %v", err)
	}
	if ml.Name != "app.iso" || ml.Size != 1048576 {
		t.Errorf("name/size 解析错误: %+v", ml)
	}
	if ml.HashAlgo != "sha256" { // sha-256 优先
		t.Errorf("哈希选择错误: %s", ml.HashAlgo)
	}
	if !strings.HasPrefix(ml.HashSum, "aabbccdd") {
		t.Errorf("哈希值错误: %s", ml.HashSum)
	}
	// priority 升序，缺省 priority 排最后
	wantOrder := []string{"http://m1/iso", "http://m2/iso", "http://m3/iso", "http://no-priority/iso"}
	if len(ml.URLs) != len(wantOrder) {
		t.Fatalf("候选数 %d != %d", len(ml.URLs), len(wantOrder))
	}
	for i, u := range ml.URLs {
		if u.URL != wantOrder[i] {
			t.Errorf("排序[%d]=%s, want %s", i, u.URL, wantOrder[i])
		}
	}
}

func TestParseMetalink_Rejects(t *testing.T) {
	t.Parallel()
	if _, err := ParseMetalink([]byte("not xml")); err == nil {
		t.Error("非法 XML 应拒绝")
	}
	noFile := `<metalink xmlns="urn:ietf:params:xml:ns:metalink"><files></files></metalink>`
	if _, err := ParseMetalink([]byte(noFile)); err == nil {
		t.Error("无 file 条目应拒绝")
	}
	noURL := `<metalink xmlns="urn:ietf:params:xml:ns:metalink"><files><file name="x"><size>1</size></file></files></metalink>`
	if _, err := ParseMetalink([]byte(noURL)); err == nil {
		t.Error("无候选 URL 应拒绝")
	}
	// 非法哈希长度被忽略 → HashAlgo 为空
	badHash := `<metalink xmlns="urn:ietf:params:xml:ns:metalink"><files><file name="x">` +
		`<hash type="sha-256">zz</hash><url priority="1">http://a/x</url></file></files></metalink>`
	ml, err := ParseMetalink([]byte(badHash))
	if err != nil || ml.HashAlgo != "" {
		t.Errorf("非法哈希应被忽略: %v %+v", err, ml)
	}
}

func TestIsMetalinkURL(t *testing.T) {
	t.Parallel()
	for _, u := range []string{"http://a/x.meta4", "https://a/b/c.METALINK", "http://a/x.metalink?x=1"} {
		if !IsMetalinkURL(u) {
			t.Errorf("%s 应识别为 metalink", u)
		}
	}
	for _, u := range []string{"http://a/x.bin", "ftp://a/x.meta4", "http://a/meta4/x"} {
		if IsMetalinkURL(u) {
			t.Errorf("%s 不应识别为 metalink", u)
		}
	}
}

// TestFetchMetalink_Live 端到端：经 testserver 拉取并解析（相对候选 URL 基于元文件解析）。
func TestFetchMetalink_Live(t *testing.T) {
	srv, cleanup := startTestServer(t)
	defer cleanup()
	srv.CreateFile(t, "m.bin", 1<<20)
	tr := NewTransport(false)
	ml, err := FetchMetalink(context.Background(), tr, srv.s.BaseURL()+"/meta4/m.bin.meta4")
	if err != nil {
		t.Fatalf("FetchMetalink: %v", err)
	}
	if len(ml.URLs) != 2 {
		t.Fatalf("候选数 %d != 2", len(ml.URLs))
	}
	if !strings.HasPrefix(ml.URLs[0].URL, srv.s.BaseURL()+"/missing/") {
		t.Errorf("候选 1 应解析为服务端绝对地址: %s", ml.URLs[0].URL)
	}
	if !strings.HasPrefix(ml.URLs[1].URL, srv.s.BaseURL()+"/file/") {
		t.Errorf("候选 2 应解析为服务端绝对地址: %s", ml.URLs[1].URL)
	}
	if ml.HashAlgo != "sha256" || ml.Size != 1<<20 || ml.Name != "porter-meta4-out.bin" {
		t.Errorf("元数据错误: algo=%s size=%d name=%s", ml.HashAlgo, ml.Size, ml.Name)
	}
}
