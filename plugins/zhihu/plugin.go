package zhihu

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/plugin"
	"net/url"
	"regexp"
	"strings"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "zhihu", Name: "知乎", Version: "0.2.0", APIVersion: "v1", RequiresBrowser: true, BrowserMode: "extension", LoginURL: "https://www.zhihu.com/signin", CookieDomains: []string{"zhihu.com"}, Capabilities: []string{"login", "search", "read", "publish", "like", "comment"}}
}
func (*Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "author_scan", Description: "滚动加载作者文章目录，每轮最多20秒；continue=true继续同一目录页，返回总数与覆盖状态", InputSchema: plugin.ToolSchema(`{"url":{"type":"string"},"continue":{"type":"boolean"}}`, []string{"url"})},
		{Name: "author_posts", Description: "读取知乎作者当前已加载文章目录；有界采样，不宣称全量", InputSchema: plugin.ToolSchema(`{"url":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":100}}`, []string{"url"})},
		{Name: "article_detail", Description: "读取专栏文章完整 DOM 正文、段落、图片链接、时间和完整性信号", InputSchema: plugin.ToolSchema(`{"url":{"type":"string"}}`, []string{"url"})},
		{Name: "search", Description: "搜索知乎内容", InputSchema: plugin.ToolSchema(`{"keyword":{"type":"string"}}`, []string{"keyword"})},
		{Name: "open", Description: "打开知乎网页", InputSchema: plugin.ToolSchema(`{"url":{"type":"string"}}`, []string{"url"})},
		{Name: "snapshot", Description: "读取当前知乎页面", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "get_detail", Description: "读取知乎内容详情", InputSchema: plugin.ToolSchema(`{"id":{"type":"string"}}`, []string{"id"})},
		{Name: "like", Description: "点赞知乎内容", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"id":{"type":"string"}}`, []string{"id"})},
		{Name: "comment", Description: "评论知乎内容", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"id":{"type":"string"},"content":{"type":"string"}}`, []string{"id", "content"})},
		{Name: "editor_snapshot", Description: "读取知乎文章编辑器状态", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "fill_article", Description: "填写知乎文章标题和正文，不提交发布", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"title":{"type":"string","maxLength":100},"content":{"type":"string"}}`, []string{"title", "content"})},
		{Name: "click", Description: "点击知乎当前页面元素", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"}}`, []string{"selector"})},
		{Name: "fill", Description: "填写知乎当前页面元素", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"},"value":{"type":"string"}}`, []string{"selector", "value"})},
	}
}

type input struct {
	Keyword, URL, ID, Title, Content, Selector, Value string
	Limit                                             int
	Continue                                          bool
}

func (p *Plugin) Execute(ctx context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	switch name {
	case "author_scan":
		_, target, err := collectionTarget("author_posts", in.URL)
		if err != nil {
			return nil, err
		}
		reader, ok := c.Browser.(plugin.StructuredReader)
		if !ok {
			return nil, fmt.Errorf("structured collection requires browser extension")
		}
		if !in.Continue {
			if err := c.Browser.Open(ctx, target); err != nil {
				return nil, err
			}
		}
		result, err := reader.ReadZhihu(ctx, "author_scan", 2000)
		if err != nil {
			return nil, err
		}
		var observed struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(result, &observed); err != nil {
			return nil, err
		}
		_, actual, err := collectionTarget("author_posts", observed.URL)
		if err != nil || actual != target {
			return nil, fmt.Errorf("author scan is bound to a different page")
		}
		return result, nil
	case "author_posts", "article_detail":
		mode, target, err := collectionTarget(name, in.URL)
		if err != nil {
			return nil, err
		}
		if in.Limit == 0 {
			in.Limit = 50
		}
		if in.Limit < 1 || in.Limit > 100 {
			return nil, fmt.Errorf("limit must be 1..100")
		}
		reader, ok := c.Browser.(plugin.StructuredReader)
		if !ok {
			return nil, fmt.Errorf("structured collection requires the updated browser extension")
		}
		if err := c.Browser.Open(ctx, target); err != nil {
			return nil, err
		}
		return reader.ReadZhihu(ctx, mode, in.Limit)

	case "open":
		if err := c.Browser.Open(ctx, in.URL); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "url": in.URL}, nil
	case "snapshot", "editor_snapshot":
		t, e := c.Browser.Snapshot(ctx)
		return map[string]any{"text": t, "source": "browser-extension"}, e
	case "search":
		if strings.TrimSpace(in.Keyword) == "" {
			return nil, fmt.Errorf("keyword is required")
		}
		if err := c.Browser.Open(ctx, "https://www.zhihu.com/search?type=content&q="+in.Keyword); err != nil {
			return nil, err
		}
		t, e := c.Browser.Snapshot(ctx)
		return map[string]any{"keyword": in.Keyword, "text": t}, e
	case "get_detail":
		if err := c.Browser.Open(ctx, "https://www.zhihu.com/question/"+in.ID); err != nil {
			return nil, err
		}
		t, e := c.Browser.Snapshot(ctx)
		return map[string]any{"id": in.ID, "text": t}, e
	case "like":
		if err := c.Browser.Open(ctx, "https://www.zhihu.com/question/"+in.ID); err != nil {
			return nil, err
		}
		if err := c.Browser.Click(ctx, "button[aria-label*='赞同']"); err != nil {
			return nil, err
		}
		return map[string]any{"success": true}, nil
	case "comment":
		if strings.TrimSpace(in.Content) == "" {
			return nil, fmt.Errorf("content is required")
		}
		if err := c.Browser.Open(ctx, "https://www.zhihu.com/question/"+in.ID); err != nil {
			return nil, err
		}
		if err := c.Browser.Fill(ctx, "textarea[placeholder*='评论']", in.Content); err != nil {
			return nil, err
		}
		return map[string]any{"success": true}, nil
	case "fill_article":
		if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Content) == "" {
			return nil, fmt.Errorf("title and content are required")
		}
		if len([]rune(in.Title)) > 100 {
			return nil, fmt.Errorf("title must be <=100 characters")
		}
		if err := c.Browser.Fill(ctx, `textarea[placeholder*="请输入标题"]`, in.Title); err != nil {
			return nil, err
		}
		if err := c.Browser.Fill(ctx, `div.public-DraftEditor-content[contenteditable="true"]`, in.Content); err != nil {
			return nil, err
		}
		return map[string]any{"status": "editor_filled", "published": false}, nil
	case "click":
		if err := c.Browser.Click(ctx, in.Selector); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "fill":
		if err := c.Browser.Fill(ctx, in.Selector, in.Value); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}

func collectionTarget(name, raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return "", "", fmt.Errorf("invalid Zhihu URL")
	}
	mode := "article"
	pattern := `^/p/[0-9]+/?$`
	host := "zhuanlan.zhihu.com"
	if name == "author_posts" {
		mode = "author"
		host = "www.zhihu.com"
		pattern = `^/people/[A-Za-z0-9_-]+/posts/?$`
	}
	if u.Hostname() != host || !regexp.MustCompile(pattern).MatchString(u.Path) {
		return "", "", fmt.Errorf("URL does not match %s", name)
	}
	u.RawQuery = ""
	u.Fragment = ""
	return mode, u.String(), nil
}
