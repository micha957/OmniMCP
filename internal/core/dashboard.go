package core

import (
	"context"
	"embed"
	"encoding/json"
	"github.com/heybox/omnimcp/plugin"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed web/index.html
var dashboardFS embed.FS

type Dashboard struct {
	Server   *Server
	Accounts *AccountStore
}

func (d *Dashboard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := dashboardFS.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/api/plugins", d.plugins)
	mux.HandleFunc("/api/tools", d.tools)
	mux.HandleFunc("/api/tools/call", d.callTool)
	mux.HandleFunc("/api/accounts", d.accounts)
	mux.HandleFunc("/api/accounts/login", d.login)
	mux.HandleFunc("/mcp", d.mcp)
	mux.HandleFunc("/healthz", d.health)
	if d.Server.Extensions != nil {
		mux.HandleFunc("/extension/ws", d.Server.Extensions.HandleWS)
	}
	return withLocalHeaders(mux)
}

func (d *Dashboard) health(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, map[string]any{"ok": true, "service": "omnimcp"})
}

// mcp exposes the same JSON-RPC handler over HTTP for self-hosted deployments.
// Set OMNIMCP_API_TOKEN to require Bearer authentication; leaving it empty keeps
// the endpoint suitable for localhost-only development.
func (d *Dashboard) mcp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if token := strings.TrimSpace(os.Getenv("OMNIMCP_API_TOKEN")); token != "" {
		want := "Bearer " + token
		if r.Header.Get("Authorization") != want {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonOut(w, response{"2.0", nil, nil, map[string]any{"code": -32700, "message": err.Error()}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d.Server.handle(r.Context(), req))
}
func withLocalHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (d *Dashboard) plugins(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	type item struct {
		plugin.Manifest
		Enabled   bool `json:"enabled"`
		ToolCount int  `json:"tool_count"`
	}
	out := []item{}
	for _, m := range d.Server.Registry.Plugins() {
		n := 0
		for name := range d.Server.Registry.Tools() {
			if strings.HasPrefix(name, m.ID+"_") {
				n++
			}
		}
		out = append(out, item{m, d.Server.Registry.Enabled(m.ID), n})
	}
	jsonOut(w, out)
}

func (d *Dashboard) tools(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	type item struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
		PluginID    string          `json:"plugin_id"`
	}
	out := []item{}
	for name, ref := range d.Server.Registry.Tools() {
		out = append(out, item{Name: name, Description: ref.t.Description, InputSchema: ref.t.InputSchema, PluginID: ref.p.Manifest().ID})
	}
	jsonOut(w, out)
}

func (d *Dashboard) callTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		http.Error(w, "name and arguments are required", 400)
		return
	}
	if len(in.Arguments) == 0 {
		in.Arguments = json.RawMessage(`{}`)
	}
	res := d.Server.handle(r.Context(), request{JSONRPC: "2.0", ID: "dashboard", Method: "tools/call", Params: mustJSONRaw(map[string]any{"name": in.Name, "arguments": json.RawMessage(in.Arguments)})})
	if res.Error != nil {
		jsonOut(w, map[string]any{"ok": false, "error": res.Error})
		return
	}
	jsonOut(w, map[string]any{"ok": true, "result": res.Result})
}

func mustJSONRaw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func (d *Dashboard) accounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		jsonOut(w, d.Accounts.List())
	case "POST":
		var a AccountRecord
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := d.Accounts.Upsert(a); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		jsonOut(w, map[string]any{"ok": true})
	case "DELETE":
		if err := d.Accounts.Delete(r.URL.Query().Get("plugin_id"), r.URL.Query().Get("account_id")); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonOut(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
func (d *Dashboard) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if token := strings.TrimSpace(os.Getenv("OMNIMCP_API_TOKEN")); token != "" && r.Header.Get("Authorization") != "Bearer "+token {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var in struct {
		PluginID  string `json:"plugin_id"`
		AccountID string `json:"account_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	manifest := plugin.Manifest{}
	for _, m := range d.Server.Registry.Plugins() {
		if m.ID == in.PluginID {
			manifest = m
			break
		}
	}
	if !manifest.RequiresBrowser || manifest.LoginURL == "" {
		http.Error(w, "该插件没有声明浏览器登录能力", 400)
		return
	}
	if os.Getenv("OMNIMCP_BROWSER_MODE") != "extension" && d.Server.Sessions == nil {
		http.Error(w, "sessions unavailable", 500)
		return
	}
	if os.Getenv("OMNIMCP_BROWSER_MODE") == "extension" && manifest.BrowserMode != "managed" {
		jsonOut(w, map[string]any{"ok": true, "mode": "extension", "login_url": manifest.LoginURL, "message": "请在当前 Chrome 中完成登录"})
		return
	}
	// Browser startup and page loading can take longer than an HTTP request. Start
	// it in the background so the dashboard responds immediately; the persistent
	// profile is already attached to this account session.
	go func() {
		var b plugin.Browser
		var getErr error
		account := plugin.Account{PluginID: in.PluginID, AccountID: in.AccountID}
		if os.Getenv("OMNIMCP_BROWSER_MODE") == "extension" && d.Server.Extensions != nil && manifest.BrowserMode != "managed" {
			b = d.Server.Extensions.Browser(account)
		} else {
			b, getErr = d.Server.Sessions.Get(account)
		}
		if getErr != nil {
			d.Server.Logger.Error("create browser login session", "account", in.AccountID, "error", getErr)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		auth, ok := b.(plugin.Auth)
		if !ok {
			d.Server.Logger.Error("browser session does not support auth", "plugin", in.PluginID)
			return
		}
		if openErr := auth.OpenLogin(ctx, manifest.LoginURL); openErr != nil {
			d.Server.Logger.Error("open plugin login page", "plugin", in.PluginID, "account", in.AccountID, "error", openErr)
		}
	}()
	jsonOut(w, map[string]any{"ok": true, "message": "浏览器正在打开，请完成登录"})
}
