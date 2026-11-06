package tiktok

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/heybox/omnimcp/plugin"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "tiktok", Name: "TikTok", Version: "0.1.0", APIVersion: "v1", RequiresBrowser: true, BrowserMode: "extension",
		LoginURL: "https://www.tiktok.com/login", CookieDomains: []string{"tiktok.com"},
		Capabilities: []string{"login", "search", "video", "comments", "user", "like", "comment", "publish"}}
}

func (*Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "check_login", Description: "检查 TikTok 是否已登录，返回当前用户", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "search_videos", Description: "搜索 TikTok 视频，返回链接、作者、文案和播放量", Timeout: 3 * time.Minute, InputSchema: plugin.ToolSchema(`{"keyword":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":100}}`, []string{"keyword"})},
		{Name: "get_video", Description: "获取视频详情：文案、作者、时长、播放/点赞/评论/分享/收藏数、音乐、话题", InputSchema: plugin.ToolSchema(`{"url":{"type":"string","description":"视频链接（含短链）"}}`, []string{"url"})},
		{Name: "get_comments", Description: "读取视频一级评论，滚动加载到 limit 条", Timeout: 3 * time.Minute, InputSchema: plugin.ToolSchema(`{"url":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":500}}`, []string{"url"})},
		{Name: "get_user", Description: "获取用户资料（粉丝、获赞、作品数）和作品列表；user 可为用户名、@用户名或主页链接", Timeout: 3 * time.Minute, InputSchema: plugin.ToolSchema(`{"user":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":300,"description":"作品数量，默认 30"}}`, []string{"user"})},
		{Name: "like_video", Description: "点赞或取消点赞（unlike=true），以按钮状态确认结果", Mutating: true, InputSchema: plugin.ToolSchema(`{"url":{"type":"string"},"unlike":{"type":"boolean"}}`, []string{"url"})},
		{Name: "post_comment", Description: "在视频下发表评论，并确认评论出现", Mutating: true, InputSchema: plugin.ToolSchema(`{"url":{"type":"string"},"content":{"type":"string","maxLength":150}}`, []string{"url", "content"})},
		{Name: "publish_video", Description: "通过 TikTok Studio 上传视频：mode=publish 直接发布（公开范围用账号默认设置），mode=draft 存为草稿。file_path 为本机视频绝对路径", Mutating: true, Timeout: 20 * time.Minute, InputSchema: plugin.ToolSchema(`{"file_path":{"type":"string"},"caption":{"type":"string","maxLength":2200},"mode":{"type":"string","enum":["publish","draft"]}}`, []string{"file_path", "caption", "mode"})},
	}
}

type input struct {
	Keyword  string `json:"keyword"`
	URL      string `json:"url"`
	User     string `json:"user"`
	Limit    int    `json:"limit"`
	Unlike   bool   `json:"unlike"`
	Content  string `json:"content"`
	FilePath string `json:"file_path"`
	Caption  string `json:"caption"`
	Mode     string `json:"mode"`
}

const (
	selData        = "#__UNIVERSAL_DATA_FOR_REHYDRATION__"
	selLike        = `button:has([data-e2e="like-icon"]), button:has([data-e2e="browse-like-icon"])`
	selCommentText = `[data-e2e="comment-level-1"]`
	selCommentUser = `[data-e2e="comment-username-1"]`
)

var commentFlow = plugin.CommentFlow{
	Openers: []plugin.ClickTarget{{Selector: `[data-e2e="comment-icon"]`}, {Selector: `[data-e2e="browse-comment-icon"]`}},
	Inputs:  []string{`[data-e2e="comment-input"] [contenteditable="true"]`, `.public-DraftEditor-content[contenteditable="true"]`, `div[contenteditable="true"][role="textbox"]`},
	Submits: []plugin.ClickTarget{{Selector: `[data-e2e="comment-post"]`}},
	Verify:  15 * time.Second,
}

