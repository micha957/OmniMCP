package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heybox/omnimcp/internal/core"
	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugins/bilibili"
)

// newCLI runs the real server handler so the CLI is tested against real schemas.
func newCLI(t *testing.T) (*cli, *bytes.Buffer, *bytes.Buffer, *core.Server) {
	t.Helper()
	t.Setenv("OMNIMCP_API_TOKEN", "")
	t.Setenv("OMNIMCP_APPROVAL_TOKEN", "")
	t.Setenv("BILIBILI_COOKIE", "bili_jct=x")
	store, err := core.NewApprovalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := core.NewRegistry()
	if err := r.Register(bilibili.New()); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(core.NewApprovalsPlugin(store)); err != nil {
		t.Fatal(err)
	}
	s := &core.Server{Registry: r, Logger: core.DefaultLogger(), Browser: plugin.NewMemoryBrowser(), Approvals: store}
	h := (&core.Dashboard{Server: s}).Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { h.ServeHTTP(w, req) }))
	t.Cleanup(srv.Close)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	return &cli{base: srv.URL, stdout: stdout, stderr: stderr, client: srv.Client(),
		confirm: func() (io.ReadCloser, error) { return nil, errors.New("no terminal") }}, stdout, stderr, s
}

func TestWriteReturnsApprovalExitCode(t *testing.T) {
	c, out, _, s := newCLI(t)
	code := c.run([]string{"bilibili", "post-comment", "--aid", "100", "--content", "你好", "--account", "main", "--note", "依据"})
	if code != exitApproval {
		t.Fatalf("want exit %d, got %d: %s", exitApproval, code, out.String())
	}
	if !strings.Contains(out.String(), "approval_required") {
		t.Fatal(out.String())
	}
	items := s.Approvals.List("pending", 0)
	if len(items) != 1 || items[0].AccountID != "main" || items[0].Note != "依据" {
		t.Fatalf("%+v", items)
	}
}

func TestFlagsFollowSchema(t *testing.T) {
	c, _, errOut, _ := newCLI(t)
	if code := c.run([]string{"bilibili", "post-comment", "--aid", "100"}); code != exitUsage || !strings.Contains(errOut.String(), "--content") {
		t.Fatalf("missing required: %d %s", code, errOut.String())
	}
	errOut.Reset()
	if code := c.run([]string{"bilibili", "post-comment", "--nope", "1"}); code != exitUsage || !strings.Contains(errOut.String(), "未知参数") {
		t.Fatalf("unknown flag: %d %s", code, errOut.String())
	}
	if code := c.run([]string{"bilibili", "nope"}); code != exitUsage {
		t.Fatalf("unknown command: %d", code)
	}
}

func TestToolsAndHelpComeFromServer(t *testing.T) {
	c, out, _, _ := newCLI(t)
	if code := c.run([]string{"tools", "bilibili"}); code != exitOK {
		t.Fatal(code)
	}
	if !strings.Contains(out.String(), "bilibili reply-comment\t[写·需确认]") || !strings.Contains(out.String(), "bilibili get-video\t[读]") || strings.Contains(out.String(), "omnimcp") {
		t.Fatal(out.String())
	}
	out.Reset()
	if code := c.run([]string{"bilibili", "publish-video", "--help"}); code != exitOK || !strings.Contains(out.String(), "--file-path <string> （必填）") || !strings.Contains(out.String(), "--account") {
		t.Fatal(out.String())
	}
}

func TestApproveNeedsARealTerminal(t *testing.T) {
	c, _, errOut, s := newCLI(t)
	c.run([]string{"bilibili", "post-comment", "--aid", "1", "--content", "x"})
	id := s.Approvals.List("pending", 0)[0].ID
	if code := c.run([]string{"approvals", "approve", id}); code != exitUsage || !strings.Contains(errOut.String(), "真实终端") {
		t.Fatalf("approve without a terminal: %d %s", code, errOut.String())
	}
	if a, _ := s.Approvals.Get(id); a.Status != core.ApprovalPending {
		t.Fatalf("must stay pending: %+v", a)
	}
}

func TestApproveAnswerComesFromTerminal(t *testing.T) {
	c, out, _, s := newCLI(t)
	c.run([]string{"bilibili", "post-comment", "--aid", "1", "--content", "x"})
	id := s.Approvals.List("pending", 0)[0].ID
	tty := filepath.Join(t.TempDir(), "tty")
	answer := func(text string) {
		if err := os.WriteFile(tty, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		c.confirm = func() (io.ReadCloser, error) { return os.Open(tty) }
	}
	answer("no\n")
	if code := c.run([]string{"approvals", "approve", id}); code != exitOK || !strings.Contains(out.String(), "未执行") {
		t.Fatalf("%d %s", code, out.String())
	}
	if a, _ := s.Approvals.Get(id); a.Status != core.ApprovalPending {
		t.Fatalf("anything but yes must not run: %+v", a)
	}
	answer("yes\n")
	if code := c.run([]string{"approvals", "approve", id}); code != exitOK {
		t.Fatalf("%d", code)
	}
	s.WaitApprovals()
	if a, _ := s.Approvals.Get(id); a.Status == core.ApprovalPending || a.Attempts != 1 {
		t.Fatalf("approval did not start: %+v", a)
	}
}

func TestRejectAndUnreachableServer(t *testing.T) {
	c, _, _, s := newCLI(t)
	c.run([]string{"bilibili", "post-comment", "--aid", "1", "--content", "x"})
	id := s.Approvals.List("pending", 0)[0].ID
	if code := c.run([]string{"approvals", "reject", id, "--reason", "不合适"}); code != exitOK {
		t.Fatal(code)
	}
	if a, _ := s.Approvals.Get(id); a.Status != core.ApprovalRejected || a.Reason != "不合适" {
		t.Fatalf("%+v", a)
	}
	c.base = "http://127.0.0.1:1"
	if code := c.run([]string{"tools"}); code != exitServer {
		t.Fatal(code)
	}
}
