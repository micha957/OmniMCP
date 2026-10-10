package zhihu

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugin/plugintest"
)

// editor autosaves to /p/42/edit once the body is typed.
type editor struct{ *plugintest.Page }

func (e *editor) TypeText(ctx context.Context, sel, v string) error {
	if sel == selArticleBody {
		e.URL = "https://zhuanlan.zhihu.com/p/42/edit"
	}
	return e.Page.TypeText(ctx, sel, v)
}

func newEditor(publishWorks bool) *editor {
	p := plugintest.New()
	p.SetElement(selArticleTitle, plugin.ElementInfo{Exists: true, Visible: true})
	p.OnClick = func(p *plugintest.Page, sel string, opt plugin.ClickOptions) error {
		if opt.Text != "发布" {
			return fmt.Errorf("not found")
		}
		if publishWorks {
			p.URL = "https://zhuanlan.zhihu.com/p/42"
		}
		return nil
	}
	return &editor{p}
}

func run(t *testing.T, b plugin.Browser, mode string) (any, error) {
	return New().Execute(context.Background(), &plugin.Context{Browser: b}, "publish_article", json.RawMessage(`{"title":"标题","content":"正文","mode":"`+mode+`"}`))
}

func TestArticleDraftReturnsAutosavedURL(t *testing.T) {
	e := newEditor(true)
	out, err := run(t, e, "draft")
	if err != nil {
		t.Fatal(err)
	}
	if m := out.(map[string]any); m["status"] != "draft_saved" || m["url"] != "https://zhuanlan.zhihu.com/p/42/edit" || len(e.Clicks) != 0 {
		t.Fatalf("%v clicks=%v", m, e.Clicks)
	}
}

func TestArticlePublishRequiresArticlePage(t *testing.T) {
	out, err := run(t, newEditor(true), "publish")
	if err != nil || out.(map[string]any)["url"] != "https://zhuanlan.zhihu.com/p/42" {
		t.Fatalf("%v %v", out, err)
	}
	// Publish click that leaves the page on the draft must be reported as failure.
	defer func(d time.Duration) { publishWait = d }(publishWait)
	publishWait = 2 * time.Second
	if _, err := run(t, newEditor(false), "publish"); err == nil {
		t.Fatal("staying on /edit must not count as published")
	}
}
