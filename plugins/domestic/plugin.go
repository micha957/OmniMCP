package domestic

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/plugin"
	"net/url"
	"strings"
	"time"
)

// Config describes one page-driven platform. Selectors are best-effort and
// tried in order; actions verify their effect rather than trusting a click.
type Config struct {
	ID, Name, Host string
	// SearchTemplate and DetailTemplate take one %s: the escaped keyword / id.
	SearchTemplate, DetailTemplate string
	// DetailPath keeps "/" in ids such as weibo's "uid/mid".
	DetailPath    bool
	LikeSelectors []string
	// LikedMarker is a substring of the like button's class once liked; empty
	// means the platform exposes no reliable state and likes are unverified.
	LikedMarker string
	Comment     plugin.CommentFlow
	// Creator enables publish_video through the platform's creator centre.
	Creator *plugin.CreatorFlow
	// CreatorAccount enables login and account data tools; so far only Douyin's
	// creator centre has endpoints checked against a real account.
	CreatorAccount bool
}

type Plugin struct{ Config }

func New(c Config) *Plugin { return &Plugin{c} }

func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: p.ID, Name: p.Name, Version: "0.2.0", APIVersion: "v1", RequiresBrowser: true, BrowserMode: "extension", LoginURL: "https://" + p.Host, Capabilities: []string{"login", "search", "read", "like", "comment"}}
}
func (p *Plugin) Tools() []plugin.Tool {
	tools := []plugin.Tool{
		{Name: "search", Description: "搜索" + p.Name + "内容，返回搜索结果页文本", InputSchema: plugin.ToolSchema(`{"keyword":{"type":"string"}}`, []string{"keyword"})},
		{Name: "open", Description: "打开" + p.Name + "网页", InputSchema: plugin.ToolSchema(`{"url":{"type":"string","format":"uri"}}`, []string{"url"})},
		{Name: "snapshot", Description: "读取当前" + p.Name + "页面", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "get_detail", Description: "读取" + p.Name + "内容详情页文本", InputSchema: plugin.ToolSchema(`{"id":{"type":"string"}}`, []string{"id"})},
		{Name: "like", Description: "点赞" + p.Name + "内容；能读到点赞状态时会确认结果", Mutating: true, InputSchema: plugin.ToolSchema(`{"id":{"type":"string"}}`, []string{"id"})},
		{Name: "comment", Description: "评论" + p.Name + "内容，提交后在页面上确认评论出现", Mutating: true, InputSchema: plugin.ToolSchema(`{"id":{"type":"string"},"content":{"type":"string"}}`, []string{"id", "content"})},
	}
	if p.CreatorAccount {
		tools = append(tools,
			plugin.Tool{Name: "check_login", Description: "检查" + p.Name + "创作者中心登录状态，返回当前用户", InputSchema: plugin.ToolSchema(`{}`, nil)},
			plugin.Tool{Name: "get_my_account", Description: "读取" + p.Name + "账号粉丝数和作品的播放、点赞、收藏、评论、分享数据", InputSchema: plugin.ToolSchema(`{}`, nil)})
	}
	if f := p.Creator; f != nil {
		props := `{"file_path":{"type":"string","description":"本机视频绝对路径"},"content":{"type":"string","description":"简介/文案，可含 #话题"},"mode":{"type":"string","enum":["publish","draft"]}`
		desc := "通过" + p.Name + "创作者中心上传视频：mode=publish 直接发布"
		if len(f.Draft) > 0 {
			desc += "，mode=draft 存草稿"
		}
		if len(f.Title) > 0 {
			props += fmt.Sprintf(`,"title":{"type":"string","maxLength":%d}`, f.TitleMax)
		}
		tools = append(tools, plugin.Tool{Name: "publish_video", Description: desc, Mutating: true, Timeout: 20 * time.Minute,
			InputSchema: plugin.ToolSchema(props+"}", []string{"file_path", "mode"})})
	}
	return tools
}

