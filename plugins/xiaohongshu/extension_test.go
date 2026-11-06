package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
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

func TestPublishStopsWhenFormDiffersFromApproval(t *testing.T) {
	img := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(img, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	page := plugintest.New()
	page.URL = publishURL
	for _, sel := range []string{"div.upload-content", ".upload-input", selPublishTitle, contentSelectors[0]} {
		page.SetElement(sel, plugin.ElementInfo{Exists: true, Visible: true})
	}
	page.SetElement(selImagePreview, plugin.ElementInfo{Exists: true, Count: 1})
	// The real incident: the caret moved mid-way and the ending was scrambled.
	page.TypeMangle = func(v string) string { return strings.Replace(v, "#开发日记", "记发日开#", 1) }
	var clicked []string
	page.OnClick = func(_ *plugintest.Page, sel string, _ plugin.ClickOptions) error {
		clicked = append(clicked, sel)
		return nil
	}
	_, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "publish_note",
		json.RawMessage(fmt.Sprintf(`{"title":"标题","content":"正文 #开发日记","images":[%q],"mode":"publish"}`, img)))
	if err == nil || !strings.Contains(err.Error(), "已中止发布") {
		t.Fatalf("want refusal, got %v", err)
	}
	if errors.Is(err, plugin.ErrOutcomeUnknown) {
		t.Fatalf("nothing was submitted, so the outcome is known: %v", err)
	}
	for _, c := range clicked {
		if strings.Contains(c, "publish") {
			t.Fatalf("publish must not be clicked: %v", clicked)
		}
	}
}

func TestPublishStopsWhenAIDeclarationFails(t *testing.T) {
	img := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(img, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	page := plugintest.New()
	page.URL = publishURL
	for _, sel := range []string{"div.upload-content", ".upload-input", selPublishTitle, contentSelectors[0]} {
		page.SetElement(sel, plugin.ElementInfo{Exists: true, Visible: true})
	}
	page.SetElement(selImagePreview, plugin.ElementInfo{Exists: true, Count: 1})
	var clicked []string
	page.OnClick = func(_ *plugintest.Page, sel string, opt plugin.ClickOptions) error {
		clicked = append(clicked, sel)
		if opt.Text == "笔记含AI合成内容" {
			return fmt.Errorf("not found")
		}
		return nil
	}
	_, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "publish_note",
		json.RawMessage(fmt.Sprintf(`{"title":"标题","content":"正文","images":[%q],"mode":"publish","declare_ai":true}`, img)))
	if err == nil || !strings.Contains(err.Error(), "已中止发布") {
		t.Fatalf("want refusal, got %v", err)
	}
	for _, c := range clicked {
		if strings.Contains(c, "publish") {
			t.Fatalf("publish must not be clicked: %v", clicked)
		}
	}
}

func TestSameTextIgnoresEditorWhitespace(t *testing.T) {
	if !engine.SameText("第一段\n\n\n\n第二段 #拼豆", "第一段\n\t\n第二段 #拼豆") {
		t.Fatal("blank-line differences must not block publishing")
	}
	if engine.SameText("第一段", "第二段") {
		t.Fatal("different text must not match")
	}
}

