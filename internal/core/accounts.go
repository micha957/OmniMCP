package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// AccountRecord is the local dashboard representation of an account. Secrets are
// kept in a 0600 file and are never returned by the API.
type AccountRecord struct {
	PluginID  string            `json:"plugin_id"`
	AccountID string            `json:"account_id"`
	Label     string            `json:"label"`
	Secrets   map[string]string `json:"secrets,omitempty"`
}

type AccountStore struct {
	mu       sync.Mutex
	path     string
	accounts []AccountRecord
}

func NewAccountStore(dataDir string) (*AccountStore, error) {
	if dataDir == "" {
		dataDir = "data"
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	s := &AccountStore{path: filepath.Join(dataDir, "accounts.json")}
	if b, err := os.ReadFile(s.path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &s.accounts); err != nil {
			return nil, fmt.Errorf("read accounts: %w", err)
		}
	}
	s.applyEnv()
	return s, nil
}

func (s *AccountStore) applyEnv() {
	for _, a := range s.accounts {
		key := strings.ToUpper(strings.NewReplacer("-", "_", "/", "_", " ", "_").Replace(a.AccountID))
		switch a.PluginID {
		case "wechat":
			if v := a.Secrets["app_id"]; v != "" {
				_ = os.Setenv("OMNIMCP_WECHAT_"+key+"_APP_ID", v)
			}
			if v := a.Secrets["app_secret"]; v != "" {
				_ = os.Setenv("OMNIMCP_WECHAT_"+key+"_APP_SECRET", v)
			}
		case "bilibili":
			if v := a.Secrets["cookie"]; v != "" {
				_ = os.Setenv("OMNIMCP_BILIBILI_COOKIE_"+key, v)
			}
		}
	}
}

func (s *AccountStore) save() error {
	b, err := json.MarshalIndent(s.accounts, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
func (s *AccountStore) List() []AccountRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AccountRecord, len(s.accounts))
	copy(out, s.accounts)
	for i := range out {
		out[i].Secrets = nil
	}
	return out
}
func (s *AccountStore) Upsert(a AccountRecord) error {
	a.PluginID, a.AccountID = strings.TrimSpace(a.PluginID), strings.TrimSpace(a.AccountID)
	if a.PluginID == "" || a.AccountID == "" {
		return fmt.Errorf("plugin_id and account_id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.accounts {
		if s.accounts[i].PluginID == a.PluginID && s.accounts[i].AccountID == a.AccountID {
			s.accounts[i] = a
			s.applyEnv()
			return s.save()
		}
	}
	s.accounts = append(s.accounts, a)
	s.applyEnv()
	return s.save()
}
func (s *AccountStore) Delete(pluginID, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.accounts[:0]
	for _, a := range s.accounts {
		if a.PluginID != pluginID || a.AccountID != accountID {
			out = append(out, a)
		}
	}
	s.accounts = out
	return s.save()
}
