package plugin_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugin/plugintest"
)

var flow = plugin.CreatorFlow{
	URL: "https://creator.test/upload", LoginHint: "/login", FileInput: "input[type=file]",
	Ready: []string{"#title"}, Title: []string{"#title"}, TitleMax: 5, Body: []string{"#body"},
	Publish:           []plugin.ClickTarget{{Selector: "button", Text: "发布", Exact: true}},
	Draft:             []plugin.ClickTarget{{Selector: "button", Text: "暂存离开", Exact: true}},
	Confirm:           []plugin.ClickTarget{{Selector: "dialog button", Text: "确认", Exact: true}},
	RedirectOnSuccess: true, DraftTexts: []string{"已保存"},
}

func video(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "v.mp4")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func formPage() *plugintest.Page {
	p := plugintest.New()
	p.SetElement("input[type=file]", plugin.ElementInfo{Exists: true, Visible: true})
	p.SetElement("#title", plugin.ElementInfo{Exists: true, Visible: true})
	p.SetElement("#body", plugin.ElementInfo{Exists: true, Visible: true})
	return p
}

func TestCreatorFlowPublishWaitsForEnabledButtonAndRedirect(t *testing.T) {
	p := formPage()
	disabled := 2 // the publish button is refused twice while "transcoding"
	p.OnClick = func(p *plugintest.Page, sel string, opt plugin.ClickOptions) error {
		if opt.Text != "发布" {
			return fmt.Errorf("not found")
		}
		if disabled > 0 {
			disabled--
			return fmt.Errorf("element is disabled")
		}
		p.URL = "https://creator.test/manage"
		return nil
	}
	page, _ := plugin.NewPage(p)
	out, err := plugin.RunCreatorFlow(context.Background(), page, flow, plugin.CreatorPost{Files: []string{video(t)}, Title: "标题", Body: "正文 #话题"})
	if err != nil {
		t.Fatal(err)
	}
	if out["status"] != "published" || p.Typed["#title"] != "标题" || p.Typed["#body"] != "正文 #话题" || len(p.Uploads) != 1 {
		t.Fatalf("out=%v typed=%v uploads=%v", out, p.Typed, p.Uploads)
	}
	if p.Keys[0] != "Escape" {
		t.Fatalf("suggestion popups should be dismissed after the body, keys=%v", p.Keys)
	}
}

func TestCreatorFlowDraftNeedsSpecificEvidence(t *testing.T) {
	p := formPage()
	p.Text = "草稿箱" // a menu link must not count as success
	clickedConfirm := false
	p.OnClick = func(p *plugintest.Page, sel string, opt plugin.ClickOptions) error {
		switch opt.Text {
		case "暂存离开":
			p.Text += " 已保存"
			return nil
		case "确认":
			clickedConfirm = true
		}
		return fmt.Errorf("not found")
	}
	page, _ := plugin.NewPage(p)
	out, err := plugin.RunCreatorFlow(context.Background(), page, flow, plugin.CreatorPost{Files: []string{video(t)}, Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if out["status"] != "draft_saved" || out["evidence"] != "已保存" {
		t.Fatalf("%v", out)
	}
	if clickedConfirm {
		t.Fatal("publish confirmation dialogs must never be clicked in draft mode")
	}
}

func TestCreatorFlowValidatesBeforeOpening(t *testing.T) {
	p := formPage()
	page, _ := plugin.NewPage(p)
	noDraft := flow
	noDraft.Draft = nil
	cases := map[string]plugin.CreatorPost{
		"title too long": {Files: []string{video(t)}, Title: "一二三四五六"},
		"missing file":   {Files: []string{"/nonexistent.mp4"}},
	}
	for name, post := range cases {
		if _, err := plugin.RunCreatorFlow(context.Background(), page, flow, post); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := plugin.RunCreatorFlow(context.Background(), page, noDraft, plugin.CreatorPost{Files: []string{video(t)}, Draft: true}); err == nil {
		t.Error("draft on a platform without drafts must fail")
	}
	if len(p.Opened) != 0 {
		t.Fatalf("validation failures must not open pages: %v", p.Opened)
	}
	if _, err := plugin.ParseMode(""); err == nil {
		t.Error("mode is required")
	}
}

func TestCreatorFlowDetectsLoginRedirect(t *testing.T) {
	p := formPage()
	p.OnClick = nil
	page, _ := plugin.NewPage(&loginRedirect{p})
	if _, err := plugin.RunCreatorFlow(context.Background(), page, flow, plugin.CreatorPost{Files: []string{video(t)}}); err == nil {
		t.Fatal("login redirect should stop the flow")
	}
}

type loginRedirect struct{ *plugintest.Page }

func (l *loginRedirect) Open(ctx context.Context, u string) error {
	_ = l.Page.Open(ctx, u)
	l.URL = "https://creator.test/login?next=upload"
	return nil
}
