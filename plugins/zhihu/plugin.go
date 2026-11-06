package zhihu

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/plugin"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "zhihu", Name: "知乎", Version: "0.2.0", APIVersion: "v1", RequiresBrowser: true, BrowserMode: "extension", LoginURL: "https://www.zhihu.com/signin", CookieDomains: []string{"zhihu.com"}, Capabilities: []string{"login", "search", "read", "publish", "like", "comment"}}
}
func (*Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "check_login", Description: "检查知乎登录状态，返回当前用户", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "get_notifications", Description: "读取评论和回复通知。complete=false 表示还有更早的通知未读取", InputSchema: plugin.ToolSchema(`{"limit":{"type":"integer","minimum":1,"maximum":50}}`, nil)},
		{Name: "get_my_account", Description: "读取当前账号粉丝、获赞数据，以及最近回答和文章的赞同、评论数", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "author_scan", Description: "滚动加载作者文章目录，每轮最多20秒；continue=true继续同一目录页，返回总数与覆盖状态", InputSchema: plugin.ToolSchema(`{"url":{"type":"string"},"continue":{"type":"boolean"}}`, []string{"url"})},
		{Name: "author_posts", Description: "读取知乎作者当前已加载文章目录；有界采样，不宣称全量", InputSchema: plugin.ToolSchema(`{"url":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":100}}`, []string{"url"})},
		{Name: "article_detail", Description: "读取专栏文章完整 DOM 正文、段落、图片链接、时间和完整性信号", InputSchema: plugin.ToolSchema(`{"url":{"type":"string"}}`, []string{"url"})},
		{Name: "search", Description: "搜索知乎内容", InputSchema: plugin.ToolSchema(`{"keyword":{"type":"string"}}`, []string{"keyword"})},
		{Name: "open", Description: "打开知乎网页", InputSchema: plugin.ToolSchema(`{"url":{"type":"string"}}`, []string{"url"})},
		{Name: "snapshot", Description: "读取当前知乎页面", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "get_detail", Description: "读取知乎问题、回答或文章页面；传 url，或传问题 id", InputSchema: plugin.ToolSchema(`{"url":{"type":"string"},"id":{"type":"string","description":"问题 ID"}}`, nil)},
		{Name: "like", Description: "赞同知乎回答或文章（页面上第一个赞同按钮），并确认按钮变为已赞同", Mutating: true, InputSchema: plugin.ToolSchema(`{"url":{"type":"string"},"id":{"type":"string","description":"问题 ID"}}`, nil)},
		{Name: "comment", Description: "评论知乎回答或文章，提交后确认评论出现", Mutating: true, InputSchema: plugin.ToolSchema(`{"url":{"type":"string"},"id":{"type":"string","description":"问题 ID"},"content":{"type":"string"}}`, []string{"content"})},
		{Name: "editor_snapshot", Description: "读取知乎文章编辑器状态", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "fill_article", Description: "在当前已打开的知乎编辑器里填写标题和正文，不提交", Mutating: true, InputSchema: plugin.ToolSchema(`{"title":{"type":"string","maxLength":100},"content":{"type":"string"}}`, []string{"title", "content"})},
		{Name: "publish_article", Description: "写一篇知乎专栏文章：mode=draft 保存为草稿（编辑器自动保存），mode=publish 直接发布。返回文章链接", Mutating: true, Timeout: 3 * time.Minute, InputSchema: plugin.ToolSchema(`{"title":{"type":"string","maxLength":100},"content":{"type":"string"},"mode":{"type":"string","enum":["publish","draft"]}}`, []string{"title", "content", "mode"})},
		{Name: "click", Description: "点击知乎当前页面元素", Mutating: true, InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"}}`, []string{"selector"})},
		{Name: "fill", Description: "填写知乎当前页面元素", Mutating: true, InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"},"value":{"type":"string"}}`, []string{"selector", "value"})},
	}
}

type input struct {
	Keyword, URL, ID, Title, Content, Selector, Value, Mode string
	Limit                                                   int
	Continue                                                bool
}

