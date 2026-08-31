// FTP 目录列取集成测试：真实 testserver FTP 服务端（含 MLSD/LIST 两种形态）。
package network

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mao-jh/porter/testserver"
)

func TestFTPListDir(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("a.bin", "aaa")
	mustWrite("b.bin", "bbbbbb")
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(filepath.Join("subdir", "c.bin"), "cccccccc")

	ftp, err := testserver.NewFTPServer(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ftp.Close()
	tr := NewFTPTransport(false)

	// MLSD 优先
	entries, err := tr.ListDir(context.Background(), ftp.URL()+"/")
	if err != nil {
		t.Fatalf("ListDir 失败: %v", err)
	}
	names := map[string]FTPEntry{}
	for _, e := range entries {
		names[e.Name] = e
	}
	if e, ok := names["a.bin"]; !ok || e.Size != 3 {
		t.Errorf("缺 a.bin 或大小错: %+v", names)
	}
	if e, ok := names["b.bin"]; !ok || e.Size != 6 {
		t.Errorf("缺 b.bin 或大小错: %+v", names)
	}
	if e, ok := names["subdir"]; !ok || !e.IsDir {
		t.Errorf("缺 subdir 目录: %+v", names)
	}

	// 解析器单测：Unix LIST 行
	uni, ok := parseLISTLine("-rw-r--r-- 1 porter porter       1024 Jan 01 12:00 movie.mp4")
	if !ok || uni.Name != "movie.mp4" || uni.Size != 1024 || uni.IsDir {
		t.Errorf("Unix LIST 解析错误: %+v", uni)
	}
	dirLine, ok := parseLISTLine("drwxr-xr-x 2 porter porter       4096 Jan 01 12:00 videos")
	if !ok || !dirLine.IsDir || dirLine.Name != "videos" {
		t.Errorf("Unix 目录行解析错误: %+v", dirLine)
	}
	// Windows LIST 行
	win, ok := parseLISTLine("01-01-26 12:00PM       <DIR>          docs")
	if !ok || !win.IsDir || win.Name != "docs" {
		t.Errorf("Windows LIST 解析错误: %+v", win)
	}
	winFile, ok := parseLISTLine("01-01-26 12:00PM             2048 song.mp3")
	if !ok || winFile.Name != "song.mp3" || winFile.Size != 2048 {
		t.Errorf("Windows 文件行解析错误: %+v", winFile)
	}

	// MLSD 行解析
	m, err := parseMLSD("type=file;size=777;modify=20260101120000; data.zip")
	if err != nil || len(m) != 1 || m[0].Name != "data.zip" || m[0].Size != 777 {
		t.Errorf("MLSD 解析错误: %+v err=%v", m, err)
	}
}
