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
	"runtime/debug"
	"sort"
	"sync"
	"time"
)

// DefaultToolTimeout applies to tools that do not declare their own Timeout.
// OMNIMCP_TOOL_TIMEOUT (a Go duration such as "3m") overrides it.
const DefaultToolTimeout = 2 * time.Minute

var supportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type Server struct {
	Registry   *Registry
	Logger     *slog.Logger
	Browser    plugin.Browser
	Sessions   *SessionManager
	Extensions *ExtensionManager
	Artifacts  plugin.ArtifactStore
	// Approvals receives every mutating tool call. Without it writes are refused,
	// so a missing queue can never mean "run without asking".
	Approvals    *ApprovalStore
	approvalRuns sync.WaitGroup
	// Auto lists the tools the owner chose to run without a confirmation click.
	Auto   AutoPolicy
	autoMu sync.Mutex

	// accountLocks serialises browser tools per plugin/account: both connectors
	// drive a single tab per account, so concurrent calls would interleave.
	accountLocks sync.Map
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

func rpcError(id any, code int, msg string) response {
	return response{"2.0", id, nil, map[string]any{"code": code, "message": msg}}
}

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(rpcError(nil, -32700, err.Error()))
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
		return s.initialize(req)
	case "ping":
		return response{"2.0", req.ID, map[string]any{}, nil}
	case "tools/list":
		return s.list(req.ID)
	case "tools/call":
		return s.call(ctx, req)
	default:
		// Notifications (initialized, cancelled, ...) need no reply.
		if req.ID == nil {
			return response{JSONRPC: "2.0"}
		}
		return rpcError(req.ID, -32601, "method not found")
	}
}
func (s *Server) initialize(req request) response {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &p)
	version := supportedProtocolVersions[0]
	for _, v := range supportedProtocolVersions {
		if v == p.ProtocolVersion {
			version = v
		}
	}
	return response{"2.0", req.ID, map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]string{"name": "omnimcp", "version": "0.2.0"},
		"instructions":    "工具名为 <平台>_<动作>。所有工具都接受可选参数 _account_id 选择平台账号（默认 default）。写操作（发布、评论、点赞等）不会立即执行：调用后进入待确认队列并返回 approval_id，用户确认后由服务端执行，结果用 omnimcp_approval_status 查询。可用 _note 向用户说明这项操作的依据。",
	}, nil}
}

func (s *Server) list(id any) response {
	all := s.Registry.Tools()
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	tools := make([]map[string]any, 0, len(names))
	for _, n := range names {
		ref := all[n]
		readOnly := !ref.t.Mutating
		tools = append(tools, map[string]any{
			"name":        n,
			"description": ref.t.Description,
			"inputSchema": withAccountParam(ref.t.InputSchema, ref.t.Mutating),
			"annotations": map[string]any{
				"title":           ref.p.Manifest().Name + " · " + ref.t.Name,
				"readOnlyHint":    readOnly,
				"destructiveHint": ref.t.Mutating,
				"openWorldHint":   true,
			},
		})
	}
	return response{"2.0", id, map[string]any{"tools": tools}, nil}
}

// withAccountParam advertises the runtime-level _account_id argument so models
// know they can route a call to a specific platform account.
func withAccountParam(schema json.RawMessage, mutating bool) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil || m == nil {
		m = map[string]any{"type": "object"}
	}
	props, _ := m["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	props["_account_id"] = map[string]any{"type": "string", "description": "平台账号 ID，默认 default"}
	if mutating {
		props["_note"] = map[string]any{"type": "string", "description": "向确认人说明这项操作的依据"}
	}
	m["properties"] = props
	if req, ok := m["required"]; ok && req == nil {
		delete(m, "required")
	}
	b, _ := json.Marshal(m)
	return b
}