func (p *Plugin) Execute(ctx context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	switch name {
	case "check_login":
		u, err := checkLogin(ctx, c)
		if err != nil {
			return nil, err
		}
		if u == nil {
			return map[string]any{"logged_in": false}, nil
		}
		return map[string]any{"logged_in": true, "user": map[string]string{"user_id": u.ID, "nickname": u.Name}}, nil
	case "get_notifications":
		return notifications(ctx, c, in.Limit)
	case "get_my_account":
		return myAccount(ctx, c)
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
		if err := c.Browser.Open(ctx, "https://www.zhihu.com/search?type=content&q="+url.QueryEscape(in.Keyword)); err != nil {
			return nil, err
		}
		t, e := c.Browser.Snapshot(ctx)
		return map[string]any{"keyword": in.Keyword, "text": t}, e
	case "get_detail":
		target, err := contentURL(in.ID, in.URL)
		if err != nil {
			return nil, err
		}
		if err := c.Browser.Open(ctx, target); err != nil {
			return nil, err
		}
		t, e := c.Browser.Snapshot(ctx)
		return map[string]any{"url": target, "text": t}, e
	case "like":
		target, err := contentURL(in.ID, in.URL)
		if err != nil {
			return nil, err
		}
		if err := c.Browser.Open(ctx, target); err != nil {
			return nil, err
		}
		return like(ctx, c.Browser)
	case "comment":
		if strings.TrimSpace(in.Content) == "" {
			return nil, fmt.Errorf("content is required")
		}
		target, err := contentURL(in.ID, in.URL)
		if err != nil {
			return nil, err
		}
		if err := c.Browser.Open(ctx, target); err != nil {
			return nil, err
		}
		if err := plugin.PostComment(ctx, c.Browser, commentFlow, in.Content); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "verified": true}, nil
	case "publish_article":
		return publishArticle(ctx, c.Browser, in)
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

// contentURL accepts a full zhihu.com URL or a bare question id.
func contentURL(id, raw string) (string, error) {
	if raw == "" {
		if strings.TrimSpace(id) == "" {
			return "", fmt.Errorf("url or id is required")
		}
		return "https://www.zhihu.com/question/" + url.PathEscape(strings.TrimSpace(id)), nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !(u.Hostname() == "zhihu.com" || strings.HasSuffix(u.Hostname(), ".zhihu.com")) {
		return "", fmt.Errorf("url must be an https zhihu.com address")
	}
	return u.String(), nil
}

const voteSelector = "button.VoteButton--up, button.VoteButton[aria-label*='赞同']"

var commentFlow = plugin.CommentFlow{
	Openers: []plugin.ClickTarget{{Selector: ".ContentItem-actions button", Text: "条评论"}, {Selector: ".ContentItem-actions button", Text: "添加评论"}},
	Inputs:  []string{".CommentEditor-input [contenteditable='true']", ".public-DraftEditor-content[contenteditable='true']", "div[contenteditable='true']"},
	Submits: []plugin.ClickTarget{{Selector: "button", Text: "发布"}},
}

func like(ctx context.Context, b plugin.Browser) (any, error) {
	in, ok1 := b.(plugin.Inspector)
	ti, ok2 := b.(plugin.TrustedInput)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("browser connector does not support verified input; reload the OmniMCP extension")
	}
	if err := in.WaitFor(ctx, voteSelector, "", 8*time.Second); err != nil {
		return nil, fmt.Errorf("找不到赞同按钮: %w", err)
	}
	if info, err := in.Query(ctx, voteSelector); err == nil && strings.Contains(info.Attrs["class"], "is-active") {
		return map[string]any{"success": true, "changed": false, "voted": true}, nil
	}
	if err := ti.ClickElement(ctx, voteSelector, plugin.ClickOptions{}); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := in.Query(ctx, voteSelector); err == nil && strings.Contains(info.Attrs["class"], "is-active") {
			return map[string]any{"success": true, "changed": true, "voted": true}, nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	return nil, fmt.Errorf("点击后赞同按钮未变为已赞同")
}

const (
	selArticleTitle = `textarea[placeholder*="标题"]`
	selArticleBody  = `.public-DraftEditor-content[contenteditable="true"]`
)

// publishWait bounds how long to wait for the article page after publishing.
var publishWait = 30 * time.Second

var articlePath = regexp.MustCompile(`^/p/(\d+)(/edit)?/?$`)

// publishArticle writes a new column article. The editor autosaves to a
// draft at /p/<id>/edit; publishing moves the page to /p/<id>.
func publishArticle(ctx context.Context, b plugin.Browser, in input) (any, error) {
	draft, err := plugin.ParseMode(in.Mode)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Content) == "" {
		return nil, fmt.Errorf("title and content are required")
	}
	if n := len([]rune(in.Title)); n > 100 {
		return nil, fmt.Errorf("title must be <=100 characters")
	}
	page, err := plugin.NewPage(b)
	if err != nil {
		return nil, err
	}
	if err := page.Open(ctx, "https://zhuanlan.zhihu.com/write"); err != nil {
		return nil, err
	}
	if u, _ := page.CurrentURL(ctx); strings.Contains(u, "signin") {
		return nil, fmt.Errorf("知乎未登录，请先在 Chrome 中登录")
	}
	if err := page.WaitFor(ctx, selArticleTitle, "", 15*time.Second); err != nil {
		return nil, fmt.Errorf("没有打开文章编辑器: %w", err)
	}
	if err := page.TypeText(ctx, selArticleTitle, in.Title); err != nil {
		return nil, fmt.Errorf("填写标题失败: %w", err)
	}
	if err := page.TypeText(ctx, selArticleBody, in.Content); err != nil {
		return nil, fmt.Errorf("填写正文失败: %w", err)
	}
	// Wait for the autosave to assign the draft its /p/<id>/edit address,
	// so a later URL change can only mean the article was published.
	var draftURL string
	err = plugin.Poll(ctx, 30*time.Second, time.Second, func() (bool, error) {
		u, err := page.CurrentURL(ctx)
		if err != nil {
			return false, err
		}
		if parsed, err := url.Parse(u); err == nil && articlePath.MatchString(parsed.Path) {
			draftURL = u
			return true, nil
		}
		return false, fmt.Errorf("草稿还没有自动保存")
	})
	if err != nil {
		return nil, err
	}
	if draft {
		return map[string]any{"success": true, "status": "draft_saved", "url": draftURL}, nil
	}
	if _, err := plugin.ClickFirst(ctx, page, []plugin.ClickTarget{{Selector: "button", Text: "发布", Exact: true}}, 15*time.Second); err != nil {
		return nil, fmt.Errorf("没有找到发布按钮: %w", err)
	}
	if _, err := page.AwaitOutcome(ctx, draftURL, true, nil, nil, publishWait); err != nil {
		return nil, fmt.Errorf("点击发布后页面没有跳转到文章页（可能需要在发布设置里补充话题，草稿已保存在 %s）: %w: %w", draftURL, plugin.ErrOutcomeUnknown, err)
	}
	u, _ := page.CurrentURL(ctx)
	if parsed, err := url.Parse(u); err != nil || !articlePath.MatchString(parsed.Path) || strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/edit") {
		return nil, fmt.Errorf("发布后跳转到了意外的页面 %s: %w", u, plugin.ErrOutcomeUnknown)
	}
	return map[string]any{"success": true, "status": "published", "url": u}, nil
}