func TestMyAccountMergesCreatorStats(t *testing.T) {
	page := plugintest.New()
	page.Globals["__INITIAL_STATE__.user.userInfo"] = `{"userId":"me","nickname":"我"}`
	page.Globals["__INITIAL_STATE__.user.userPageData"] = `{"interactions":[{"type":"fans","count":"1"}]}`
	page.Globals["__INITIAL_STATE__.user.notes"] = `[[{"id":"n1","xsecToken":"t","noteCard":{"displayTitle":"标题","interactInfo":{"likedCount":"2"}}}]]`
	page.Lists[selNoteCard] = []map[string]string{{"title": "标题 ", "views": "14", "comments": "3", "likes": "2", "collects": "4", "shares": "5"}}
	out, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "get_my_account", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	n := out.(map[string]any)["notes"].([]MyNote)[0]
	if n.Views != "14" || n.Comments != "3" || n.Collects != "4" || n.Shares != "5" || n.ID != "n1" {
		t.Fatalf("%+v", n)
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

func notePage(comments string) *plugintest.Page {
	page := plugintest.New()
	page.Globals["__INITIAL_STATE__.note.noteDetailMap"] = `{"n1":{"note":{"noteId":"n1","title":"t"},"comments":{"list":` + comments + `}}}`
	page.Globals["__INITIAL_STATE__.user.userInfo"] = `{"userId":"me","nickname":"我"}`
	page.SetElement(selCommentInput, plugin.ElementInfo{Exists: true, Visible: true})
	return page
}

func TestReplyTargetsCommentAndReturnsReceipt(t *testing.T) {
	page := notePage(`[{"id":"c1","content":"这个能用吗","userInfo":{"userId":"u1","nickname":"小A"}}]`)
	page.SetElement("#comment-c1", plugin.ElementInfo{Exists: true, Visible: true})
	page.OnClick = func(p *plugintest.Page, sel string, _ plugin.ClickOptions) error {
		if strings.HasSuffix(sel, ".reply") {
			p.SetElement(selEngageBar, plugin.ElementInfo{Exists: true, Text: "回复 小A\n这个能用吗"})
		}
		if sel == selCommentSubmit {
			p.Globals["__INITIAL_STATE__.note.noteDetailMap"] = `{"n1":{"note":{"noteId":"n1","title":"t"},"comments":{"list":[{"id":"c1","content":"这个能用吗","userInfo":{"userId":"u1","nickname":"小A"},"subComments":[{"id":"r9","content":"可以用","userInfo":{"userId":"me"}}]}]}}}`
		}
		return nil
	}
	out, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "reply_comment", json.RawMessage(`{"feed_id":"n1","xsec_token":"x","comment_id":"c1","content":"可以用"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["comment_id"] != "r9" || m["reply_to"] != "c1" {
		t.Fatalf("receipt: %v", m)
	}
	if strings.Join(page.Clicks, ",") != "#comment-c1 .right .interactions .reply,"+selCommentSubmit {
		t.Fatalf("clicked the wrong controls: %v", page.Clicks)
	}
}

func TestReplyStopsBeforeTypingWhenTargetIsWrong(t *testing.T) {
	page := notePage(`[{"id":"c1","content":"问题","userInfo":{"userId":"u1","nickname":"小A"}}]`)
	page.SetElement("#comment-c1", plugin.ElementInfo{Exists: true, Visible: true})
	page.SetElement(selEngageBar, plugin.ElementInfo{Exists: true, Text: "回复 小B\n别人的评论"})
	_, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "reply_comment", json.RawMessage(`{"feed_id":"n1","xsec_token":"x","comment_id":"c1","content":"你好"}`))
	if err == nil || !strings.Contains(err.Error(), "回复对象不符") {
		t.Fatalf("want target refusal, got %v", err)
	}
	if len(page.Typed) != 0 || strings.Contains(strings.Join(page.Clicks, ","), selCommentSubmit) {
		t.Fatalf("nothing may be typed or sent: %v %v", page.Typed, page.Clicks)
	}
}

func TestCommentWithoutConfirmationIsUnknown(t *testing.T) {
	page := notePage(`[]`)
	page.SetElement(selCommentTrigger, plugin.ElementInfo{Exists: true, Visible: true})
	_, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "post_comment", json.RawMessage(`{"feed_id":"n1","xsec_token":"x","content":"你好"}`))
	if !errors.Is(err, plugin.ErrOutcomeUnknown) {
		t.Fatalf("clicked send without proof must be unknown, got %v", err)
	}
}

func TestReplyRejectsUnsafeCommentID(t *testing.T) {
	page := notePage(`[]`)
	_, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "reply_comment", json.RawMessage(`{"feed_id":"n1","xsec_token":"x","comment_id":"a b, #x","content":"你好"}`))
	if err == nil || len(page.Opened) != 0 {
		t.Fatalf("id must be validated before opening a page: %v", err)
	}
}

func TestNotificationsReportCoverage(t *testing.T) {
	page := plugintest.New()
	page.Globals["__INITIAL_STATE__.notification.notificationMap.mentions"] = `{"hasMore":true,"messageList":[
		{"id":"m1","type":"comment/comment","title":"评论了你","time":1,"userInfo":{"userid":"u1","nickname":"小A"},"commentInfo":{"id":"c1","content":"怎么用","illegalInfo":{"illegalStatus":"NORMAL"}},"itemInfo":{"id":"n1","type":"note_info","content":"笔记","xsecToken":"tok"}},
		{"id":"m2","type":"comment/comment","commentInfo":{"id":"c2","illegalInfo":{"illegalStatus":"DELETED"}}}]}`
	out, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "get_notifications", json.RawMessage(`{"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	items := m["items"].([]engine.NotificationItem)
	if len(items) != 1 || items[0].CommentID != "c1" || items[0].FeedID != "n1" || items[0].FeedXsecToken != "tok" {
		t.Fatalf("items: %+v", items)
	}
	if m["complete"] != false || m["has_more"] != true {
		t.Fatalf("partial read must be reported: %v", m)
	}
}
