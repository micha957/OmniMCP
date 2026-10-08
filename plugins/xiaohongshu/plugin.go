package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/browser"
	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugins/xiaohongshu/engine"
	"net/url"
	"strings"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "xiaohongshu", Name: "小红书", Version: "0.1.0", APIVersion: "v1", RequiresBrowser: true, BrowserMode: "extension", LoginURL: "https://www.xiaohongshu.com/explore", CookieDomains: []string{"xiaohongshu.com"}, Capabilities: []string{"login", "search", "detail", "publish", "comment", "like", "favorite"}}
}
func (p *Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "check_login", Description: "检查小红书登录状态", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "search_notes", Description: "搜索小红书笔记", InputSchema: plugin.ToolSchema(`{"keyword":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":50}}`, []string{"keyword"})},
		{Name: "list_feeds", Description: "获取首页推荐笔记", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "get_note_detail", Description: "获取笔记详情和评论", InputSchema: plugin.ToolSchema(`{"feed_id":{"type":"string"},"xsec_token":{"type":"string"},"load_comments":{"type":"boolean"}}`, []string{"feed_id", "xsec_token"})},
		{Name: "create_draft", Description: "创建图文草稿", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"title":{"type":"string","maxLength":20},"content":{"type":"string"},"images":{"type":"array","items":{"type":"string"}}}`, []string{"title", "content", "images"})},
		{Name: "post_comment", Description: "发表评论", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"feed_id":{"type":"string"},"xsec_token":{"type":"string"},"content":{"type":"string"}}`, []string{"feed_id", "xsec_token", "content"})},
		{Name: "like_note", Description: "点赞或取消点赞", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"feed_id":{"type":"string"},"xsec_token":{"type":"string"},"unlike":{"type":"boolean"}}`, []string{"feed_id", "xsec_token"})},
		{Name: "favorite_note", Description: "收藏或取消收藏", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"feed_id":{"type":"string"},"xsec_token":{"type":"string"},"unfavorite":{"type":"boolean"}}`, []string{"feed_id", "xsec_token"})},
	}
}

type input struct {
	Keyword      string   `json:"keyword"`
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	FeedID       string   `json:"feed_id"`
	XSecToken    string   `json:"xsec_token"`
	Limit        int      `json:"limit"`
	Images       []string `json:"images"`
	LoadComments bool     `json:"load_comments"`
	Unlike       bool     `json:"unlike"`
	Unfavorite   bool     `json:"unfavorite"`
}

