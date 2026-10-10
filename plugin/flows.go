package plugin

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CommentFlow describes how to post a comment on an already opened page.
// Selectors are tried in order; the first visible match wins.
type CommentFlow struct {
	// Openers reveal the editor (e.g. a "评论" button) when it is not visible yet.
	Openers []ClickTarget
	Inputs  []string
	// Submits are tried after typing; when none matches, Enter is pressed.
	Submits []ClickTarget
	// Verify is how long to wait for the comment to appear on the page.
	Verify time.Duration
}

// ClickTarget is a selector with an optional text filter.
type ClickTarget struct {
	Selector string
	Text     string
	Exact    bool
}

func (t ClickTarget) options() ClickOptions { return ClickOptions{Text: t.Text, Exact: t.Exact} }

// PostComment types content into the page's comment editor, submits it and
// confirms the text shows up outside the (now cleared) editor.
func PostComment(ctx context.Context, b Browser, flow CommentFlow, content string) error {
	in, ok1 := b.(Inspector)
	ti, ok2 := b.(TrustedInput)
	if !ok1 || !ok2 {
		return fmt.Errorf("browser connector does not support verified input; reload the OmniMCP extension")
	}
	// Openers often toggle the panel, so only click them when no editor is shown yet.
	if len(flow.Openers) > 0 && firstExisting(ctx, in, flow.Inputs, 1500*time.Millisecond) == "" {
		for _, o := range flow.Openers {
			if ti.ClickElement(ctx, o.Selector, o.options()) == nil {
				time.Sleep(800 * time.Millisecond)
				break
			}
		}
	}
	input := firstExisting(ctx, in, flow.Inputs, 8*time.Second)
	if input == "" {
		return fmt.Errorf("未找到评论输入框（尝试了 %s）", strings.Join(flow.Inputs, " | "))
	}
	if err := ti.TypeText(ctx, input, content); err != nil {
		return fmt.Errorf("输入评论失败: %w", err)
	}
	time.Sleep(500 * time.Millisecond)
	submitted := false
	for _, s := range flow.Submits {
		if ti.ClickElement(ctx, s.Selector, s.options()) == nil {
			submitted = true
			break
		}
	}
	if !submitted {
		if err := ti.PressKey(ctx, "Enter"); err != nil {
			return fmt.Errorf("提交评论失败: %w", err)
		}
	}
	return verifyPosted(ctx, in, input, content, flow.Verify)
}

func firstExisting(ctx context.Context, in Inspector, selectors []string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		for _, s := range selectors {
			if info, err := in.Query(ctx, s); err == nil && info.Exists && info.Visible {
				return s
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return ""
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// verifyPosted waits until the editor no longer holds the text while the page
// still shows it, i.e. the comment moved from the input into the list.
func verifyPosted(ctx context.Context, in Inspector, input, content string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	probe := []rune(strings.TrimSpace(content))
	if len(probe) > 15 {
		probe = probe[:15]
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		editor, _ := in.Query(ctx, input)
		if !strings.Contains(editor.Text, string(probe)) {
			if in.WaitFor(ctx, "", string(probe), time.Second) == nil {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("已提交，但未在页面上确认到该评论（可能被审核拦截或提交失败）")
}
