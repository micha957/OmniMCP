package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugins/xiaohongshu/engine"
)

// Page selectors shared with the rod engine.
const (
	selLikeButton     = engine.SelectorLikeButton
	selCollectButton  = engine.SelectorCollectButton
	selCommentTrigger = "div.input-box div.content-edit span"
	selCommentInput   = "div.input-box div.content-edit p.content-input"
	selCommentSubmit  = "div.bottom button.submit"
	selComments       = ".comments-container"
	selPublishTitle   = "div.d-input input"
	selImagePreview   = ".img-preview-area .pr"
	publishURL        = "https://creator.xiaohongshu.com/publish/publish?source=official"
)

var contentSelectors = []string{`div[role="textbox"][contenteditable="true"]`, `div.tiptap[contenteditable="true"]`, `div.ql-editor`}

type extensionPage struct{ *plugin.Page }

func newExtensionPage(b plugin.Browser) (*extensionPage, error) {
	p, err := plugin.NewPage(b)
	if err != nil {
		return nil, err
	}
	return &extensionPage{p}, nil
}

func nonEmptyFeeds(raw json.RawMessage) bool {
	feeds, err := engine.ParseFeeds(raw)
	return err == nil && len(feeds) > 0
}

func noteURL(feedID, token string) string {
	return "https://www.xiaohongshu.com/explore/" + url.PathEscape(feedID) + "?xsec_token=" + url.QueryEscape(token) + "&xsec_source=pc_feed"
}

func (p *extensionPage) noteDetail(ctx context.Context, feedID string) (*engine.FeedDetailResponse, error) {
	raw, err := p.PollGlobal(ctx, "__INITIAL_STATE__.note.noteDetailMap", 10*time.Second, func(raw json.RawMessage) bool {
		_, err := engine.ParseNoteDetail(raw, feedID)
		return err == nil
	})
	if err != nil {
		if u, _ := p.CurrentURL(ctx); strings.Contains(u, "/404") || strings.Contains(u, "login") {
			return nil, fmt.Errorf("笔记不可访问（当前页面 %s），可能需要登录或 xsec_token 已失效", u)
		}
		return nil, fmt.Errorf("读取笔记详情失败: %w", err)
	}
	return engine.ParseNoteDetail(raw, feedID)
}

