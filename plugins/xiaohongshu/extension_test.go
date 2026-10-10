package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugin/plugintest"
	"github.com/heybox/omnimcp/plugins/xiaohongshu/engine"
)

// fakePage mimics a note page whose like state lives in __INITIAL_STATE__.
type fakePage struct {
	*plugin.MemoryBrowser
	liked  bool
	clicks int
}

func (f *fakePage) state() string {
	return fmt.Sprintf(`{"n1":{"note":{"noteId":"n1","title":"t","interactInfo":{"liked":%v}},"comments":{"list":[]}}}`, f.liked)
}
func (f *fakePage) ReadGlobal(_ context.Context, path string) (json.RawMessage, error) {
	if path == "__INITIAL_STATE__.note.noteDetailMap" {
		return json.RawMessage(f.state()), nil
	}
	return nil, fmt.Errorf("missing %s", path)
}
func (f *fakePage) ClickElement(_ context.Context, sel string, _ plugin.ClickOptions) error {
	if sel == selLikeButton {
		f.clicks++
		f.liked = !f.liked
	}
	return nil
}
func (f *fakePage) Query(context.Context, string) (plugin.ElementInfo, error) {
	return plugin.ElementInfo{}, nil
}
func (f *fakePage) WaitFor(context.Context, string, string, time.Duration) error { return nil }
func (f *fakePage) Scroll(context.Context, string) error                         { return nil }
func (f *fakePage) CurrentURL(context.Context) (string, error)                   { return f.URL, nil }
func (f *fakePage) TypeText(context.Context, string, string) error               { return nil }
func (f *fakePage) PressKey(context.Context, string) error                       { return nil }
func (f *fakePage) Upload(context.Context, string, string) error                 { return nil }

func runTool(t *testing.T, b plugin.Browser, name, args string) (any, error) {
	t.Helper()
	return New().Execute(context.Background(), &plugin.Context{Browser: b}, name, json.RawMessage(args))
}

func TestExtensionLikeVerifiesState(t *testing.T) {
	page := &fakePage{MemoryBrowser: plugin.NewMemoryBrowser()}
	out, err := runTool(t, page, "like_note", `{"feed_id":"n1","xsec_token":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if m := out.(map[string]any); m["changed"] != true || !page.liked || page.clicks != 1 {
		t.Fatalf("like not applied once: %v clicks=%d", m, page.clicks)
	}
	// Already liked: no second click, reported as unchanged.
	out, err = runTool(t, page, "like_note", `{"feed_id":"n1","xsec_token":"x"}`)
	if err != nil || out.(map[string]any)["changed"] != false || page.clicks != 1 {
		t.Fatalf("idempotent like failed: %v %v clicks=%d", out, err, page.clicks)
	}
	if !strings.Contains(page.URL, "/explore/n1") {
		t.Fatalf("note page not opened: %s", page.URL)
	}
}

func TestPublishValidatesTitleInCharacters(t *testing.T) {
	in := input{Title: "一二三四五六七八九十一二三四五六七八九十", Content: "c", Images: []string{"/nonexistent.png"}, Mode: "publish"}
	if err := in.validate("publish_note"); err == nil || !strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("20 CJK characters must pass the length check, got %v", err)
	}
	in.Title += "多"
	if err := in.validate("publish_note"); err == nil || !strings.Contains(err.Error(), "<=20") {
		t.Fatalf("21 characters must be rejected, got %v", err)
	}
}

func TestVideoDraftClicksSaveNeverPublish(t *testing.T) {
	video := filepath.Join(t.TempDir(), "v.mp4")
	if err := os.WriteFile(video, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	page := plugintest.New()
	page.URL = publishURL
	for _, sel := range []string{"div.upload-content", ".upload-input", selPublishTitle, contentSelectors[0]} {
		page.SetElement(sel, plugin.ElementInfo{Exists: true, Visible: true})
	}
	var clicked []string
	page.OnClick = func(p *plugintest.Page, sel string, opt plugin.ClickOptions) error {
		clicked = append(clicked, opt.Text)
		switch opt.Text {
		case "上传视频":
			return nil
		case "暂存离开":
			p.URL = "https://creator.xiaohongshu.com/new/note-manager"
			return nil
		}
		return fmt.Errorf("not found")
	}
	out, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "publish_video",
		json.RawMessage(fmt.Sprintf(`{"title":"标题","content":"正文","video":%q,"mode":"draft"}`, video)))
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["status"] != "draft_saved" || page.Uploads[0] != video {
		t.Fatalf("out=%v uploads=%v", out, page.Uploads)
	}
	for _, c := range clicked {
		if c == "发布" || strings.Contains(strings.Join(page.Clicks, ","), "xhs-publish-btn") {
			t.Fatalf("draft mode touched the publish button: %v %v", clicked, page.Clicks)
		}
	}
}

func TestParseFeedsAcceptsRefAndArray(t *testing.T) {
	for _, raw := range []string{
		`[{"id":"a","modelType":"note"},{"id":"q","modelType":"hot_query"}]`,
		`{"_value":[{"id":"a","modelType":"note"}]}`,
	} {
		feeds, err := engine.ParseFeeds([]byte(raw))
		if err != nil || len(feeds) != 1 || feeds[0].ID != "a" {
			t.Fatalf("%s: %v %v", raw, feeds, err)
		}
	}
}

func (f *fakePage) ExtractList(context.Context, plugin.ExtractSpec) ([]map[string]string, error) {
	return nil, nil
}
func (f *fakePage) ReadScriptJSON(context.Context, string) (json.RawMessage, error) { return nil, nil }
