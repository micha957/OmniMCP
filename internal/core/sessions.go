package core

import (
	"fmt"
	"github.com/heybox/omnimcp/browser"
	"github.com/heybox/omnimcp/plugin"
	"path/filepath"
	"strings"
	"sync"
)

// SessionManager owns long-lived browsers. A session is isolated by plugin and account,
// so one platform/account can never accidentally reuse another account's cookies.
type SessionManager struct {
	mu       sync.Mutex
	dataDir  string
	headless bool
	sessions map[string]*session
}
type session struct {
	mu      sync.Mutex
	browser *browser.RodBrowser
}

func NewSessionManager(dataDir string, headless bool) *SessionManager {
	return &SessionManager{dataDir: dataDir, headless: headless, sessions: map[string]*session{}}
}
func (m *SessionManager) key(a plugin.Account) string {
	return safePart(a.PluginID) + "/" + safePart(a.AccountID)
}
func safePart(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "default"
	}
	v = filepath.Base(v)
	return strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(v)
}
func (m *SessionManager) Get(a plugin.Account) (plugin.Browser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(a)
	if s := m.sessions[k]; s != nil {
		return s.browser, nil
	}
	b, err := browser.NewRodBrowser(m.headless, m.dataDir, k)
	if err != nil {
		return nil, fmt.Errorf("create session %s: %w", k, err)
	}
	m.sessions[k] = &session{browser: b}
	return b, nil
}
func (m *SessionManager) Lock(a plugin.Account) func() {
	m.mu.Lock()
	s := m.sessions[m.key(a)]
	m.mu.Unlock()
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}
func (m *SessionManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var first error
	for k, s := range m.sessions {
		if err := s.browser.Close(); err != nil && first == nil {
			first = fmt.Errorf("close %s: %w", k, err)
		}
	}
	m.sessions = map[string]*session{}
	return first
}