// executeExtension runs workflows inside the user's own Chrome. Data is read
// from the page's __INITIAL_STATE__ so results match the managed engine.
func (pl *Plugin) executeExtension(ctx context.Context, c *plugin.Context, name string, in input) (any, error) {
	p, err := newExtensionPage(c.Browser)
	if err != nil {
		return nil, err
	}
	switch name {
	case "check_login":
		if err := p.Open(ctx, "https://www.xiaohongshu.com/explore"); err != nil {
			return nil, err
		}
		raw, err := p.PollGlobal(ctx, "__INITIAL_STATE__.user.userInfo", 6*time.Second, func(raw json.RawMessage) bool { return string(raw) != "null" })
		if err != nil {
			return map[string]any{"logged_in": false}, nil
		}
		var u struct {
			Guest    bool   `json:"guest"`
			Nickname string `json:"nickname"`
			UserID   string `json:"userId"`
		}
		_ = json.Unmarshal(raw, &u)
		loggedIn := !u.Guest && u.UserID != ""
		if !loggedIn {
			return map[string]any{"logged_in": false}, nil
		}
		return map[string]any{"logged_in": true, "user": map[string]string{"nickname": u.Nickname, "userId": u.UserID}}, nil

	case "search_notes":
		if err := p.Open(ctx, "https://www.xiaohongshu.com/search_result?keyword="+url.QueryEscape(in.Keyword)+"&source=web_explore_feed"); err != nil {
			return nil, err
		}
		raw, err := p.PollGlobal(ctx, "__INITIAL_STATE__.search.feeds", 15*time.Second, nonEmptyFeeds)
		if err != nil {
			return nil, fmt.Errorf("读取搜索结果失败（可能未登录或触发验证）: %w", err)
		}
		feeds, _ := engine.ParseFeeds(raw)
		return map[string]any{"keyword": in.Keyword, "items": limitFeeds(feeds, in.Limit)}, nil

	case "list_feeds":
		if err := p.Open(ctx, "https://www.xiaohongshu.com/explore"); err != nil {
			return nil, err
		}
		raw, err := p.PollGlobal(ctx, "__INITIAL_STATE__.feed.feeds", 10*time.Second, nonEmptyFeeds)
		if err != nil {
			return nil, fmt.Errorf("读取推荐流失败: %w", err)
		}
		feeds, _ := engine.ParseFeeds(raw)
		return map[string]any{"items": limitFeeds(feeds, in.Limit)}, nil

	case "get_note_detail":
		if err := p.Open(ctx, noteURL(in.FeedID, in.XSecToken)); err != nil {
			return nil, err
		}
		detail, err := p.noteDetail(ctx, in.FeedID)
		if err != nil {
			return nil, err
		}
		if in.LoadComments {
			pl.loadMoreComments(ctx, p)
			// Scrolling appends to the same state entry.
			if more, err := p.noteDetail(ctx, in.FeedID); err == nil {
				detail = more
			}
		}
		return detail, nil

	case "get_user_profile":
		tab, _ := engine.ParseProfileTab(in.Tab)
		target := "https://www.xiaohongshu.com/user/profile/" + url.PathEscape(in.UserID) + "?xsec_token=" + url.QueryEscape(in.XSecToken) + "&xsec_source=pc_note"
		if tab != engine.TabNotes {
			target += "&tab=" + string(tab) + "&subTab=note"
		}
		if err := p.Open(ctx, target); err != nil {
			return nil, err
		}
		return p.readProfile(ctx, tab)

	case "like_note", "favorite_note":
		if err := p.Open(ctx, noteURL(in.FeedID, in.XSecToken)); err != nil {
			return nil, err
		}
		want, selector, pick := !in.Unlike, selLikeButton, func(i engine.InteractInfo) bool { return i.Liked }
		if name == "favorite_note" {
			want, selector, pick = !in.Unfavorite, selCollectButton, func(i engine.InteractInfo) bool { return i.Collected }
		}
		return p.toggle(ctx, in.FeedID, selector, want, pick)

	case "post_comment":
		return p.postComment(ctx, in)

	case "reply_comment":
		return p.replyComment(ctx, in)

	case "get_notifications":
		return p.notifications(ctx, in.Tab, in.Limit)

	case "get_unread_count":
		return p.unreadCount(ctx)

	case "get_my_account":
		return p.myAccount(ctx)

	case "publish_note", "publish_video":
		draft, _ := plugin.ParseMode(in.Mode)
		return p.publish(ctx, c, in, name == "publish_video", draft)
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

// probe returns a short prefix used to find user text on the page; platforms
// may render emoji or long text differently.
func probe(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > 15 {
		r = r[:15]
	}
	return string(r)
}

func (p *extensionPage) toggle(ctx context.Context, feedID, selector string, want bool, pick func(engine.InteractInfo) bool) (any, error) {
	state := func() (bool, error) {
		d, err := p.noteDetail(ctx, feedID)
		if err != nil {
			return false, err
		}
		return pick(d.Note.InteractInfo), nil
	}
	cur, err := state()
	if err != nil {
		return nil, err
	}
	if cur == want {
		return map[string]any{"success": true, "changed": false, "state": want}, nil
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if err := p.ClickElement(ctx, selector, plugin.ClickOptions{}); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if cur, err = state(); err == nil && cur == want {
				return map[string]any{"success": true, "changed": true, "state": want}, nil
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	return nil, fmt.Errorf("点击后状态未变为 %v", want)
}

func (pl *Plugin) loadMoreComments(ctx context.Context, p *extensionPage) {
	for i := 0; i < 8; i++ {
		if ctx.Err() != nil {
			return
		}
		if err := p.Scroll(ctx, ".comments-container .parent-comment:last-child"); err != nil {
			return
		}
		time.Sleep(1200 * time.Millisecond)
		if end, err := p.Query(ctx, ".comments-container .end-container"); err == nil && end.Exists {
			return
		}
	}
}

// publish fills the creator form for an image note or a video and then
// either publishes it or saves it as a draft ("暂存离开").
func (p *extensionPage) publish(ctx context.Context, c *plugin.Context, in input, video, draft bool) (any, error) {
	if err := p.Open(ctx, publishURL); err != nil {
		return nil, err
	}
	if u, _ := p.CurrentURL(ctx); strings.Contains(u, "login") {
		return nil, fmt.Errorf("创作者平台未登录，请先在 Chrome 中登录 creator.xiaohongshu.com")
	}
	if err := p.WaitFor(ctx, "div.upload-content", "", 15*time.Second); err != nil {
		return nil, err
	}
	tab := "上传图文"
	if video {
		tab = "上传视频"
	}
	if _, err := plugin.ClickFirst(ctx, p.Page, []plugin.ClickTarget{{Selector: "div.creator-tab", Text: tab, Exact: true}}, 15*time.Second); err != nil {
		return nil, fmt.Errorf("切换到「%s」失败: %w", tab, err)
	}
	if err := p.WaitFor(ctx, ".upload-input", "", 10*time.Second); err != nil {
		return nil, err
	}
	// Videos keep the submit buttons disabled until transcoding finishes.
	submitWait := 15 * time.Second
	if video {
		submitWait = 10 * time.Minute
		if err := p.Upload(ctx, ".upload-input", in.Video); err != nil {
			return nil, fmt.Errorf("上传视频失败: %w", err)
		}
	} else {
		for i, img := range in.Images {
			sel := ".upload-input"
			if i > 0 {
				sel = `input[type="file"][accept*="image"], input[type="file"]`
			}
			if err := p.Upload(ctx, sel, img); err != nil {
				return nil, fmt.Errorf("上传第 %d 张图片失败: %w", i+1, err)
			}
			if err := p.waitPreviews(ctx, i+1, 60*time.Second); err != nil {
				return nil, err
			}
		}
	}
	if err := p.WaitFor(ctx, selPublishTitle, "", 3*time.Minute); err != nil {
		return nil, fmt.Errorf("上传后没有出现标题输入框: %w", err)
	}
	if err := p.TypeText(ctx, selPublishTitle, in.Title); err != nil {
		return nil, fmt.Errorf("输入标题失败: %w", err)
	}
	body := p.FirstExisting(ctx, contentSelectors, 10*time.Second)
	if body == "" {
		return nil, fmt.Errorf("未找到正文输入框")
	}
	if err := p.TypeText(ctx, body, in.Content); err != nil {
		return nil, fmt.Errorf("输入正文失败: %w", err)
	}
	_ = p.PressKey(ctx, "Escape")
	time.Sleep(time.Second)

	if in.DeclareAI {
		if err := p.declareAI(ctx); err != nil {
			return nil, err
		}
	}
	// Whatever went wrong while filling, the form must match what was approved.
	if err := p.verifyForm(ctx, body, in); err != nil {
		return nil, err
	}

	formURL, _ := p.CurrentURL(ctx)
	status, texts := "published", []string{"发布成功"}
	if draft {
		status, texts = "draft_saved", []string{"暂存成功", "保存成功", "已保存到草稿箱"}
		// No coordinate fallback here: guessing inside the widget could hit
		// "发布" and publish what was meant to be a draft.
		if _, err := plugin.ClickFirst(ctx, p.Page, []plugin.ClickTarget{{Selector: "button", Text: "暂存离开", Exact: true}, {Selector: ".publish-page-publish-btn button", Text: "暂存离开"}}, submitWait); err != nil {
			return nil, fmt.Errorf("没有找到「暂存离开」按钮: %w", err)
		}
	} else if err := p.clickPublish(ctx, submitWait); err != nil {
		return nil, err
	}
	evidence, err := p.AwaitOutcome(ctx, formURL, true, texts, nil, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", plugin.ErrOutcomeUnknown, err)
	}
	out := map[string]any{"status": status, "title": in.Title, "evidence": evidence}
	if video {
		out["video"] = in.Video
	} else {
		out["image_count"] = len(in.Images)
	}
	if !draft {
		addNoteReceipt(ctx, c, out, in.Title)
	}
	return out, nil
}

// declareAI picks the platform's AI declaration. The platform limits undeclared
// AI content, so a declaration that cannot be set stops the publish.
func (p *extensionPage) declareAI(ctx context.Context) error {
	if _, err := plugin.ClickFirst(ctx, p.Page, []plugin.ClickTarget{{Selector: ".d-select-placeholder", Text: "添加内容类型声明", Exact: true}}, 5*time.Second); err != nil {
		return fmt.Errorf("设置 AI 声明失败，已中止发布: %w", err)
	}
	if _, err := plugin.ClickFirst(ctx, p.Page, []plugin.ClickTarget{{Selector: ".d-option", Text: "笔记含AI合成内容", Exact: true}}, 5*time.Second); err != nil {
		return fmt.Errorf("设置 AI 声明失败，已中止发布: %w", err)
	}
	return nil
}

func (p *extensionPage) verifyForm(ctx context.Context, bodySelector string, in input) error {
	title, err := p.Query(ctx, selPublishTitle)
	if err != nil {
		return fmt.Errorf("读回标题失败，已中止发布: %w", err)
	}
	if !engine.SameText(title.Text, in.Title) {
		return fmt.Errorf("表单标题与确认内容不一致，已中止发布：%q", title.Text)
	}
	body, err := p.Query(ctx, bodySelector)
	if err != nil {
		return fmt.Errorf("读回正文失败，已中止发布: %w", err)
	}
	if !engine.SameText(body.Text, in.Content) {
		return fmt.Errorf("表单正文与确认内容不一致，已中止发布，没有提交")
	}
	return nil
}

func (p *extensionPage) waitPreviews(ctx context.Context, n int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := p.Query(ctx, selImagePreview); err == nil && info.Count >= n {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("第 %d 张图片上传超时", n)
}

// clickPublish handles both the new <xhs-publish-btn> widget (publish button
// in a shadow root, right of centre) and the legacy red button.
func (p *extensionPage) clickPublish(ctx context.Context, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if w, err := p.Query(ctx, "xhs-publish-btn"); err == nil && w.Exists && w.Visible && w.Attrs["is-publish"] != "false" {
			if w.Attrs["submit-disabled"] != "true" {
				return p.ClickElement(ctx, "xhs-publish-btn", plugin.ClickOptions{XRatio: 0.65})
			}
		} else if b, err := p.Query(ctx, ".publish-page-publish-btn button.bg-red"); err == nil && b.Exists && b.Visible {
			if _, disabled := b.Attrs["disabled"]; !disabled {
				return p.ClickElement(ctx, ".publish-page-publish-btn button.bg-red", plugin.ClickOptions{})
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("发布按钮不可点击（图片可能仍在上传或内容未通过校验）")
}