func (s *Server) lockAccount(a plugin.Account) func() {
	v, _ := s.accountLocks.LoadOrStore(a.PluginID+"/"+a.AccountID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func toolTimeout(t plugin.Tool) time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	if d, err := time.ParseDuration(os.Getenv("OMNIMCP_TOOL_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return DefaultToolTimeout
}

// toolError reports execution failures inside the result so the model can see
// and react to them, as the MCP spec recommends for tool errors.
func toolError(id any, err error) response {
	return response{"2.0", id, map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": err.Error()}}}, nil}
}

func toolResult(id any, v any) response {
	b, err := json.Marshal(v)
	if err != nil {
		return toolError(id, fmt.Errorf("encode result: %w", err))
	}
	result := map[string]any{"content": []map[string]string{{"type": "text", "text": string(b)}}}
	if len(b) > 0 && b[0] == '{' {
		result["structuredContent"] = json.RawMessage(b)
	}
	return response{"2.0", id, result, nil}
}

func (s *Server) call(ctx context.Context, req request) (res response) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return rpcError(req.ID, -32602, err.Error())
	}
	rawArgs := map[string]json.RawMessage{}
	if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
		if err := json.Unmarshal(p.Arguments, &rawArgs); err != nil {
			return rpcError(req.ID, -32602, "arguments must be an object")
		}
	}
	ref, ok := s.Registry.Lookup(p.Name)
	if !ok || !s.Registry.Enabled(ref.p.Manifest().ID) {
		return rpcError(req.ID, -32602, "unknown tool: "+p.Name)
	}
	accountID := "default"
	if v, ok := rawArgs["_account_id"]; ok {
		_ = json.Unmarshal(v, &accountID)
		delete(rawArgs, "_account_id")
	}
	if accountID == "" {
		accountID = "default"
	}
	note := ""
	if v, ok := rawArgs["_note"]; ok {
		_ = json.Unmarshal(v, &note)
		delete(rawArgs, "_note")
	}
	args, _ := json.Marshal(rawArgs)
	manifest := ref.p.Manifest()
	account := plugin.Account{PluginID: manifest.ID, AccountID: accountID}
	logger := s.Logger
	if logger == nil {
		logger = DefaultLogger()
	}
	started := time.Now()
	defer func() {
		isErr := false
		if m, ok := res.Result.(map[string]any); ok {
			isErr, _ = m["isError"].(bool)
		}
		logger.Info("tool call", "tool", p.Name, "account", accountID, "mutating", ref.t.Mutating, "error", isErr || res.Error != nil, "duration", time.Since(started).Round(time.Millisecond))
	}()

	if ref.t.Mutating {
		return s.proposeWrite(req, p.Name, ref, accountID, args, note)
	}
	result, err := s.invoke(ctx, ref, account, args, req.ID, p.Name)
	if err != nil {
		return toolError(req.ID, err)
	}
	return toolResult(req.ID, result)
}

// invoke runs a tool on its account's browser. Callers decide whether a call may
// run at all: reads come straight from tools/call, writes only from Approve.
func (s *Server) invoke(ctx context.Context, ref toolRef, account plugin.Account, args json.RawMessage, requestID any, name string) (result any, err error) {
	logger := s.Logger
	if logger == nil {
		logger = DefaultLogger()
	}
	defer func() {
		if r := recover(); r != nil {
			logger.Error("tool panicked", "tool", name, "account", account.AccountID, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("工具执行异常: %v", r)
		}
	}()
	manifest := ref.p.Manifest()
	if manifest.RequiresBrowser {
		defer s.lockAccount(account)()
	}
	var b plugin.Browser
	if os.Getenv("OMNIMCP_BROWSER_MODE") == "extension" && s.Extensions != nil && manifest.RequiresBrowser && manifest.BrowserMode != "managed" {
		b = s.Extensions.Browser(account)
	} else if s.Sessions != nil && manifest.RequiresBrowser {
		if b, err = s.Sessions.Get(account); err != nil {
			return nil, err
		}
	} else {
		b = s.Browser
	}
	var auth plugin.Auth
	if a, ok := b.(plugin.Auth); ok {
		auth = a
	}
	c := &plugin.Context{Account: account, Browser: b, Auth: auth, Artifacts: s.Artifacts, Logger: logger, RequestID: requestID, ToolName: name}
	callCtx, cancel := context.WithTimeout(ctx, toolTimeout(ref.t))
	defer cancel()
	result, err = ref.p.Execute(callCtx, c, ref.t.Name, args)
	if err != nil {
		if callCtx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("%w（超时 %s）: %w", err, toolTimeout(ref.t), context.DeadlineExceeded)
		}
		return nil, err
	}
	return result, nil
}
func DefaultLogger() *slog.Logger { return slog.New(slog.NewTextHandler(os.Stderr, nil)) }
