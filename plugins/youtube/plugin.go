package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/heybox/omnimcp/plugin"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "youtube", Name: "YouTube", Version: "0.1.0", APIVersion: "v1", RequiresBrowser: true, BrowserMode: "extension",
		LoginURL: "https://accounts.google.com/ServiceLogin?service=youtube&continue=https://www.youtube.com/", CookieDomains: []string{"youtube.com"},
		Capabilities: []string{"login", "search", "video", "comments", "channel", "like", "comment", "upload"}}
}

func (*Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "check_login", Description: "检查 YouTube 是否已登录", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "search_videos", Description: "搜索 YouTube 视频，返回视频 ID、标题、频道、播放量、时长、发布时间", InputSchema: plugin.ToolSchema(`{"keyword":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":100},"type":{"type":"string","enum":["video","shorts","all"],"description":"默认 video"}}`, []string{"keyword"})},
		{Name: "get_video", Description: "获取视频详情：标题、完整简介、频道、时长、播放量、点赞数、发布日期、关键词", InputSchema: plugin.ToolSchema(`{"video":{"type":"string","description":"视频 ID 或链接"}}`, []string{"video"})},
		{Name: "get_comments", Description: "读取视频评论（按页面默认排序），滚动加载到 limit 条", Timeout: 3 * time.Minute, InputSchema: plugin.ToolSchema(`{"video":{"type":"string","description":"视频 ID 或链接"},"limit":{"type":"integer","minimum":1,"maximum":500}}`, []string{"video"})},
		{Name: "get_channel_videos", Description: "获取频道信息和视频列表；channel 可为 @handle、UC 开头的频道 ID 或频道链接", Timeout: 3 * time.Minute, InputSchema: plugin.ToolSchema(`{"channel":{"type":"string"},"tab":{"type":"string","enum":["videos","shorts","streams"]},"limit":{"type":"integer","minimum":1,"maximum":300}}`, []string{"channel"})},
		{Name: "like_video", Description: "点赞或取消点赞（unlike=true），以按钮状态确认结果", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"video":{"type":"string"},"unlike":{"type":"boolean"}}`, []string{"video"})},
		{Name: "post_comment", Description: "在视频下发表评论，并确认评论出现", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"video":{"type":"string"},"content":{"type":"string","maxLength":10000}}`, []string{"video", "content"})},
		{Name: "upload_video", Description: "通过 YouTube Studio 上传视频：mode=publish 按 visibility 发布（默认 private），mode=draft 填好标题简介后存为草稿。file_path 为本机视频绝对路径", Mutating: true, Confirm: true, Timeout: 20 * time.Minute, InputSchema: plugin.ToolSchema(`{"file_path":{"type":"string"},"title":{"type":"string","maxLength":100},"description":{"type":"string","maxLength":5000},"visibility":{"type":"string","enum":["private","unlisted","public"]},"made_for_kids":{"type":"boolean"},"mode":{"type":"string","enum":["publish","draft"]}}`, []string{"file_path", "title", "mode"})},
	}
}