// Search result cards changed markup several times; the anchor fallback keeps
// working because every card links to /@user/video/<id> and its cover's alt
// text carries the caption.
var searchSpecs = []plugin.ExtractSpec{
	{Item: `[data-e2e="search_video-item"], [data-e2e="search_top-item"]`, Fields: map[string]plugin.Field{
		"url":     {Selector: `a[href*="/video/"]`, Attr: "href"},
		"desc":    {Selector: `[data-e2e="search-card-video-caption"], [data-e2e="search-card-desc"]`},
		"author":  {Selector: `[data-e2e="search-card-user-unique-id"]`},
		"views":   {Selector: `[data-e2e="video-views"]`},
		"alt":     {Selector: "img", Attr: "alt"},
		"cover":   {Selector: "img", Attr: "src"},
		"created": {Selector: `[class*="DivTimeTag"], [class*="TimeTag"]`},
	}},
	{Item: `a[href*="/video/"]`, Fields: map[string]plugin.Field{
		"url":   {Attr: "href"},
		"alt":   {Selector: "img", Attr: "alt"},
		"cover": {Selector: "img", Attr: "src"},
		"views": {Selector: `[data-e2e="video-views"]`},
	}},
}

var userPostSpec = plugin.ExtractSpec{Item: `[data-e2e="user-post-item"]`, Fields: map[string]plugin.Field{
	"url":    {Selector: `a[href*="/video/"], a[href*="/photo/"]`, Attr: "href"},
	"views":  {Selector: `[data-e2e="video-views"]`},
	"alt":    {Selector: "img", Attr: "alt"},
	"cover":  {Selector: "img", Attr: "src"},
	"pinned": {Selector: `[data-e2e="video-card-badge"]`},
}}

func (p *Plugin) Execute(ctx context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	page, err := plugin.NewPage(c.Browser)
	if err != nil {
		return nil, err
	}
	switch name {
	case "check_login":
		if err := page.Open(ctx, "https://www.tiktok.com/"); err != nil {
			return nil, err
		}
		raw, err := readData(ctx, page)
		if err != nil {
			return nil, err
		}
		user, nick, ok, err := ParseLogin(raw)
		if err != nil || !ok {
			return map[string]any{"logged_in": false}, err
		}
		return map[string]any{"logged_in": true, "user": map[string]string{"unique_id": user, "nickname": nick}}, nil

	case "search_videos":
		if strings.TrimSpace(in.Keyword) == "" {
			return nil, fmt.Errorf("keyword is required")
		}
		if err := page.Open(ctx, "https://www.tiktok.com/search/video?q="+url.QueryEscape(in.Keyword)); err != nil {
			return nil, err
		}
		limit := clamp(in.Limit, 20, 100)
		videos, err := extractCards(ctx, page, searchSpecs, limit)
		if err != nil {
			return nil, fmt.Errorf("读取搜索结果失败（可能需要登录或触发了验证码）: %w", err)
		}
		return map[string]any{"keyword": in.Keyword, "items": videos}, nil

	case "get_video":
		v, err := openVideo(ctx, page, in.URL)
		if err != nil {
			return nil, err
		}
		return v, nil

	case "get_comments":
		v, err := openVideo(ctx, page, in.URL)
		if err != nil {
			return nil, err
		}
		limit := clamp(in.Limit, 20, 500)
		items, err := loadComments(ctx, page, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"video_id": v.ID, "total_comments": v.Comments, "count": len(items), "items": items}, nil

	case "get_user":
		user, err := NormalizeUser(in.User)
		if err != nil {
			return nil, err
		}
		if err := page.Open(ctx, "https://www.tiktok.com/@"+url.PathEscape(user)); err != nil {
			return nil, err
		}
		raw, err := readData(ctx, page)
		if err != nil {
			return nil, err
		}
		profile, err := ParseProfile(raw)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"user": profile}
		if limit := clamp(in.Limit, 30, 300); profile.Videos > 0 {
			videos, err := extractCards(ctx, page, []plugin.ExtractSpec{userPostSpec}, limit)
			if err != nil {
				out["videos_error"] = err.Error()
			}
			out["videos"] = videos
		}
		return out, nil

	case "like_video":
		if _, err := openVideo(ctx, page, in.URL); err != nil {
			return nil, err
		}
		return toggleLike(ctx, page, !in.Unlike)

	case "post_comment":
		if strings.TrimSpace(in.Content) == "" {
			return nil, fmt.Errorf("content is required")
		}
		v, err := openVideo(ctx, page, in.URL)
		if err != nil {
			return nil, err
		}
		if err := plugin.PostComment(ctx, page, commentFlow, in.Content); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "verified": true, "video_id": v.ID}, nil

	case "publish_video":
		return publish(ctx, page, in)
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

