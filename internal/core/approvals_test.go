package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heybox/omnimcp/plugin"
)

// writePlugin counts real executions so tests can prove nothing ran unapproved.
type writePlugin struct {
	runs atomic.Int32
	err  error
	last atomic.Value
}

func (*writePlugin) Manifest() plugin.Manifest { return plugin.Manifest{ID: "w", Name: "W"} }
func (*writePlugin) Tools() []plugin.Tool {
	return []plugin.Tool{{Name: "post", Description: "post", Mutating: true, InputSchema: plugin.ToolSchema(`{"content":{"type":"string"}}`, []string{"content"})}}
}
func (p *writePlugin) Execute(_ context.Context, c *plugin.Context, _ string, raw json.RawMessage) (any, error) {
	p.runs.Add(1)
	p.last.Store(string(raw))
	if p.err != nil {
		return nil, p.err
	}
	return map[string]any{"comment_id": "c1", "account": c.Account.AccountID}, nil
}

func approvalServer(t *testing.T) (*Server, *writePlugin) {
	t.Helper()
	store, err := NewApprovalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	p := &writePlugin{}
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(NewApprovalsPlugin(store)); err != nil {
		t.Fatal(err)
	}
	return &Server{Registry: r, Logger: DefaultLogger(), Browser: plugin.NewMemoryBrowser(), Approvals: store}, p
}

func propose(t *testing.T, s *Server, content string) map[string]any {
	t.Helper()
	v := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"w_post","arguments":{"content":"`+content+`","_account_id":"main","_note":"依据"}}}`)
	res := v["result"].(map[string]any)
	return res["structuredContent"].(map[string]any)
}

func TestMutatingCallOnlyProposes(t *testing.T) {
	s, p := approvalServer(t)
	out := propose(t, s, "你好")
	if out["status"] != "approval_required" || p.runs.Load() != 0 {
		t.Fatalf("write must not run before approval: %v runs=%d", out, p.runs.Load())
	}
	a, ok := s.Approvals.Get(out["approval_id"].(string))
	if !ok || a.AccountID != "main" || a.Note != "依据" || strings.Contains(string(a.Args), "_account_id") {
		t.Fatalf("proposal stored wrong: %+v", a)
	}
}

func TestWriteWithoutQueueIsRefused(t *testing.T) {
	s := fakeServer(t)
	v := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fake_fail"}}`)
	text := v["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "审批队列未启用") {
		t.Fatalf("missing queue must fail closed: %s", text)
	}
}

