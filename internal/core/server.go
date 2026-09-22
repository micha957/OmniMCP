package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/plugin"
	"io"
	"log/slog"
	"os"
	"time"
)

type Server struct {
	Registry   *Registry
	Logger     *slog.Logger
	Browser    plugin.Browser
	Sessions   *SessionManager
	Extensions *ExtensionManager
	Artifacts  plugin.ArtifactStore
	// Confirm is called before mutating tools. A host application can replace it
	// with an approval UI, policy engine, or an audit-backed decision.
	Confirm func(context.Context, plugin.Tool, string) error
}
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(response{"2.0", nil, nil, map[string]any{"code": -32700, "message": err.Error()}})
			continue
		}
		res := s.handle(ctx, req)
		if req.ID != nil {
			if err := enc.Encode(res); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
func (s *Server) handle(ctx context.Context, req request) response {
	switch req.Method {
	case "initialize":
		return response{"2.0", req.ID, map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "omnimcp", "version": "0.1.0"}}, nil}
	case "notifications/initialized":
		return response{"2.0", req.ID, nil, nil}
	case "tools/list":
		return s.list(req.ID)
	case "tools/call":
		return s.call(ctx, req)
	default:
		return response{"2.0", req.ID, nil, map[string]any{"code": -32601, "message": "method not found"}}
	}
}
func (s *Server) list(id any) response {
	tools := []map[string]any{}
	for n, ref := range s.Registry.Tools() {
		tools = append(tools, map[string]any{"name": n, "description": ref.t.Description, "inputSchema": json.RawMessage(ref.t.InputSchema)})
	}
	return response{"2.0", id, map[string]any{"tools": tools}, nil}
}
func (s *Server) call(ctx context.Context, req request) response {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return response{"2.0", req.ID, nil, map[string]any{"code": -32602, "message": err.Error()}}
	}
	var rawArgs map[string]json.RawMessage
	if len(p.Arguments) > 0 {
		if err := json.Unmarshal(p.Arguments, &rawArgs); err != nil {
			return response{"2.0", req.ID, nil, map[string]any{"code": -32602, "message": "arguments must be an object"}}
		}
	}
	ref, ok := s.Registry.Lookup(p.Name)
	if !ok {
		return response{"2.0", req.ID, nil, map[string]any{"code": -32602, "message": "unknown tool"}}
	}
	confirm := s.Confirm
	if confirm == nil {
		confirm = func(context.Context, plugin.Tool, string) error { return nil }
	}
	accountID := "default"
	if rawArgs != nil {
		if v, ok := rawArgs["_account_id"]; ok {
			_ = json.Unmarshal(v, &accountID)
			delete(rawArgs, "_account_id")
			p.Arguments, _ = json.Marshal(rawArgs)
		}
	}
	account := plugin.Account{PluginID: ref.p.Manifest().ID, AccountID: accountID}
	var b plugin.Browser
	if os.Getenv("OMNIMCP_BROWSER_MODE") == "extension" && s.Extensions != nil && ref.p.Manifest().RequiresBrowser && ref.p.Manifest().BrowserMode != "managed" {
		b = s.Extensions.Browser(account)
	} else if s.Sessions != nil && ref.p.Manifest().RequiresBrowser {
		var err error
		b, err = s.Sessions.Get(account)
		if err != nil {
			return response{"2.0", req.ID, nil, map[string]any{"code": -32000, "message": err.Error()}}
		}
	} else {
		b = s.Browser
	}
	var unlock func()
	if s.Sessions != nil && ref.p.Manifest().RequiresBrowser {
		unlock = s.Sessions.Lock(account)
		defer unlock()
	}
	var auth plugin.Auth
	if a, ok := b.(plugin.Auth); ok {
		auth = a
	}
	c := &plugin.Context{Account: account, Browser: b, Auth: auth, Artifacts: s.Artifacts, Logger: s.Logger, RequestID: req.ID, ToolName: p.Name, Confirm: func(ctx context.Context, name string) error { return confirm(ctx, ref.t, name) }}
	if ref.t.Confirm {
		if err := c.Confirm(ctx, p.Name); err != nil {
			return response{"2.0", req.ID, nil, map[string]any{"code": -32000, "message": err.Error()}}
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := ref.p.Execute(callCtx, c, p.Name[len(ref.p.Manifest().ID)+1:], p.Arguments)
	if err != nil {
		return response{"2.0", req.ID, nil, map[string]any{"code": -32000, "message": err.Error()}}
	}
	return response{"2.0", req.ID, map[string]any{"content": []map[string]string{{"type": "text", "text": mustMarshal(result)}}}, nil}
}
func mustMarshal(v any) string    { b, _ := json.Marshal(v); return string(b) }
func DefaultLogger() *slog.Logger { return slog.New(slog.NewTextHandler(os.Stderr, nil)) }

var _ = fmt.Sprintf
