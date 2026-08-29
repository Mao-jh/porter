// Package persist 实现任务状态的磁盘持久化（嵌入式 JSON，异常退出重启可恢复）。
// 策略（§2 决策「持久化」）：状态变更即流式落盘 + 稀疏文件预分配配合使用。
package persist

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// ShardState 单个分片的持久化进度：[Start,End) 内已连续覆盖 Done 字节前缀。
type ShardState struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Done  int64 `json:"done"` // 自 Start 起连续完成的前缀长度
}

// State 单个任务的持久化状态。
type State struct {
	ID        string       `json:"id"`
	URL       string       `json:"url"`
	FileSize  int64        `json:"file_size"`
	Done      int64        `json:"done"`
	Status    string       `json:"status"`
	UpdatedAt int64        `json:"updated_at"`       // unix nanos
	Shards    []ShardState `json:"shards,omitempty"` // 每分片进度（断点续传）
}

// Store 是持久化存储句柄。
type Store struct {
	dir  string
	mu   sync.RWMutex
	data map[string]*State
}

// Open 打开/创建持久化目录。目录权限 0700（合规：state.json 含完整 URL，
// 可能携带 query 中的 token 等敏感信息，仅限属主可读）。
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("persist: empty dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, data: make(map[string]*State)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Put 更新并立即落盘某任务状态（原子写：先 .tmp 再 rename）。
func (s *Store) Put(st *State) error {
	if st == nil || st.ID == "" {
		return errors.New("persist: invalid state")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[st.ID] = st
	return s.flushLocked()
}

// Get 读取某任务状态（用于断点续传恢复）。
func (s *Store) Get(id string) (*State, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.data[id]
	return st, ok
}

// All 返回所有状态快照。
func (s *Store) All() []*State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*State, 0, len(s.data))
	for _, st := range s.data {
		cp := *st
		out = append(out, &cp)
	}
	return out
}

// Remove 删除某任务状态。
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, id)
	return s.flushLocked()
}

func (s *Store) path() string { return filepath.Join(s.dir, "state.json") }

// flushLocked 将内存快照原子写入磁盘（0600：状态文件含 URL 凭据，仅属主可读）。
func (s *Store) flushLocked() error {
	tmp := s.path() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(s.data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

// load 从磁盘恢复状态（文件不存在视为空状态，不报错）。
func (s *Store) load() error {
	f, err := os.Open(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(&s.data)
}
