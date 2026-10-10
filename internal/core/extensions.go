package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/heybox/omnimcp/plugin"
)

type extensionMessage struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Method string `json:"method,omitempty"`
	// Scope is the plugin a command belongs to; the extension keeps one tab per scope.
	Scope  string          `json:"scope,omitempty"`
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
		c.mu.Lock()
		for id, ch := range c.pending {
			ch <- extensionMessage{ID: id, Error: "browser extension disconnected"}
			delete(c.pending, id)
		}
		c.mu.Unlock()
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

// AllPlugins is the plugin_id an extension uses to serve every platform for
// one account (its Chrome profile) over a single connection.
const AllPlugins = "*"

func (m *ExtensionManager) connected(pluginID, accountID string) *extensionConn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if c := m.conns[extensionKey(pluginID, accountID)]; c != nil {
		return c
	}
	return m.conns[extensionKey(AllPlugins, accountID)]
}
func (m *ExtensionManager) call(ctx context.Context, pluginID, accountID, method string, params any) (json.RawMessage, error) {
	c := m.connected(pluginID, accountID)
	if c == nil {
		return nil, fmt.Errorf("浏览器扩展未连接账号 %s：在扩展弹窗里选择「全部平台」或 %s，账号填 %s 后保存连接", accountID, pluginID, accountID)
	}
	id := fmt.Sprintf("%d", m.seq.Add(1))
	result := make(chan extensionMessage, 1)
	c.mu.Lock()
	c.pending[id] = result
	c.mu.Unlock()
	c.writeMu.Lock()
	err := c.ws.WriteJSON(extensionMessage{ID: id, Type: "command", Method: method, Scope: pluginID, Params: params})
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

// uploadChunkSize keeps each base64 message well under Chrome's 64MB
// runtime-message limit while avoiding thousands of round trips.
const uploadChunkSize = 4 << 20

func (b *extensionBrowser) Upload(ctx context.Context, selector, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	id := fmt.Sprintf("u%d", b.m.seq.Add(1))
	if _, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "upload_begin", map[string]any{"id": id}); err != nil {
		return err
	}
	buf := make([]byte, uploadChunkSize)
	for {
		n, readErr := io.ReadFull(f, buf)
		if n > 0 {
			if _, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "upload_chunk", map[string]any{"id": id, "data": base64.StdEncoding.EncodeToString(buf[:n])}); err != nil {
				return err
			}
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	_, err = b.m.call(ctx, b.account.PluginID, b.account.AccountID, "upload_commit", map[string]any{"id": id, "selector": selector, "name": filepath.Base(path), "mimeType": mime.TypeByExtension(filepath.Ext(path))})
	return err
}
func (b *extensionBrowser) ExtractList(ctx context.Context, spec plugin.ExtractSpec) ([]map[string]string, error) {
	v, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "extract_list", map[string]any{"spec": spec})
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []map[string]string `json:"items"`
	}
	err = json.Unmarshal(v, &out)
	return out.Items, err
}
func (b *extensionBrowser) ReadScriptJSON(ctx context.Context, selector string) (json.RawMessage, error) {
	v, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "script_json", map[string]any{"selector": selector})
	if err != nil {
		return nil, err
	}
	var out struct {
		JSON string `json:"json"`
	}
	if err := json.Unmarshal(v, &out); err != nil {
		return nil, err
	}
	if !json.Valid([]byte(out.JSON)) {
		return nil, fmt.Errorf("%s does not contain valid JSON", selector)
	}
	return json.RawMessage(out.JSON), nil
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

func (b *extensionBrowser) Query(ctx context.Context, selector string) (plugin.ElementInfo, error) {
	var info plugin.ElementInfo
	v, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "query", map[string]any{"selector": selector})
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(v, &info)
	return info, err
}
func (b *extensionBrowser) WaitFor(ctx context.Context, selector, text string, timeout time.Duration) error {
	_, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "wait_for", map[string]any{"selector": selector, "text": text, "timeout_ms": timeout.Milliseconds()})
	return err
}
func (b *extensionBrowser) Scroll(ctx context.Context, selector string) error {
	_, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "scroll", map[string]any{"selector": selector})
	return err
}
func (b *extensionBrowser) CurrentURL(ctx context.Context) (string, error) {
	v, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "url", nil)
	if err != nil {
		return "", err
	}
	var out struct {
		URL string `json:"url"`
	}
	err = json.Unmarshal(v, &out)
	return out.URL, err
}
func (b *extensionBrowser) ReadGlobal(ctx context.Context, path string) (json.RawMessage, error) {
	v, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "read_global", map[string]any{"path": path})
	if err != nil {
		return nil, err
	}
	var out struct {
		JSON string `json:"json"`
	}
	if err := json.Unmarshal(v, &out); err != nil {
		return nil, err
	}
	if out.JSON == "" {
		return nil, fmt.Errorf("page global %s is not available", path)
	}
	return json.RawMessage(out.JSON), nil
}

func (b *extensionBrowser) ClickElement(ctx context.Context, selector string, opt plugin.ClickOptions) error {
	_, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "trusted_click", map[string]any{"selector": selector, "text": opt.Text, "exact": opt.Exact, "x_ratio": opt.XRatio})
	return err
}
func (b *extensionBrowser) TypeText(ctx context.Context, selector, value string) error {
	_, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "trusted_type", map[string]any{"selector": selector, "value": value})
	return err
}
func (b *extensionBrowser) PressKey(ctx context.Context, key string) error {
	_, err := b.m.call(ctx, b.account.PluginID, b.account.AccountID, "trusted_key", map[string]any{"key": key})
	return err
}

// Connected reports whether an extension is attached for the account.
func (m *ExtensionManager) Connected(a plugin.Account) bool {
	return m.connected(a.PluginID, a.AccountID) != nil
}

// Connections lists attached plugin/account pairs for the dashboard.
func (m *ExtensionManager) Connections() []plugin.Account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]plugin.Account, 0, len(m.conns))
	for k := range m.conns {
		parts := strings.SplitN(k, "/", 2)
		out = append(out, plugin.Account{PluginID: parts[0], AccountID: parts[1]})
	}
	return out
}

var _ plugin.Browser = (*extensionBrowser)(nil)
var _ plugin.Auth = (*extensionBrowser)(nil)
var _ plugin.Uploader = (*extensionBrowser)(nil)
var _ plugin.Inspector = (*extensionBrowser)(nil)
var _ plugin.TrustedInput = (*extensionBrowser)(nil)
var _ plugin.Extractor = (*extensionBrowser)(nil)
var _ plugin.StateReader = (*extensionBrowser)(nil)

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