func clamp(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func readData(ctx context.Context, page *plugin.Page) (json.RawMessage, error) {
	var raw json.RawMessage
	err := plugin.Poll(ctx, 10*time.Second, 400*time.Millisecond, func() (bool, error) {
		v, err := page.ReadScriptJSON(ctx, selData)
		raw = v
		return err == nil, err
	})
	if err != nil {
		if u, _ := page.CurrentURL(ctx); u != "" {
			return nil, fmt.Errorf("页面数据未加载（%s），可能触发了验证码: %w", u, err)
		}
		return nil, err
	}
	return raw, nil
}

// openVideo navigates to a post (resolving short links) and returns its data.
func openVideo(ctx context.Context, page *plugin.Page, raw string) (Video, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), "tiktok.com") {
		return Video{}, fmt.Errorf("url 必须是 https 的 TikTok 视频链接")
	}
	if err := page.Open(ctx, u.String()); err != nil {
		return Video{}, err
	}
	data, err := readData(ctx, page)
	if err != nil {
		return Video{}, err
	}
	v, err := ParseVideoDetail(data)
	if err != nil {
		return Video{}, err
	}
	if _, id, ok := ParseVideoURL(u.String()); ok && id != v.ID {
		return Video{}, fmt.Errorf("打开的页面不是视频 %s（得到 %s）", id, v.ID)
	}
	return v, nil
}

