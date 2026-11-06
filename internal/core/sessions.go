package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/heybox/omnimcp/browser"
	"github.com/heybox/omnimcp/plugin"
)

const defaultAccount = "default"

// SessionManager runs every managed session as a tab in one shared browser. All
// tabs share one cookie jar, so a platform can only be logged in to one account:
// a request for another account fails instead of acting as the logged-in one.
type SessionManager struct {
	mu       sync.Mutex
	dataDir  string
	headless bool
	shared   *browser.Shared
	sessions map[string]*browser.RodBrowser
	state    sessionState
	connect  func() (*browser.Shared, error)
}

// sessionState survives restarts along with the browser itself.
type sessionState struct {
	// Accounts maps a platform to the account id bound to its login.
	Accounts map[string]string `json:"accounts"`
	// Tabs maps a platform to the target id of its tab.
	Tabs map[string]string `json:"tabs"`
}

func NewSessionManager(dataDir string, headless bool) *SessionManager {
	m := &SessionManager{dataDir: dataDir, headless: headless, sessions: map[string]*browser.RodBrowser{}, state: sessionState{Accounts: map[string]string{}, Tabs: map[string]string{}}}
	if b, err := os.ReadFile(m.statePath()); err == nil {
		_ = json.Unmarshal(b, &m.state)
	}
	if m.state.Accounts == nil {
		m.state.Accounts = map[string]string{}
	}
	if m.state.Tabs == nil {
		m.state.Tabs = map[string]string{}
	}
	m.connect = func() (*browser.Shared, error) {
		port := os.Getenv("OMNIMCP_CHROME_PORT")
		if port == "" {
			port = "9333"
		}
		return browser.ConnectShared(browser.SharedOptions{Headless: headless, DataDir: dataDir, Port: port, Bin: os.Getenv("OMNIMCP_CHROME_BIN")})
	}
	return m
}

func (m *SessionManager) statePath() string {
	dir := m.dataDir
	if dir == "" {
		dir = "data"
	}
	return filepath.Join(dir, "profiles", "shared-sessions.json")
}

// saveState is best effort: a lost file only costs an extra tab after a restart.
func (m *SessionManager) saveState() {
	b, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return
	}
	path := m.statePath()
	if os.MkdirAll(filepath.Dir(path), 0o700) == nil {
		_ = os.WriteFile(path, b, 0o600)
	}
}

// bindAccount enforces one account per platform. "default" means the caller made
// no choice, so it follows the bound account and never creates a binding. The
// generic browser plugin has no account of its own and is exempt.
func (m *SessionManager) bindAccount(a plugin.Account) error {
	account := strings.TrimSpace(a.AccountID)
	if a.PluginID == "browser" || account == "" || account == defaultAccount {
		return nil
	}
	bound := m.state.Accounts[a.PluginID]
	if bound == "" {
		m.state.Accounts[a.PluginID] = account
		m.saveState()
		return nil
	}
	if bound != account {
		return fmt.Errorf("%s 在共享浏览器里已绑定账号 %s，不能用 %s 操作：同一浏览器配置中，一个平台只能登录一个账号。需要多账号时请改用扩展模式并分别配置 Chrome Profile", a.PluginID, bound, account)
	}
	return nil
}

func (m *SessionManager) Get(a plugin.Account) (plugin.Browser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.bindAccount(a); err != nil {
		return nil, err
	}
	// The person can quit the browser at any time; reopen rather than fail.
	if m.shared == nil || !m.shared.Alive() {
		shared, err := m.connect()
		if err != nil {
			return nil, fmt.Errorf("连接浏览器失败: %w", err)
		}
		m.shared = shared
		m.sessions = map[string]*browser.RodBrowser{}
	}
	if b := m.sessions[a.PluginID]; b != nil {
		return b, nil
	}
	platform := a.PluginID
	b := m.shared.Tab(m.state.Tabs[platform], func(id string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.state.Tabs[platform] = id
		m.saveState()
	})
	m.sessions[platform] = b
	return b, nil
}

func (m *SessionManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shared == nil {
		return nil
	}
	return m.shared.Close()
}
