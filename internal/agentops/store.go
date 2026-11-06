package agentops

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

//go:embed seed.json
var seed []byte

// Store owns a single process's state. Writes replace the file atomically and
// publish the new in-memory snapshot only after persistence succeeds.
type Store struct {
	mu    sync.Mutex
	path  string
	state State
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	fresh := os.IsNotExist(err)
	if fresh {
		b = seed
	} else if err != nil {
		return nil, err
	}
	if len(b) > 32<<20 {
		return nil, fmt.Errorf("运营状态超过 32 MB，请先归档")
	}
	if err := json.Unmarshal(b, &s.state); err != nil {
		return nil, fmt.Errorf("读取运营状态失败，原文件保留: %w", err)
	}
	// Version 2 only adds fields, so a version 1 file upgrades in place.
	if s.state.Version != 1 && s.state.Version != 2 {
		return nil, fmt.Errorf("不支持的运营状态版本 %d", s.state.Version)
	}
	s.state.Version = 2
	if err := s.state.Config.Validate(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for i := range s.state.Drafts {
		if err := s.state.Drafts[i].Validate(); err != nil {
			return nil, fmt.Errorf("草稿 %s: %w", s.state.Drafts[i].ID, err)
		}
		if fresh {
			s.state.Drafts[i].CreatedAt = now
			s.state.Drafts[i].UpdatedAt = now
		}
	}
	for i := range s.state.Runs {
		if s.state.Runs[i].Status == "running" {
			s.state.Runs[i].Status = "interrupted"
			s.state.Runs[i].Summary = "服务重启中断了上次运行；已保存的草稿保留，未完成任务可重新运行"
			s.state.Runs[i].EndedAt = now
		}
	}
	if err := s.persist(s.state); err != nil {
		return nil, err
	}
	return s, nil
}

// AssetDir is the local material library, next to the state file.
func (s *Store) AssetDir() string { return filepath.Join(filepath.Dir(s.path), "assets") }

func clone(s State) State {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

func (s *Store) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.state)
}

func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.state)
	if err := fn(&next); err != nil {
		return err
	}
	if err := s.persist(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) persist(next State) error {
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 32<<20 {
		return fmt.Errorf("运营状态超过 32 MB，请归档后重试")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".agentops-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), s.path)
}

func writeJSON(w io.Writer, v any) error {
	return json.NewEncoder(w).Encode(v)
}