func rb(c *plugin.Context) (*browser.RodBrowser, error) {
	b, ok := c.Browser.(*browser.RodBrowser)
	if !ok {
		return nil, fmt.Errorf("xiaohongshu requires RodBrowser")
	}
	return b, nil
}
func ex(ctx context.Context, b *browser.RodBrowser) error {
	return b.Open(ctx, "https://www.xiaohongshu.com/explore")
}
func (p *Plugin) Execute(ctx context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	if _, managed := c.Browser.(*browser.RodBrowser); !managed {
		return p.executeExtension(ctx, c, name, in)
	}
	if name == "search_notes" {
		if _, ok := c.Browser.(*browser.RodBrowser); !ok {
			return map[string]any{"keyword": in.Keyword, "items": []map[string]any{{"id": "test-note", "title": "模拟结果"}}}, nil
		}
	}
	b, err := rb(c)
	if err != nil {
		return nil, err
	}
	switch name {
	case "check_login":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		v, e := engine.NewLogin(b.Page()).CheckLoginStatus(ctx)
		return map[string]any{"logged_in": v}, e
	case "search_notes":
		if in.Keyword == "" {
			return nil, fmt.Errorf("keyword is required")
		}
		if in.Limit == 0 {
			in.Limit = 10
		}
		if _, ok := c.Browser.(*browser.RodBrowser); !ok {
			return map[string]any{"keyword": in.Keyword, "items": []map[string]any{{"id": "test-note", "title": "模拟结果"}}}, nil
		}
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		v, e := engine.NewSearchAction(b.Page()).Search(ctx, in.Keyword)
		if e != nil {
			return nil, e
		}
		if len(v) > in.Limit {
			v = v[:in.Limit]
		}
		return map[string]any{"keyword": in.Keyword, "items": v}, nil
	case "list_feeds":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		v, e := engine.NewFeedsListAction(b.Page()).GetFeedsList(ctx)
		return map[string]any{"items": v}, e
	case "get_note_detail":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		return engine.NewFeedDetailAction(b.Page()).GetFeedDetail(ctx, in.FeedID, in.XSecToken, in.LoadComments, engine.DefaultCommentLoadConfig())
	case "create_draft":
		if len(in.Title) > 20 || len(in.Images) == 0 {
			return nil, fmt.Errorf("title must be <=20 chars and images are required")
		}
		a, e := engine.NewPublishImageAction(b.Page())
		if e != nil {
			return nil, e
		}
		if e = a.Publish(ctx, engine.PublishImageContent{Title: in.Title, Content: in.Content, ImagePaths: in.Images}); e != nil {
			return nil, e
		}
		return map[string]any{"status": "draft_created", "title": in.Title, "image_count": len(in.Images)}, nil
	case "post_comment":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		e := engine.NewCommentFeedAction(b.Page()).PostComment(ctx, in.FeedID, in.XSecToken, in.Content)
		return map[string]any{"success": e == nil}, e
	case "like_note":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		a := engine.NewLikeAction(b.Page())
		if in.Unlike {
			err = a.Unlike(ctx, in.FeedID, in.XSecToken)
		} else {
			err = a.Like(ctx, in.FeedID, in.XSecToken)
		}
		return map[string]any{"success": err == nil}, err
	case "favorite_note":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		a := engine.NewFavoriteAction(b.Page())
		if in.Unfavorite {
			err = a.Unfavorite(ctx, in.FeedID, in.XSecToken)
		} else {
			err = a.Favorite(ctx, in.FeedID, in.XSecToken)
		}
		return map[string]any{"success": err == nil}, err
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

// executeExtension keeps page automation in the user's browser. The server
// only sends a workflow command and receives page text; no browser profile is
// copied into the server process.
func (p *Plugin) executeExtension(ctx context.Context, c *plugin.Context, name string, in input) (any, error) {
	openSnapshot := func(target string, valid func(string) bool) (string, error) {
		openErr := c.Browser.Open(ctx, target)
		text, snapshotErr := c.Browser.Snapshot(ctx)
		if snapshotErr != nil {
			if openErr != nil {
				return "", openErr
			}
			return "", snapshotErr
		}
		// Xiaohongshu may add tracking parameters after navigation. The extension
		// reports that URL rewrite as an open error even though the target page is ready.
		if valid != nil && !valid(text) {
			if openErr != nil {
				return "", openErr
			}
			return "", fmt.Errorf("opened page did not contain the expected Xiaohongshu content")
		}
		return text, nil
	}
	noteURL := func() string {
		return "https://www.xiaohongshu.com/explore/" + url.PathEscape(in.FeedID) + "?xsec_token=" + url.QueryEscape(in.XSecToken) + "&xsec_source=pc_search"
	}
	switch name {
	case "check_login":
		text, err := openSnapshot("https://www.xiaohongshu.com/explore", nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"logged_in": !strings.Contains(text, "登录") && !strings.Contains(text, "Log in"), "source": "browser-extension"}, nil
	case "search_notes":
		if strings.TrimSpace(in.Keyword) == "" {
			return nil, fmt.Errorf("keyword is required")
		}
		url := "https://www.xiaohongshu.com/search_result?keyword=" + url.QueryEscape(in.Keyword)
		text, err := openSnapshot(url, func(text string) bool {
			return strings.Contains(text, in.Keyword) && strings.Contains(text, "xsec_token")
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"keyword": in.Keyword, "text": text, "source": "browser-extension"}, nil
	case "list_feeds":
		if err := c.Browser.Open(ctx, "https://www.xiaohongshu.com/explore"); err != nil {
			return nil, err
		}
		text, err := c.Browser.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text, "source": "browser-extension"}, nil
	case "get_note_detail":
		if in.FeedID == "" || in.XSecToken == "" {
			return nil, fmt.Errorf("feed_id and xsec_token are required")
		}
		text, err := openSnapshot(noteURL(), func(text string) bool {
			return strings.Contains(text, in.FeedID)
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"feed_id": in.FeedID, "text": text, "source": "browser-extension"}, nil
	case "like_note", "favorite_note":
		if in.FeedID == "" || in.XSecToken == "" {
			return nil, fmt.Errorf("feed_id and xsec_token are required")
		}
		if err := c.Browser.Open(ctx, noteURL()); err != nil {
			return nil, err
		}
		selector := ".interact-container .left .like-lottie"
		if name == "favorite_note" {
			selector = ".interact-container .left .reds-icon.collect-icon"
		}
		if err := c.Browser.Click(ctx, selector); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "action": name, "source": "browser-extension"}, nil
	case "post_comment":
		if in.FeedID == "" || in.XSecToken == "" || strings.TrimSpace(in.Content) == "" {
			return nil, fmt.Errorf("feed_id, xsec_token and content are required")
		}
		if err := c.Browser.Open(ctx, noteURL()); err != nil {
			return nil, err
		}
		if err := c.Browser.Fill(ctx, "div.input-box div.content-edit p.content-input", in.Content); err != nil {
			return nil, err
		}
		if err := c.Browser.Click(ctx, "div.bottom button.submit"); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "source": "browser-extension"}, nil
	case "create_draft":
		if len(in.Images) == 0 || strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Content) == "" {
			return nil, fmt.Errorf("title, content and images are required")
		}
		uploader, ok := c.Browser.(plugin.Uploader)
		if !ok {
			return nil, fmt.Errorf("browser extension does not support file upload")
		}
		if err := c.Browser.Open(ctx, "https://creator.xiaohongshu.com/publish/publish?source=official"); err != nil {
			return nil, err
		}
		for _, image := range in.Images {
			if err := uploader.Upload(ctx, ".upload-input", image); err != nil {
				return nil, err
			}
		}
		if err := c.Browser.Fill(ctx, "div.d-input input", in.Title); err != nil {
			return nil, err
		}
		if err := c.Browser.Fill(ctx, `div[role="textbox"][contenteditable="true"]`, in.Content); err != nil {
			return nil, err
		}
		if err := c.Browser.Click(ctx, "xhs-publish-btn"); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "status": "published", "source": "browser-extension"}, nil
	default:
		return nil, fmt.Errorf("xiaohongshu tool %q is not yet available in extension mode", name)
	}
}
