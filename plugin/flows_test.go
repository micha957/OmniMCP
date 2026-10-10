package plugin

import (
	"context"
	"strings"
	"testing"
	"time"
)

// commentPage keeps typed text in the editor until submit moves it to the page.
type commentPage struct {
	*MemoryBrowser
	editor, posted string
	submitWorks    bool
	pressed        string
}

func (p *commentPage) Query(_ context.Context, sel string) (ElementInfo, error) {
	if sel == "#box" {
		return ElementInfo{Exists: true, Visible: true, Text: p.editor}, nil
	}
	return ElementInfo{}, nil
}
func (p *commentPage) WaitFor(_ context.Context, _, text string, _ time.Duration) error {
	if strings.Contains(p.posted, text) {
		return nil
	}
	return context.DeadlineExceeded
}
func (p *commentPage) Scroll(context.Context, string) error       { return nil }
func (p *commentPage) CurrentURL(context.Context) (string, error) { return "", nil }
func (p *commentPage) ClickElement(_ context.Context, sel string, _ ClickOptions) error {
	if sel == "#send" && p.submitWorks {
		p.posted, p.editor = p.editor, ""
		return nil
	}
	return context.Canceled
}
func (p *commentPage) TypeText(_ context.Context, _, v string) error { p.editor = v; return nil }
func (p *commentPage) PressKey(_ context.Context, k string) error    { p.pressed = k; return nil }

func TestPostCommentVerifiesSubmission(t *testing.T) {
	flow := CommentFlow{Inputs: []string{"#missing", "#box"}, Submits: []ClickTarget{{Selector: "#send"}}, Verify: 1500 * time.Millisecond}
	ok := &commentPage{MemoryBrowser: NewMemoryBrowser(), submitWorks: true}
	if err := PostComment(context.Background(), ok, flow, "你好世界"); err != nil {
		t.Fatal(err)
	}
	// Submit button absent: falls back to Enter, but the text never leaves the
	// editor, so the flow must report failure instead of success.
	stuck := &commentPage{MemoryBrowser: NewMemoryBrowser()}
	if err := PostComment(context.Background(), stuck, flow, "你好世界"); err == nil {
		t.Fatal("unsubmitted comment reported as success")
	}
	if stuck.pressed != "Enter" {
		t.Fatalf("expected Enter fallback, got %q", stuck.pressed)
	}
}