func TestApproveRunsStoredContentOnce(t *testing.T) {
	s, p := approvalServer(t)
	out := propose(t, s, "你好")
	id, hash := out["approval_id"].(string), out["hash"].(string)
	if _, err := s.Approve(id, "wrong"); err == nil {
		t.Fatal("stale hash must be refused")
	}
	if _, err := s.Approve(id, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(id, hash); err == nil {
		t.Fatal("an approval must only run once")
	}
	s.WaitApprovals()
	a, _ := s.Approvals.Get(id)
	if a.Status != ApprovalSucceeded || p.runs.Load() != 1 || !strings.Contains(string(a.Result), "c1") {
		t.Fatalf("unexpected outcome: %+v runs=%d", a, p.runs.Load())
	}
	if again := propose(t, s, "你好"); again["status"] != "already_executed" || p.runs.Load() != 1 {
		t.Fatalf("same content must not be sent twice: %v", again)
	}
}

func TestEditChangesHashAndExecutedContent(t *testing.T) {
	s, p := approvalServer(t)
	out := propose(t, s, "原文")
	id, oldHash := out["approval_id"].(string), out["hash"].(string)
	a, err := s.Approvals.Edit(id, json.RawMessage(`{"content":"改过"}`))
	if err != nil || a.Hash == oldHash || !a.Edited {
		t.Fatalf("edit: %+v %v", a, err)
	}
	if _, err := s.Approve(id, oldHash); err == nil {
		t.Fatal("approving what was seen before the edit must fail")
	}
	if _, err := s.Approve(id, a.Hash); err != nil {
		t.Fatal(err)
	}
	s.WaitApprovals()
	if got := p.last.Load().(string); !strings.Contains(got, "改过") {
		t.Fatalf("edited content not executed: %s", got)
	}
}

func TestUnknownOutcomeIsNotRetriedOrDuplicated(t *testing.T) {
	s, p := approvalServer(t)
	p.err = fmt.Errorf("已点击发送: %w", plugin.ErrOutcomeUnknown)
	out := propose(t, s, "内容")
	id, hash := out["approval_id"].(string), out["hash"].(string)
	if _, err := s.Approve(id, hash); err != nil {
		t.Fatal(err)
	}
	s.WaitApprovals()
	if a, _ := s.Approvals.Get(id); a.Status != ApprovalUnknown {
		t.Fatalf("want unknown, got %+v", a)
	}
	if again := propose(t, s, "内容"); again["approval_id"] != id || p.runs.Load() != 1 {
		t.Fatalf("unknown outcome must block a new proposal: %v", again)
	}
	p.err = nil
	if _, err := s.Retry(id, hash); err != nil {
		t.Fatal(err)
	}
	s.WaitApprovals()
	if a, _ := s.Approvals.Get(id); a.Status != ApprovalSucceeded || a.Attempts != 2 {
		t.Fatalf("retry outcome: %+v", a)
	}
}

func TestExecutingBecomesUnknownAfterRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := NewApprovalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := store.Propose("w_post", "w", "main", json.RawMessage(`{"content":"x"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.begin(a.ID, a.Hash, ApprovalPending); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewApprovalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Get(a.ID); got.Status != ApprovalUnknown {
		t.Fatalf("crash during execution must read as unknown: %+v", got)
	}
}

func TestPendingExpires(t *testing.T) {
	store, err := NewApprovalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, _, _ := store.Propose("w_post", "w", "main", json.RawMessage(`{}`), "")
	store.now = func() time.Time { return time.Now().Add(approvalTTL + time.Minute) }
	if got, _ := store.Get(a.ID); got.Status != ApprovalExpired {
		t.Fatalf("want expired, got %+v", got)
	}
	if _, err := store.begin(a.ID, a.Hash, ApprovalPending); err == nil {
		t.Fatal("expired entry must not run")
	}
}

func TestMissingRequiredArgRefusedBeforeQueue(t *testing.T) {
	s, _ := approvalServer(t)
	v := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"w_post","arguments":{}}}`)
	if v["result"].(map[string]any)["isError"] != true || len(s.Approvals.List("", 0)) != 0 {
		t.Fatalf("invalid proposal should not reach the queue: %v", v)
	}
}

func TestNoApprovalToolInMCP(t *testing.T) {
	s, _ := approvalServer(t)
	v := rpc(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	for _, x := range v["result"].(map[string]any)["tools"].([]any) {
		name := x.(map[string]any)["name"].(string)
		if strings.Contains(name, "approve") || strings.Contains(name, "reject") {
			t.Fatalf("a model must not be able to decide approvals: %s", name)
		}
	}
}

func TestAutoPolicyRunsListedToolsWithinLimit(t *testing.T) {
	s, p := approvalServer(t)
	s.Auto = AutoPolicy{"w_post": 2}
	first := propose(t, s, "第一条")
	s.WaitApprovals()
	if first["status"] != "executing" || p.runs.Load() != 1 {
		t.Fatalf("listed tool should run: %v runs=%d", first, p.runs.Load())
	}
	if a, _ := s.Approvals.Get(first["approval_id"].(string)); a.Status != ApprovalSucceeded || !a.Auto {
		t.Fatalf("ledger must record it: %+v", a)
	}
	propose(t, s, "第二条")
	s.WaitApprovals()
	third := propose(t, s, "第三条")
	s.WaitApprovals()
	if third["status"] != "approval_required" || p.runs.Load() != 2 {
		t.Fatalf("over the limit must wait for a person: %v runs=%d", third, p.runs.Load())
	}
}

func TestAutoPolicyKeepsDuplicateAndUnknownGuards(t *testing.T) {
	s, p := approvalServer(t)
	s.Auto = AutoPolicy{"w_post": 10}
	p.err = fmt.Errorf("已点击: %w", plugin.ErrOutcomeUnknown)
	first := propose(t, s, "同一条")
	s.WaitApprovals()
	again := propose(t, s, "同一条")
	s.WaitApprovals()
	if again["approval_id"] != first["approval_id"] || again["status"] != ApprovalUnknown || p.runs.Load() != 1 {
		t.Fatalf("an unknown result must never be resent: %v runs=%d", again, p.runs.Load())
	}
}

func TestUnlistedToolStillWaits(t *testing.T) {
	s, p := approvalServer(t)
	s.Auto = AutoPolicy{"w_other": 10}
	if out := propose(t, s, "内容"); out["status"] != "approval_required" || p.runs.Load() != 0 {
		t.Fatalf("%v", out)
	}
}

func TestParseAutoPolicy(t *testing.T) {
	policy, err := ParseAutoPolicy(" a_x:2, b_y:30 ,")
	if err != nil || policy["a_x"] != 2 || policy["b_y"] != 30 || len(policy) != 2 {
		t.Fatalf("%v %v", policy, err)
	}
	for _, bad := range []string{"a_x", "a_x:0", "a_x:many", ":3"} {
		if _, err := ParseAutoPolicy(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if p, err := ParseAutoPolicy(""); err != nil || len(p) != 0 {
		t.Fatal("empty means nothing runs automatically")
	}
}

func TestApprovalHTTPGuard(t *testing.T) {
	t.Setenv("OMNIMCP_API_TOKEN", "mcp-token")
	t.Setenv("OMNIMCP_APPROVAL_TOKEN", "")
	s, p := approvalServer(t)
	out := propose(t, s, "内容")
	id, hash := out["approval_id"].(string), out["hash"].(string)
	h := (&Dashboard{Server: s}).Handler()
	body := fmt.Sprintf(`{"hash":%q}`, hash)
	send := func(mutate func(*http.Request)) int {
		req := httptest.NewRequest("POST", "http://127.0.0.1:8090/api/approvals/"+id+"/approve", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5555"
		req.Header.Set("Content-Type", "application/json")
		if mutate != nil {
			mutate(req)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	cases := map[string]func(*http.Request){
		"remote peer":    func(r *http.Request) { r.RemoteAddr = "10.0.0.8:5555" },
		"rebinding":      func(r *http.Request) { r.Host = "evil.example" },
		"cross-site":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"foreign origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"form post":      func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
	}
	for name, mutate := range cases {
		if code := send(mutate); code != http.StatusForbidden {
			t.Fatalf("%s: want 403, got %d", name, code)
		}
	}
	if p.runs.Load() != 0 {
		t.Fatal("guarded requests must not execute")
	}
	t.Setenv("OMNIMCP_APPROVAL_TOKEN", "human-token")
	if code := send(func(r *http.Request) { r.Header.Set("Authorization", "Bearer mcp-token") }); code != http.StatusUnauthorized {
		t.Fatalf("the MCP token must not open approvals, got %d", code)
	}
	if code := send(func(r *http.Request) { r.Header.Set("Authorization", "Bearer human-token") }); code != http.StatusOK {
		t.Fatalf("approval token rejected: %d", code)
	}
	s.WaitApprovals()
	if p.runs.Load() != 1 {
		t.Fatalf("runs=%d", p.runs.Load())
	}
}
