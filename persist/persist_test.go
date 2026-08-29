package persist

import (
	"path/filepath"
	"testing"
)

// TestStore_PutGet 基本写入/读取往返。
func TestStore_PutGet(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st := &State{ID: "t1", URL: "http://127.0.0.1/x", FileSize: 100, Done: 42, Status: "running"}
	if err := s.Put(st); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := s.Get("t1")
	if !ok {
		t.Fatal("应存在")
	}
	if got.Done != 42 || got.FileSize != 100 {
		t.Fatalf("状态不一致: %+v", got)
	}
}

// TestStore_ResumeSimulation 模拟「进程崩溃后重启恢复」：新建 Store 读取同一目录。
func TestStore_ResumeSimulation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")

	s1, _ := Open(path)
	_ = s1.Put(&State{ID: "dl", URL: "http://127.0.0.1/a", FileSize: 1 << 20, Done: 700000, Status: "running"})
	_ = s1.Put(&State{ID: "dl2", URL: "http://127.0.0.1/b", FileSize: 500, Done: 500, Status: "done"})
	// s1 超出作用域（模拟进程退出），重新打开 = 重启
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重启 Open: %v", err)
	}
	dl, ok := s2.Get("dl")
	if !ok || dl.Done != 700000 {
		t.Fatalf("恢复失败: %+v", dl)
	}
	all := s2.All()
	if len(all) != 2 {
		t.Fatalf("应恢复 2 条, got %d", len(all))
	}
}

// TestStore_Remove 删除后不可见。
func TestStore_Remove(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "state"))
	_ = s.Put(&State{ID: "x"})
	if err := s.Remove("x"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := s.Get("x"); ok {
		t.Fatal("应已删除")
	}
}