// extractCards tries each spec until one yields video links, then scrolls to
// load more and normalises the rows.
func extractCards(ctx context.Context, page *plugin.Page, specs []plugin.ExtractSpec, limit int) ([]Video, error) {
	var spec plugin.ExtractSpec
	err := plugin.Poll(ctx, 15*time.Second, 600*time.Millisecond, func() (bool, error) {
		for _, s := range specs {
			rows, err := page.ExtractList(ctx, plugin.ExtractSpec{Item: s.Item, Fields: s.Fields, Limit: 5})
			if err != nil {
				return false, err
			}
			for _, r := range rows {
				if _, _, ok := ParseVideoURL(r["url"]); ok {
					spec = s
					return true, nil
				}
			}
		}
		return false, fmt.Errorf("页面上没有找到视频卡片")
	})
	if err != nil {
		return nil, err
	}
	rows, err := page.ExtractUntil(ctx, spec, limit, spec.Item+":last-of-type", limit/8+5)
	if err != nil {
		return nil, err
	}
	var out []Video
	seen := map[string]bool{}
	for _, r := range rows {
		user, id, ok := ParseVideoURL(r["url"])
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		desc := r["desc"]
		if desc == "" {
			desc = r["alt"]
		}
		out = append(out, Video{ID: id, URL: VideoURL(user, id), Desc: desc, Author: user, Cover: r["cover"], ViewsText: strings.TrimSpace(r["views"])})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func loadComments(ctx context.Context, page *plugin.Page, limit int) ([]map[string]string, error) {
	appeared := func() bool {
		info, err := page.Query(ctx, selCommentText)
		return err == nil && info.Exists
	}
	if plugin.Poll(ctx, 6*time.Second, 500*time.Millisecond, func() (bool, error) { return appeared(), nil }) != nil {
		// Comments live behind the comment icon on some layouts.
		for _, t := range commentFlow.Openers {
			if page.ClickElement(ctx, t.Selector, plugin.ClickOptions{}) == nil {
				break
			}
		}
		if err := plugin.Poll(ctx, 10*time.Second, 500*time.Millisecond, func() (bool, error) { return appeared(), nil }); err != nil {
			if empty, _ := page.Query(ctx, `[data-e2e="comment-empty"], [class*="NoComment"]`); empty.Exists {
				return []map[string]string{}, nil
			}
			return nil, fmt.Errorf("评论区没有加载出来（可能需要登录）")
		}
	}
	// Usernames and texts are siblings without a stable wrapper, so extract
	// both sequences and zip them by position.
	texts, err := page.ExtractUntil(ctx, plugin.ExtractSpec{Item: selCommentText, Fields: map[string]plugin.Field{"text": {}}}, limit, selCommentText+":last-of-type", limit/10+5)
	if err != nil {
		return nil, err
	}
	users, _ := page.ExtractList(ctx, plugin.ExtractSpec{Item: selCommentUser, Fields: map[string]plugin.Field{"user": {}, "url": {Selector: "a", Attr: "href"}, "self_url": {Attr: "href"}}, Limit: limit})
	likes, _ := page.ExtractList(ctx, plugin.ExtractSpec{Item: `[data-e2e="comment-like-count"]`, Fields: map[string]plugin.Field{"likes": {}}, Limit: limit})
	out := make([]map[string]string, 0, len(texts))
	for i, t := range texts {
		row := map[string]string{"text": t["text"]}
		if i < len(users) {
			row["user"] = users[i]["user"]
			if row["user_url"] = users[i]["url"]; row["user_url"] == "" {
				row["user_url"] = users[i]["self_url"]
			}
		}
		if len(likes) == len(texts) {
			row["likes"] = likes[i]["likes"]
		}
		out = append(out, row)
	}
	return out, nil
}

func toggleLike(ctx context.Context, page *plugin.Page, want bool) (any, error) {
	var info plugin.ElementInfo
	err := plugin.Poll(ctx, 10*time.Second, 500*time.Millisecond, func() (bool, error) {
		var err error
		info, err = page.Query(ctx, selLike)
		return err == nil && info.Exists, err
	})
	if err != nil {
		return nil, fmt.Errorf("找不到点赞按钮: %w", err)
	}
	state, known := info.Attrs["aria-pressed"]
	if known && (state == "true") == want {
		return map[string]any{"success": true, "changed": false, "liked": want}, nil
	}
	if !known && !want {
		return nil, fmt.Errorf("无法读取点赞状态，不执行取消点赞以免误点成点赞")
	}
	if err := page.ClickElement(ctx, selLike, plugin.ClickOptions{}); err != nil {
		return nil, err
	}
	if !known {
		return map[string]any{"success": true, "verified": false, "note": "页面没有暴露点赞状态，未确认结果"}, nil
	}
	err = plugin.Poll(ctx, 5*time.Second, 400*time.Millisecond, func() (bool, error) {
		info, err := page.Query(ctx, selLike)
		return err == nil && (info.Attrs["aria-pressed"] == "true") == want, nil
	})
	if err != nil {
		return nil, fmt.Errorf("点击后点赞状态未变为 %v（可能需要登录）", want)
	}
	return map[string]any{"success": true, "changed": true, "liked": want}, nil
}

// creatorFlow drives TikTok Studio's upload page.
var creatorFlow = plugin.CreatorFlow{
	URL:       "https://www.tiktok.com/tiktokstudio/upload?from=creator_center",
	LoginHint: "/login",
	FileInput: `input[type="file"]`,
	Ready:     []string{`.public-DraftEditor-content[contenteditable="true"]`, `div[contenteditable="true"]`},
	Body:      []string{`.public-DraftEditor-content[contenteditable="true"]`, `div[contenteditable="true"]`},
	BodyMax:   2200,
	Publish: []plugin.ClickTarget{{Selector: `button[data-e2e="post_video_button"]`},
		{Selector: "button", Text: "Post", Exact: true}, {Selector: "button", Text: "发布", Exact: true}},
	Draft: []plugin.ClickTarget{{Selector: `button[data-e2e="save_draft_button"]`},
		{Selector: "button", Text: "Save draft", Exact: true}, {Selector: "button", Text: "保存草稿", Exact: true}, {Selector: "button", Text: "存草稿", Exact: true}},
	Confirm:           []plugin.ClickTarget{{Selector: "div[role='dialog'] button", Text: "Post now", Exact: true}, {Selector: "div[role='dialog'] button", Text: "立即发布", Exact: true}},
	RedirectOnSuccess: true,
	PublishedTexts:    []string{"Your video has been uploaded", "Video published", "视频已发布"},
	DraftTexts:        []string{"Draft saved", "Saved to drafts", "草稿已保存", "已保存为草稿"},
}

func publish(ctx context.Context, page *plugin.Page, in input) (any, error) {
	draft, err := plugin.ParseMode(in.Mode)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Caption) == "" {
		return nil, fmt.Errorf("caption is required")
	}
	return plugin.RunCreatorFlow(ctx, page, creatorFlow, plugin.CreatorPost{Files: []string{in.FilePath}, Body: in.Caption, Draft: draft})
}
