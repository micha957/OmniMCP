package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/heybox/omnimcp/plugin"
)

type extensionMessage struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Method string          `json:"method,omitempty"`
	Params any             `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}
type extensionConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan extensionMessage
}
type ExtensionManager struct {
	mu       sync.RWMutex
	seq      atomic.Uint64
	conns    map[string]*extensionConn
	upgrader websocket.Upgrader
}

func NewExtensionManager() *ExtensionManager {
	return &ExtensionManager{conns: map[string]*extensionConn{}, upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}}
}
func extensionKey(pluginID, accountID string) string { return pluginID + "/" + accountID }
func (m *ExtensionManager) HandleWS(w http.ResponseWriter, r *http.Request) {
	if token := os.Getenv("OMNIMCP_API_TOKEN"); token != "" && r.URL.Query().Get("token") != token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	pluginID, accountID := r.URL.Query().Get("plugin_id"), r.URL.Query().Get("account_id")
	if pluginID == "" || accountID == "" {
		http.Error(w, "plugin_id and account_id are required", 400)
		return
	}
	ws, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &extensionConn{ws: ws, pending: map[string]chan extensionMessage{}}
	key := extensionKey(pluginID, accountID)
	m.mu.Lock()
	if old := m.conns[key]; old != nil {
		_ = old.ws.Close()
	}
	m.conns[key] = c
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.conns[key] == c {
			delete(m.conns, key)
		}
		m.mu.Unlock()
		_ = ws.Close()
	}()
	for {
		var msg extensionMessage
		if err := ws.ReadJSON(&msg); err != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[msg.ID]
		if ch != nil {
			ch <- msg
			delete(c.pending, msg.ID)
		}
		c.mu.Unlock()
	}
}
func (m *ExtensionManager) connected(pluginID, accountID string) *extensionConn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conns[extensionKey(pluginID, accountID)]
}
func (m *ExtensionManager) call(ctx context.Context, pluginID, accountID, method string, params any) (json.RawMessage, error) {
	c := m.connected(pluginID, accountID)
	if c == nil {
		return nil, fmt.Errorf("extension is not connected for %s/%s", pluginID, accountID)
	}
	id := fmt.Sprintf("%d", m.seq.Add(1))
	result := make(chan extensionMessage, 1)
	c.mu.Lock()
	c.pending[id] = result
	c.mu.Unlock()
	c.writeMu.Lock()
	err := c.ws.WriteJSON(extensionMessage{ID: id, Type: "command", Method: method, Params: params})
	c.writeMu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case msg := <-result:
		if msg.Error != "" {
			return nil, fmt.Errorf("extension: %s", msg.Error)
		}
		return msg.Result, nil
	}
}
func (m *ExtensionManager) Browser(account plugin.Account) plugin.Browser {
	return &extensionBrowser{m: m, account: account}
}

type extensionBrowser struct {
	m       *ExtensionManager
	account plugin.Account
}

func (b *extensionBrowser) Open(ctx context.Context, url string) error {
	_, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "open", map[string]any{"url": url})
	return err
}
func (b *extensionBrowser) Snapshot(ctx context.Context) (string, error) {
	v, e := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "snapshot", nil)
	if e != nil {
		return "", e
	}
	var out struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(v, &out)
	return out.Text, nil
}
func (b *extensionBrowser) Click(ctx context.Context, selector string) error {
	_, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "click", map[string]any{"selector": selector})
	return err
}
func (b *extensionBrowser) Fill(ctx context.Context, selector, value string) error {
	_, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "fill", map[string]any{"selector": selector, "value": value})
	return err
}
func (b *extensionBrowser) Upload(ctx context.Context, selector, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	v, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "upload", map[string]any{"selector": selector, "name": filepath.Base(path), "data": base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return err
	}
	var out struct {
		OK bool `json:"ok"`
	}
	_ = json.Unmarshal(v, &out)
	return nil
}
func (b *extensionBrowser) OpenLogin(ctx context.Context, url string) error { return b.Open(ctx, url) }
func (b *extensionBrowser) CookieHeader(ctx context.Context, domain string) (string, error) {
	v, e := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "cookies", map[string]any{"domain": domain})
	if e != nil {
		return "", e
	}
	var out struct {
		Cookie string `json:"cookie"`
	}
	_ = json.Unmarshal(v, &out)
	return out.Cookie, nil
}

var _ plugin.Browser = (*extensionBrowser)(nil)
var _ plugin.Auth = (*extensionBrowser)(nil)
var _ plugin.Uploader = (*extensionBrowser)(nil)
var _ = time.Second

func (b *extensionBrowser) ReadZhihu(ctx context.Context, mode string, limit int) (json.RawMessage, error) {
	v, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "read_zhihu", map[string]any{"mode": mode, "limit": limit})
	if err != nil {
		return nil, err
	}
	var out struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(v, &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("empty structured response")
	}
	return out.Data, nil
}