type input struct {
	Keyword     string `json:"keyword"`
	Type        string `json:"type"`
	Video       string `json:"video"`
	Channel     string `json:"channel"`
	Tab         string `json:"tab"`
	Limit       int    `json:"limit"`
	Unlike      bool   `json:"unlike"`
	Content     string `json:"content"`
	FilePath    string `json:"file_path"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
	MadeForKids bool   `json:"made_for_kids"`
	Mode        string `json:"mode"`
}

const (
	selLike        = "like-button-view-model button, #segmented-like-button button"
	selLikeText    = "like-button-view-model .yt-spec-button-shape-next__button-text-content"
	selComments    = "ytd-comments#comments"
	selThread      = "ytd-comment-thread-renderer"
	selUploadInput = `ytcp-uploads-file-picker input[type="file"], input[type="file"]`
	selTitleBox    = "#title-textarea #textbox"
	selDescBox     = "#description-textarea #textbox"
)

var commentFlow = plugin.CommentFlow{
	Openers: []plugin.ClickTarget{{Selector: "#simplebox-placeholder"}},
	Inputs:  []string{"#contenteditable-root[contenteditable='true']", "ytd-commentbox #contenteditable-root"},
	Submits: []plugin.ClickTarget{{Selector: "ytd-commentbox #submit-button button"}, {Selector: "#submit-button button"}},
	Verify:  15 * time.Second,
}

var commentSpec = plugin.ExtractSpec{Item: selThread, Fields: map[string]plugin.Field{
	"author":     {Selector: "#author-text"},
	"author_url": {Selector: "#author-text", Attr: "href"},
	"text":       {Selector: "#content-text"},
	"likes":      {Selector: "#vote-count-middle"},
	"published":  {Selector: "#published-time-text"},
	"url":        {Selector: "#published-time-text a", Attr: "href"},
	"replies":    {Selector: "#more-replies"},
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
	videoID := func() (string, error) { return VideoID(in.Video) }
	switch name {
	case "check_login":
		if err := page.Open(ctx, "https://www.youtube.com/"); err != nil {
			return nil, err
		}
		raw, err := page.PollGlobal(ctx, "ytcfg.data_.LOGGED_IN", 8*time.Second, nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"logged_in": string(raw) == "true"}, nil

	case "search_videos":
		if strings.TrimSpace(in.Keyword) == "" {
			return nil, fmt.Errorf("keyword is required")
		}
		limit := clamp(in.Limit, 20, 100)
		// sp filters: EgIQAQ== = videos only, EgIYAQ== would be short duration.
		target := "https://www.youtube.com/results?search_query=" + url.QueryEscape(in.Keyword)
		if in.Type == "" || in.Type == "video" {
			target += "&sp=EgIQAQ%253D%253D"
		}
		if err := page.Open(ctx, target); err != nil {
			return nil, err
		}
		videos, err := loadVideos(ctx, page, limit, "ytd-video-renderer:last-of-type, ytd-item-section-renderer:last-of-type")
		if err != nil {
			return nil, err
		}
		if in.Type == "shorts" {
			videos = filter(videos, func(v Video) bool { return v.Short })
		}
		return map[string]any{"keyword": in.Keyword, "items": head(videos, limit)}, nil

	case "get_video":
		id, err := videoID()
		if err != nil {
			return nil, err
		}
		if err := page.Open(ctx, watchURL(id)); err != nil {
			return nil, err
		}
		raw, err := page.PollGlobal(ctx, "ytInitialPlayerResponse", 10*time.Second, func(r json.RawMessage) bool {
			d, err := ParsePlayer(r)
			return err == nil && d.ID == id
		})
		if err != nil {
			return nil, err
		}
		detail, err := ParsePlayer(raw)
		if err != nil {
			return nil, err
		}
		if info, err := page.Query(ctx, selLikeText); err == nil && info.Exists {
			detail.Likes = info.Text
		}
		return detail, nil

	case "get_comments":
		id, err := videoID()
		if err != nil {
			return nil, err
		}
		if err := page.Open(ctx, watchURL(id)); err != nil {
			return nil, err
		}
		if err := revealComments(ctx, page); err != nil {
			return nil, err
		}
		limit := clamp(in.Limit, 20, 500)
		items, err := page.ExtractUntil(ctx, commentSpec, limit, selThread+":last-of-type", limit/10+5)
		if err != nil {
			return nil, err
		}
		return map[string]any{"video_id": id, "count": len(items), "items": items}, nil

	case "get_channel_videos":
		target, err := ChannelURL(in.Channel, in.Tab)
		if err != nil {
			return nil, err
		}
		if err := page.Open(ctx, target); err != nil {
			return nil, err
		}
		limit := clamp(in.Limit, 30, 300)
		raw, err := page.PollGlobal(ctx, "ytInitialData", 10*time.Second, nil)
		if err != nil {
			return nil, err
		}
		channel, err := ParseChannel(raw)
		if err != nil {
			return nil, fmt.Errorf("不是有效的频道页面（%s）: %w", target, err)
		}
		videos, err := loadVideos(ctx, page, limit, "ytd-rich-grid-renderer #contents > :last-child")
		if err != nil {
			return nil, err
		}
		return map[string]any{"channel": channel, "tab": strings.TrimPrefix(target[strings.LastIndex(target, "/"):], "/"), "items": head(videos, limit)}, nil

	case "like_video":
		id, err := videoID()
		if err != nil {
			return nil, err
		}
		if err := page.Open(ctx, watchURL(id)); err != nil {
			return nil, err
		}
		return toggleLike(ctx, page, !in.Unlike)

	case "post_comment":
		id, err := videoID()
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Content) == "" {
			return nil, fmt.Errorf("content is required")
		}
		if err := page.Open(ctx, watchURL(id)); err != nil {
			return nil, err
		}
		if err := revealComments(ctx, page); err != nil {
			return nil, err
		}
		if info, _ := page.Query(ctx, "ytd-comments ytd-message-renderer"); info.Exists {
			return nil, fmt.Errorf("该视频已关闭评论: %s", info.Text)
		}
		if err := plugin.PostComment(ctx, page, commentFlow, in.Content); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "verified": true, "video_id": id}, nil

	case "upload_video":
		return upload(ctx, page, in)
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

func head(v []Video, n int) []Video {
	if len(v) > n {
		return v[:n]
	}
	return v
}

func filter(v []Video, keep func(Video) bool) []Video {
	out := v[:0:0]
	for _, x := range v {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

// loadVideos reads the server-rendered first page from ytInitialData, then
// scrolls and extracts DOM cards for anything beyond it, because continuation
// pages are not written back into ytInitialData.
func loadVideos(ctx context.Context, page *plugin.Page, limit int, scrollTarget string) ([]Video, error) {
	raw, err := page.PollGlobal(ctx, "ytInitialData", 10*time.Second, nil)
	if err != nil {
		return nil, err
	}
	videos, err := ParseVideos(raw)
	if err != nil {
		return nil, err
	}
	if len(videos) >= limit {
		return videos, nil
	}
	spec := plugin.ExtractSpec{Item: "ytd-video-renderer, ytd-rich-item-renderer, ytd-grid-video-renderer", Fields: map[string]plugin.Field{
		"url":       {Selector: "a#video-title, a#video-title-link, a#thumbnail, a.yt-lockup-view-model-wiz__title, a[href*='/watch'], a[href*='/shorts/']", Attr: "href"},
		"title":     {Selector: "#video-title, h3"},
		"channel":   {Selector: "ytd-channel-name a, #channel-name a"},
		"meta":      {Selector: "#metadata-line, .yt-content-metadata-view-model-wiz"},
		"duration":  {Selector: "ytd-thumbnail-overlay-time-status-renderer, badge-shape"},
		"thumbnail": {Selector: "img", Attr: "src"},
	}}
	rows, err := page.ExtractUntil(ctx, spec, limit, scrollTarget, limit/10+5)
	if err != nil {
		return videos, nil
	}
	seen := map[string]bool{}
	for _, v := range videos {
		seen[v.ID] = true
	}
	for _, r := range rows {
		id, err := VideoID(r["url"])
		if err != nil || seen[id] {
			continue
		}
		seen[id] = true
		v := Video{ID: id, URL: r["url"], Title: r["title"], Channel: r["channel"], Duration: strings.TrimSpace(r["duration"]), Thumbnail: r["thumbnail"], Short: strings.Contains(r["url"], "/shorts/")}
		// Metadata reads like "1.2M views\n3 days ago" or "1.2M views • 3 days ago".
		var meta []string
		for _, part := range strings.FieldsFunc(r["meta"], func(c rune) bool { return c == '\n' || c == '•' }) {
			if part = strings.TrimSpace(part); part != "" {
				meta = append(meta, part)
			}
		}
		if n := len(meta); n >= 2 {
			v.Views, v.Published = meta[n-2], meta[n-1]
		} else if n == 1 {
			v.Views = meta[0]
		}
		videos = append(videos, v)
	}
	return videos, nil
}

func revealComments(ctx context.Context, page *plugin.Page) error {
	return plugin.Poll(ctx, 20*time.Second, 800*time.Millisecond, func() (bool, error) {
		_ = page.Scroll(ctx, selComments)
		if info, err := page.Query(ctx, selThread); err == nil && info.Exists {
			return true, nil
		}
		// Comments disabled, or the comment box loaded before any thread.
		for _, sel := range []string{"ytd-comments ytd-message-renderer", "#simplebox-placeholder"} {
			if info, err := page.Query(ctx, sel); err == nil && info.Exists {
				return true, nil
			}
		}
		return false, fmt.Errorf("评论区没有加载出来")
	})
}

func toggleLike(ctx context.Context, page *plugin.Page, want bool) (any, error) {
	pressed := func() (bool, bool) {
		info, err := page.Query(ctx, selLike)
		if err != nil || !info.Exists {
			return false, false
		}
		return info.Attrs["aria-pressed"] == "true", true
	}
	var cur, found bool
	_ = plugin.Poll(ctx, 10*time.Second, 500*time.Millisecond, func() (bool, error) {
		cur, found = pressed()
		return found, nil
	})
	if !found {
		return nil, fmt.Errorf("找不到点赞按钮（可能未登录）")
	}
	if cur == want {
		return map[string]any{"success": true, "changed": false, "liked": want}, nil
	}
	if err := page.ClickElement(ctx, selLike, plugin.ClickOptions{}); err != nil {
		return nil, err
	}
	err := plugin.Poll(ctx, 5*time.Second, 400*time.Millisecond, func() (bool, error) {
		cur, _ = pressed()
		return cur == want, nil
	})
	if err != nil {
		return nil, fmt.Errorf("点击后点赞状态未变为 %v（可能需要登录）", want)
	}
	return map[string]any{"success": true, "changed": true, "liked": want}, nil
}

func upload(ctx context.Context, page *plugin.Page, in input) (any, error) {
	draft, err := plugin.ParseMode(in.Mode)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(in.FilePath); err != nil {
		return nil, fmt.Errorf("file_path: %w", err)
	}
	if strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	visibility := map[string]string{"": "PRIVATE", "private": "PRIVATE", "unlisted": "UNLISTED", "public": "PUBLIC"}[in.Visibility]
	if visibility == "" {
		return nil, fmt.Errorf("visibility must be private, unlisted or public")
	}
	// youtube.com/upload redirects to Studio with the upload dialog open.
	if err := page.Open(ctx, "https://www.youtube.com/upload"); err != nil {
		return nil, err
	}
	if u, _ := page.CurrentURL(ctx); strings.Contains(u, "accounts.google.com") {
		return nil, fmt.Errorf("YouTube 未登录，请先在 Chrome 中登录")
	}
	if err := page.WaitFor(ctx, selUploadInput, "", 30*time.Second); err != nil {
		return nil, fmt.Errorf("没有打开上传对话框（可能需要先创建频道）: %w", err)
	}
	if err := page.Upload(ctx, selUploadInput, in.FilePath); err != nil {
		return nil, fmt.Errorf("选择视频文件失败: %w", err)
	}
	if err := page.WaitFor(ctx, selTitleBox, "", 2*time.Minute); err != nil {
		return nil, fmt.Errorf("上传后没有进入详情填写页: %w", err)
	}
	time.Sleep(time.Second)
	if err := page.TypeText(ctx, selTitleBox, in.Title); err != nil {
		return nil, fmt.Errorf("填写标题失败: %w", err)
	}
	if in.Description != "" {
		if err := page.TypeText(ctx, selDescBox, in.Description); err != nil {
			return nil, fmt.Errorf("填写简介失败: %w", err)
		}
	}
	kids := "VIDEO_MADE_FOR_KIDS_NOT_MFK"
	if in.MadeForKids {
		kids = "VIDEO_MADE_FOR_KIDS_MFK"
	}
	if err := page.ClickElement(ctx, `tp-yt-paper-radio-button[name="`+kids+`"]`, plugin.ClickOptions{}); err != nil {
		return nil, fmt.Errorf("选择是否面向儿童失败: %w", err)
	}
	videoURL := uploadedURL(ctx, page)
	if draft {
		// Closing the wizard keeps the upload as a draft in Studio's content list.
		if err := page.ClickElement(ctx, "ytcp-uploads-dialog #close-button", plugin.ClickOptions{}); err != nil {
			return nil, fmt.Errorf("关闭上传对话框失败: %w", err)
		}
		err := plugin.Poll(ctx, 30*time.Second, time.Second, func() (bool, error) {
			d, err := page.Query(ctx, "ytcp-uploads-dialog #dialog")
			return err == nil && !d.Visible, nil
		})
		if err != nil {
			return nil, fmt.Errorf("上传对话框没有关闭，草稿可能未保存")
		}
		return map[string]any{"success": true, "status": "draft_saved", "video_url": videoURL}, nil
	}
	// Details → Video elements → Checks → Visibility.
	for i := 0; i < 3; i++ {
		if err := page.ClickElement(ctx, "#next-button", plugin.ClickOptions{}); err != nil {
			return nil, fmt.Errorf("点击下一步失败（第 %d 步）: %w", i+1, err)
		}
		time.Sleep(1500 * time.Millisecond)
	}
	radio := `tp-yt-paper-radio-button[name="` + visibility + `"]`
	if err := page.WaitFor(ctx, radio, "", 15*time.Second); err != nil {
		return nil, fmt.Errorf("没有进入公开范围设置页: %w", err)
	}
	if err := page.ClickElement(ctx, radio, plugin.ClickOptions{}); err != nil {
		return nil, err
	}
	if videoURL == "" {
		videoURL = uploadedURL(ctx, page)
	}
	if err := page.ClickElement(ctx, "#done-button", plugin.ClickOptions{}); err != nil {
		return nil, fmt.Errorf("点击保存/发布失败: %w", err)
	}
	// Studio closes the wizard and shows a share or "still processing" dialog.
	err = plugin.Poll(ctx, 60*time.Second, time.Second, func() (bool, error) {
		for _, sel := range []string{"ytcp-video-share-dialog", "ytcp-uploads-still-processing-dialog", "ytcp-prechecks-warning-dialog"} {
			if info, err := page.Query(ctx, sel); err == nil && info.Visible {
				return true, nil
			}
		}
		done, err := page.Query(ctx, "#done-button")
		return err == nil && !done.Visible, nil
	})
	if err != nil {
		return nil, fmt.Errorf("点击发布后没有确认结果: %w", err)
	}
	return map[string]any{"success": true, "status": "published", "video_url": videoURL, "visibility": strings.ToLower(visibility)}, nil
}

// uploadedURL reads the watch link Studio shows beside the upload form.
func uploadedURL(ctx context.Context, page *plugin.Page) string {
	for _, sel := range []string{".video-url-fadeable a", "a.ytcp-video-info", "ytcp-video-info a"} {
		if info, err := page.Query(ctx, sel); err == nil && info.Exists {
			if href := info.Attrs["href"]; href != "" {
				return href
			}
			return info.Text
		}
	}
	return ""
}