func (p *Plugin) detailURL(id string) string {
	if p.DetailPath {
		return fmt.Sprintf(p.DetailTemplate, strings.Trim(id, "/"))
	}
	return fmt.Sprintf(p.DetailTemplate, url.PathEscape(id))
}

func (p *Plugin) Execute(ctx context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in struct {
		Keyword, URL, ID, Content, Title, Mode string
		FilePath                               string `json:"file_path"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	switch name {
	case "check_login":
		return douyinCheckLogin(ctx, c)
	case "get_my_account":
		return douyinAccount(ctx, c)
	case "search":
		if strings.TrimSpace(in.Keyword) == "" {
			return nil, fmt.Errorf("keyword is required")
		}
		if err := c.Browser.Open(ctx, fmt.Sprintf(p.SearchTemplate, url.QueryEscape(in.Keyword))); err != nil {
			return nil, err
		}
		text, err := c.Browser.Snapshot(ctx)
		return map[string]any{"keyword": in.Keyword, "text": text}, err
	case "open":
		if err := c.Browser.Open(ctx, in.URL); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "url": in.URL}, nil
	case "snapshot":
		text, err := c.Browser.Snapshot(ctx)
		return map[string]any{"text": text}, err
	case "get_detail":
		if in.ID == "" {
			return nil, fmt.Errorf("id is required")
		}
		if err := c.Browser.Open(ctx, p.detailURL(in.ID)); err != nil {
			return nil, err
		}
		text, err := c.Browser.Snapshot(ctx)
		return map[string]any{"id": in.ID, "text": text}, err
	case "like":
		if in.ID == "" {
			return nil, fmt.Errorf("id is required")
		}
		if err := c.Browser.Open(ctx, p.detailURL(in.ID)); err != nil {
			return nil, err
		}
		return p.like(ctx, c.Browser)
	case "comment":
		if in.ID == "" || strings.TrimSpace(in.Content) == "" {
			return nil, fmt.Errorf("id and content are required")
		}
		if err := c.Browser.Open(ctx, p.detailURL(in.ID)); err != nil {
			return nil, err
		}
		if err := plugin.PostComment(ctx, c.Browser, p.Comment, in.Content); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "verified": true}, nil
	case "publish_video":
		if p.Creator == nil {
			break
		}
		draft, err := plugin.ParseMode(in.Mode)
		if err != nil {
			return nil, err
		}
		page, err := plugin.NewPage(c.Browser)
		if err != nil {
			return nil, err
		}
		return plugin.RunCreatorFlow(ctx, page, *p.Creator, plugin.CreatorPost{Files: []string{in.FilePath}, Title: in.Title, Body: in.Content, Draft: draft})
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

func (p *Plugin) like(ctx context.Context, b plugin.Browser) (any, error) {
	in, _ := b.(plugin.Inspector)
	var last error
	for _, sel := range p.LikeSelectors {
		if in != nil {
			info, err := in.Query(ctx, sel)
			if err != nil || !info.Exists {
				continue
			}
			if p.LikedMarker != "" && strings.Contains(info.Attrs["class"], p.LikedMarker) {
				return map[string]any{"success": true, "changed": false, "liked": true}, nil
			}
		}
		if ti, ok := b.(plugin.TrustedInput); ok {
			last = ti.ClickElement(ctx, sel, plugin.ClickOptions{})
		} else {
			last = b.Click(ctx, sel)
		}
		if last != nil {
			continue
		}
		if p.LikedMarker == "" || in == nil {
			return map[string]any{"success": true, "verified": false, "note": "该平台无法读取点赞状态，未确认结果"}, nil
		}
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if info, err := in.Query(ctx, sel); err == nil && strings.Contains(info.Attrs["class"], p.LikedMarker) {
				return map[string]any{"success": true, "changed": true, "liked": true}, nil
			}
			time.Sleep(400 * time.Millisecond)
		}
		return nil, fmt.Errorf("点击后未检测到已点赞状态")
	}
	return nil, fmt.Errorf("找不到点赞按钮: %v", last)
}
