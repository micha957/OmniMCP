package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/browser"
	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugins/xiaohongshu/engine"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "xiaohongshu", Name: "小红书", Version: "0.1.0", APIVersion: "v1", RequiresBrowser: true, BrowserMode: "extension", LoginURL: "https://www.xiaohongshu.com/explore", CookieDomains: []string{"xiaohongshu.com"}, Capabilities: []string{"login", "search", "detail", "publish", "comment", "like", "favorite"}}
}
func (p *Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "check_login", Description: "检查小红书登录状态，返回当前登录用户", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "search_notes", Description: "搜索小红书笔记，返回笔记 ID、xsec_token、标题、作者和互动数据", InputSchema: plugin.ToolSchema(`{"keyword":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":50}}`, []string{"keyword"})},
		{Name: "list_feeds", Description: "获取首页推荐笔记", InputSchema: plugin.ToolSchema(`{"limit":{"type":"integer","minimum":1,"maximum":50}}`, nil)},
		{Name: "get_note_detail", Description: "获取笔记详情（正文、图片、互动数据）和首屏评论；load_comments=true 时继续滚动加载更多评论", Timeout: 3 * time.Minute, InputSchema: plugin.ToolSchema(`{"feed_id":{"type":"string"},"xsec_token":{"type":"string"},"load_comments":{"type":"boolean"}}`, []string{"feed_id", "xsec_token"})},
		{Name: "get_user_profile", Description: "获取用户主页信息和笔记列表；tab 可选 note/fav/liked", InputSchema: plugin.ToolSchema(`{"user_id":{"type":"string"},"xsec_token":{"type":"string"},"tab":{"type":"string","enum":["note","fav","liked"]}}`, []string{"user_id", "xsec_token"})},
		{Name: "publish_note", Description: "发布图文笔记：mode=publish 直接公开发布，mode=draft 暂存到草稿箱（仅扩展模式）。images 为本机图片绝对路径", Mutating: true, Confirm: true, Timeout: 5 * time.Minute, InputSchema: plugin.ToolSchema(`{"title":{"type":"string","maxLength":20},"content":{"type":"string","maxLength":1000},"images":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":18},"tags":{"type":"array","items":{"type":"string"},"description":"话题标签，仅 managed 模式支持"},"mode":{"type":"string","enum":["publish","draft"]}}`, []string{"title", "content", "images", "mode"})},
		{Name: "publish_video", Description: "发布视频笔记：mode=publish 直接公开发布，mode=draft 暂存到草稿箱（仅扩展模式）。video 为本机视频绝对路径", Mutating: true, Confirm: true, Timeout: 20 * time.Minute, InputSchema: plugin.ToolSchema(`{"title":{"type":"string","maxLength":20},"content":{"type":"string","maxLength":1000},"video":{"type":"string"},"tags":{"type":"array","items":{"type":"string"},"description":"话题标签，仅 managed 模式支持"},"mode":{"type":"string","enum":["publish","draft"]}}`, []string{"title", "content", "video", "mode"})},
		{Name: "post_comment", Description: "发表评论，并在评论区确认评论已出现", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"feed_id":{"type":"string"},"xsec_token":{"type":"string"},"content":{"type":"string"}}`, []string{"feed_id", "xsec_token", "content"})},
		{Name: "like_note", Description: "点赞或取消点赞（unlike=true），以页面状态确认结果", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"feed_id":{"type":"string"},"xsec_token":{"type":"string"},"unlike":{"type":"boolean"}}`, []string{"feed_id", "xsec_token"})},
		{Name: "favorite_note", Description: "收藏或取消收藏（unfavorite=true），以页面状态确认结果", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"feed_id":{"type":"string"},"xsec_token":{"type":"string"},"unfavorite":{"type":"boolean"}}`, []string{"feed_id", "xsec_token"})},
	}
}

type input struct {
	Keyword      string   `json:"keyword"`
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	FeedID       string   `json:"feed_id"`
	XSecToken    string   `json:"xsec_token"`
	UserID       string   `json:"user_id"`
	Tab          string   `json:"tab"`
	Limit        int      `json:"limit"`
	Images       []string `json:"images"`
	Video        string   `json:"video"`
	Mode         string   `json:"mode"`
	Tags         []string `json:"tags"`
	LoadComments bool     `json:"load_comments"`
	Unlike       bool     `json:"unlike"`
	Unfavorite   bool     `json:"unfavorite"`
}

// validate checks arguments before any page is opened, for both connectors.
func (in *input) validate(name string) error {
	needNote := func() error {
		if in.FeedID == "" || in.XSecToken == "" {
			return fmt.Errorf("feed_id and xsec_token are required")
		}
		return nil
	}
	switch name {
	case "search_notes":
		if strings.TrimSpace(in.Keyword) == "" {
			return fmt.Errorf("keyword is required")
		}
	case "get_note_detail", "like_note", "favorite_note":
		return needNote()
	case "post_comment":
		if strings.TrimSpace(in.Content) == "" {
			return fmt.Errorf("content is required")
		}
		return needNote()
	case "get_user_profile":
		if in.UserID == "" || in.XSecToken == "" {
			return fmt.Errorf("user_id and xsec_token are required")
		}
		if _, err := engine.ParseProfileTab(in.Tab); err != nil {
			return err
		}
	case "publish_note", "publish_video":
		if _, err := plugin.ParseMode(in.Mode); err != nil {
			return err
		}
		files := in.Images
		if name == "publish_video" {
			files = nil
			if in.Video != "" {
				files = []string{in.Video}
			}
		}
		if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Content) == "" || len(files) == 0 {
			return fmt.Errorf("title, content and %s are required", map[bool]string{true: "video", false: "images"}[name == "publish_video"])
		}
		if n := utf8.RuneCountInString(in.Title); n > 20 {
			return fmt.Errorf("title must be <=20 characters, got %d", n)
		}
		for _, f := range files {
			if _, err := os.Stat(f); err != nil {
				return fmt.Errorf("file %s: %w", f, err)
			}
		}
	}
	if in.Limit < 0 || in.Limit > 50 {
		return fmt.Errorf("limit must be 1..50")
	}
	if in.Limit == 0 {
		in.Limit = 10
	}
	return nil
}

func ex(ctx context.Context, b *browser.RodBrowser) error {
	return b.Open(ctx, "https://www.xiaohongshu.com/explore")
}
func limitFeeds(v []engine.Feed, n int) []engine.Feed {
	if len(v) > n {
		return v[:n]
	}
	return v
}
func (p *Plugin) Execute(ctx context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	if err := in.validate(name); err != nil {
		return nil, err
	}
	b, managed := c.Browser.(*browser.RodBrowser)
	if !managed {
		return p.executeExtension(ctx, c, name, in)
	}
	var err error
	switch name {
	case "check_login":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		a := engine.NewLogin(b.Page())
		v, e := a.CheckLoginStatus(ctx)
		if e != nil || !v {
			return map[string]any{"logged_in": v}, e
		}
		u, _ := a.CurrentUser(ctx)
		return map[string]any{"logged_in": true, "user": u}, nil
	case "search_notes":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		v, e := engine.NewSearchAction(b.Page()).Search(ctx, in.Keyword)
		if e != nil {
			return nil, e
		}
		return map[string]any{"keyword": in.Keyword, "items": limitFeeds(v, in.Limit)}, nil
	case "list_feeds":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		v, e := engine.NewFeedsListAction(b.Page()).GetFeedsList(ctx)
		return map[string]any{"items": limitFeeds(v, in.Limit)}, e
	case "get_note_detail":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		return engine.NewFeedDetailAction(b.Page()).GetFeedDetail(ctx, in.FeedID, in.XSecToken, in.LoadComments, engine.DefaultCommentLoadConfig())
	case "get_user_profile":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		tab, _ := engine.ParseProfileTab(in.Tab)
		return engine.NewUserProfileAction(b.Page()).UserProfile(ctx, in.UserID, in.XSecToken, tab)
	case "publish_note", "publish_video":
		if in.Mode == plugin.ModeDraft {
			return nil, fmt.Errorf("managed 模式暂不支持存草稿，请改用扩展模式（OMNIMCP_BROWSER_MODE=extension）")
		}
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		if name == "publish_video" {
			a, e := engine.NewPublishVideoAction(b.Page())
			if e != nil {
				return nil, e
			}
			if e = a.PublishVideo(ctx, engine.PublishVideoContent{Title: in.Title, Content: in.Content, Tags: in.Tags, VideoPath: in.Video}); e != nil {
				return nil, e
			}
			return map[string]any{"status": "published", "title": in.Title, "video": in.Video}, nil
		}
		a, e := engine.NewPublishImageAction(b.Page())
		if e != nil {
			return nil, e
		}
		if e = a.Publish(ctx, engine.PublishImageContent{Title: in.Title, Content: in.Content, Tags: in.Tags, ImagePaths: in.Images}); e != nil {
			return nil, e
		}
		return map[string]any{"status": "published", "title": in.Title, "image_count": len(in.Images)}, nil
	case "post_comment":
		if err = ex(ctx, b); err != nil {
			return nil, err
		}
		if err = engine.NewCommentFeedAction(b.Page()).PostComment(ctx, in.FeedID, in.XSecToken, in.Content); err != nil {
			return nil, err
		}
		return map[string]any{"success": true}, nil
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
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "liked": !in.Unlike}, nil
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
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "collected": !in.Unfavorite}, nil
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}
