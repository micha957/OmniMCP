package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugins/xiaohongshu"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestToolsAreRegisteredFromPlugin(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(xiaohongshu.New()); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("xiaohongshu_search_notes"); !ok {
		t.Fatal("tool missing")
	}
}

func TestDisabledPluginIsHidden(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(xiaohongshu.New()); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEnabled("xiaohongshu", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("xiaohongshu_search_notes"); !ok {
		t.Fatal("lookup should remain available for direct internal use")
	}
	if len(r.Tools()) != 0 {
		t.Fatal("disabled plugin tools should not be listed")
	}
}

func TestArtifactsStayInsideRoot(t *testing.T) {
	d, err := os.MkdirTemp("", "omnimcp-artifacts-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(d)
	a, err := NewLocalArtifacts(d)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Save(context.Background(), "../note.txt", strings.NewReader("ok"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, d+string(os.PathSeparator)) {
		t.Fatalf("artifact escaped root: %s", p)
	}
}

// fakePlugin exercises the runtime without a real browser.
type fakePlugin struct{ calls chan plugin.Account }

func (fakePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "fake", Name: "Fake", RequiresBrowser: true}
}
func (fakePlugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "echo", Description: "echo", InputSchema: plugin.ToolSchema(`{"v":{"type":"string"}}`, []string{"v"})},
		{Name: "fail", Description: "fail", Mutating: true, InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "boom", Description: "panic", InputSchema: plugin.ToolSchema(`{}`, nil)},
	}
}
func (f fakePlugin) Execute(_ context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "echo":
		var in map[string]any
		_ = json.Unmarshal(raw, &in)
		return map[string]any{"account": c.Account.AccountID, "args": in}, nil
	case "fail":
		return nil, fmt.Errorf("platform said no")
	default:
		panic("selector exploded")
	}
}

func rpc(t *testing.T, s *Server, line string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(line+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		return nil
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	return v
}

func fakeServer(t *testing.T) *Server {
	r := NewRegistry()
	if err := r.Register(fakePlugin{}); err != nil {
		t.Fatal(err)
	}
	return &Server{Registry: r, Logger: DefaultLogger(), Browser: plugin.NewMemoryBrowser()}
}

func TestToolsListIsSortedAndAnnotated(t *testing.T) {
	v := rpc(t, fakeServer(t), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := v["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, x := range tools {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "fake_boom,fake_echo,fake_fail" {
		t.Fatalf("tools not sorted: %v", names)
	}
	fail := tools[2].(map[string]any)
	ann := fail["annotations"].(map[string]any)
	if ann["readOnlyHint"] != false || ann["destructiveHint"] != true {
		t.Fatalf("mutating tool annotations wrong: %v", ann)
	}
	props := fail["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if _, ok := props["_account_id"]; !ok {
		t.Fatal("_account_id should be advertised in every schema")
	}
}

func TestCallRoutesAccountAndStripsIt(t *testing.T) {
	v := rpc(t, fakeServer(t), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fake_echo","arguments":{"v":"x","_account_id":"main"}}}`)
	sc := v["result"].(map[string]any)["structuredContent"].(map[string]any)
	if sc["account"] != "main" {
		t.Fatalf("account not routed: %v", sc)
	}
	if _, leaked := sc["args"].(map[string]any)["_account_id"]; leaked {
		t.Fatal("_account_id must not reach the plugin")
	}
}

func TestToolErrorsAndPanicsBecomeIsError(t *testing.T) {
	s := fakeServer(t)
	for _, tool := range []string{"fake_fail", "fake_boom"} {
		v := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`"}}`)
		if v["error"] != nil {
			t.Fatalf("%s: tool failure must not be a protocol error: %v", tool, v)
		}
		if res := v["result"].(map[string]any); res["isError"] != true {
			t.Fatalf("%s: want isError, got %v", tool, res)
		}
	}
	v := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`)
	if v["error"] == nil {
		t.Fatal("unknown tool should be a protocol error")
	}
}

func TestPingNegotiationAndNotifications(t *testing.T) {
	s := fakeServer(t)
	if v := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"ping"}`); v["error"] != nil {
		t.Fatalf("ping failed: %v", v)
	}
	v := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if got := v["result"].(map[string]any)["protocolVersion"]; got != "2025-03-26" {
		t.Fatalf("version not negotiated: %v", got)
	}
	if v := rpc(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); v != nil {
		t.Fatalf("notifications must not be answered: %v", v)
	}
}

func TestHTTPNotificationAndBatch(t *testing.T) {
	t.Setenv("OMNIMCP_API_TOKEN", "")
	h := (&Dashboard{Server: fakeServer(t)}).Handler()
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", strings.NewReader(body)))
		return rec
	}
	if rec := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`); rec.Code != 202 || rec.Body.Len() != 0 {
		t.Fatalf("notification: %d %q", rec.Code, rec.Body.String())
	}
	rec := post(`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`)
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out) != 2 {
		t.Fatalf("batch: %v %s", err, rec.Body.String())
	}
}

func TestMCPCallReachesPlugin(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(xiaohongshu.New())
	s := &Server{Registry: r, Logger: DefaultLogger(), Browser: plugin.NewMemoryBrowser()}
	v := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"xiaohongshu_search_notes","arguments":{"keyword":"AI"}}}`)
	res := v["result"].(map[string]any)
	// MemoryBrowser lacks the extension capabilities, so the plugin must say so.
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] != true || !strings.Contains(text, "reload the OmniMCP extension") {
		t.Fatalf("unexpected result: %v", res)
	}
}
