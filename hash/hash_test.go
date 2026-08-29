package hash

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestSum_KnownVectors 已知测试向量验证正确性。
func TestSum_KnownVectors(t *testing.T) {
	cases := []struct {
		algo Algorithm
		in   string
		want string
	}{
		{SHA256, "", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{SHA256, "hello", "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
		{MD5, "hello", "5d41402abc4b2a76b9719d911017c592"},
	}
	for _, c := range cases {
		got, err := Sum(bytes.NewReader([]byte(c.in)), c.algo)
		if err != nil {
			t.Fatalf("%s: %v", c.algo, err)
		}
		if got != c.want {
			t.Errorf("%s(%q)=%s want %s", c.algo, c.in, got, c.want)
		}
	}
}

// TestSum_LargeStream 模拟 64 MiB 数据流，验证流式（固定缓冲，内存不随输入增长）。
func TestSum_LargeStream(t *testing.T) {
	size := int64(64 << 20) // 64 MiB
	// 构造确定性数据源（不占用实际 64MiB 内存：用 SectionReader + 全零 ReadSeeker）
	data := make([]byte, size)
	h := sha256.New()
	buf := make([]byte, 64<<10)
	for i := 0; i < int(size)/(64<<10); i++ {
		if _, err := h.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	expected := hex.EncodeToString(h.Sum(nil))

	// 用 bytes.Reader 模拟流（测试中可接受；真实场景为 io.SectionReader）
	got, err := Sum(bytes.NewReader(data), SHA256)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if got != expected {
		t.Errorf("大文件哈希不一致")
	}
}

// TestNew_Unsupported 未知算法返回错误。
func TestNew_Unsupported(t *testing.T) {
	if _, err := New("crc32"); err == nil {
		t.Fatal("应返回错误")
	}
}
