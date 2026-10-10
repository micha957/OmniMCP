package bilibili

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/plugin"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Plugin struct{ client *http.Client }

func New() *Plugin { return &Plugin{client: &http.Client{Timeout: 30 * time.Second}} }
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "bilibili", Name: "哔哩哔哩", Version: "0.1.0", APIVersion: "v1", RequiresBrowser: true, BrowserMode: "extension", LoginURL: "https://www.bilibili.com/", CookieDomains: []string{"bilibili.com"}, Capabilities: []string{"login", "search", "video", "comments", "user"}}
}
func (*Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "search_videos", Description: "搜索 B 站视频", InputSchema: plugin.ToolSchema(`{"keyword":{"type":"string"},"page":{"type":"integer","minimum":1}}`, []string{"keyword"})},
		{Name: "get_video", Description: "获取 B 站视频详情", InputSchema: plugin.ToolSchema(`{"bvid":{"type":"string"}}`, []string{"bvid"})},
		{Name: "get_comments", Description: "获取 B 站视频评论", InputSchema: plugin.ToolSchema(`{"aid":{"type":"string"},"page":{"type":"integer","minimum":1}}`, []string{"aid"})},
		{Name: "list_user_videos", Description: "获取 UP 主视频列表", InputSchema: plugin.ToolSchema(`{"mid":{"type":"string"},"page":{"type":"integer","minimum":1}}`, []string{"mid"})},
		{Name: "post_comment", Description: "发表评论到 B 站视频（需要 Cookie）", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"aid":{"type":"string"},"content":{"type":"string"}}`, []string{"aid", "content"})},
		{Name: "publish_video", Description: "通过 B 站创作中心投稿视频：mode=publish 立即投稿，mode=draft 存草稿。分区使用页面自动推荐", Mutating: true, Confirm: true, Timeout: 30 * time.Minute, InputSchema: plugin.ToolSchema(`{"file_path":{"type":"string","description":"本机视频绝对路径"},"title":{"type":"string","maxLength":80},"content":{"type":"string","maxLength":2000,"description":"简介"},"tags":{"type":"array","items":{"type":"string"},"maxItems":10,"description":"标签，B 站投稿通常至少需要 1 个"},"mode":{"type":"string","enum":["publish","draft"]}}`, []string{"file_path", "title", "mode"})},
	}
}

type input struct {
	Keyword, BVID, AID, MID, Content, Title, Mode string
	FilePath                                      string `json:"file_path"`
	Tags                                          []string
	Page                                          int
}

// creatorFlow drives the upload page that the creator centre embeds in an iframe;
// opening it directly keeps it in the top frame where the extension runs.
var creatorFlow = plugin.CreatorFlow{
	URL: "https://member.bilibili.com/york/videoup", LoginHint: "passport.bilibili.com",
	FileInput: `input[type="file"]`,
	Ready:     []string{`input[placeholder*="标题"]`},
	Title:     []string{`input[placeholder*="标题"]`}, TitleMax: 80,
	Body:     []string{`.ql-editor[contenteditable="true"]`, `div[contenteditable="true"]`},
	BodyMax:  2000,
	TagInput: []string{`input[placeholder*="回车"]`, `input[placeholder*="标签"]`},
	// Submitting before the upload finishes is rejected by the page.
	UploadDone:     []string{"上传完成"},
	Publish:        []plugin.ClickTarget{{Selector: "span, button", Text: "立即投稿", Exact: true}},
	Draft:          []plugin.ClickTarget{{Selector: "span, button", Text: "存草稿", Exact: true}},
	PublishedTexts: []string{"稿件投递成功", "投稿成功"},
	DraftTexts:     []string{"草稿保存成功", "存草稿成功", "保存草稿成功"},
	UploadTimeout:  25 * time.Minute,
}

func (p *Plugin) Execute(ctx context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	if in.Page < 1 {
		in.Page = 1
	}
	switch name {
	case "search_videos":
		q := url.Values{"search_type": {"video"}, "keyword": {in.Keyword}, "page": {strconv.Itoa(in.Page)}}
		return p.get(ctx, c, "https://api.bilibili.com/x/web-interface/search/type?"+q.Encode())
	case "get_video":
		return p.get(ctx, c, "https://api.bilibili.com/x/web-interface/view?bvid="+url.QueryEscape(in.BVID))
	case "get_comments":
		q := url.Values{"type": {"1"}, "oid": {in.AID}, "pn": {strconv.Itoa(in.Page)}, "ps": {"20"}}
		return p.get(ctx, c, "https://api.bilibili.com/x/v2/reply?"+q.Encode())
	case "list_user_videos":
		q := url.Values{"mid": {in.MID}, "pn": {strconv.Itoa(in.Page)}, "ps": {"20"}, "jsonp": {"jsonp"}}
		return p.get(ctx, c, "https://api.bilibili.com/x/space/arc/search?"+q.Encode())
	case "post_comment":
		return p.postComment(ctx, c, in.AID, in.Content)
	case "publish_video":
		draft, err := plugin.ParseMode(in.Mode)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Title) == "" {
			return nil, fmt.Errorf("title is required")
		}
		page, err := plugin.NewPage(c.Browser)
		if err != nil {
			return nil, err
		}
		return plugin.RunCreatorFlow(ctx, page, creatorFlow, plugin.CreatorPost{Files: []string{in.FilePath}, Title: in.Title, Body: in.Content, Tags: in.Tags, Draft: draft})
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}
func (p *Plugin) postComment(ctx context.Context, c *plugin.Context, aid, content string) (any, error) {
	cookie := p.cookie(ctx, c)
	csrf := cookieValue(cookie, "bili_jct")
	if csrf == "" {
		return nil, fmt.Errorf("BILIBILI_COOKIE must contain bili_jct for post_comment")
	}
	form := url.Values{"type": {"1"}, "oid": {aid}, "message": {content}, "plat": {"1"}, "csrf": {csrf}, "csrf_token": {csrf}}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.bilibili.com/x/v2/reply/add", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 OmniMCP/0.1")
	req.Header.Set("Referer", "https://www.bilibili.com/video/av"+aid)
	req.Header.Set("Cookie", cookie)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("bilibili http status %s", resp.Status)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if code, ok := out["code"].(float64); ok && code != 0 {
		return nil, fmt.Errorf("bilibili api: %v", out)
	}
	return out, nil
}
func cookieValue(cookie, name string) string {
	for _, part := range strings.Split(cookie, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && kv[0] == name {
			return kv[1]
		}
	}
	return ""
}
func (p *Plugin) get(ctx context.Context, c *plugin.Context, u string) (any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 OmniMCP/0.1")
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if cookie := p.cookie(ctx, c); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("bilibili http status %s", resp.Status)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if code, ok := out["code"].(float64); ok && code != 0 {
		return nil, fmt.Errorf("bilibili api: %v", out)
	}
	return out, nil
}

func (p *Plugin) cookie(ctx context.Context, c *plugin.Context) string {
	key := strings.ToUpper(strings.NewReplacer("-", "_", "/", "_", " ", "_").Replace(c.Account.AccountID))
	if v := os.Getenv("OMNIMCP_BILIBILI_COOKIE_" + key); v != "" {
		return v
	}
	if v := os.Getenv("BILIBILI_COOKIE"); v != "" {
		return v
	}
	if c.Auth != nil {
		if v, err := c.Auth.CookieHeader(ctx, "bilibili.com"); err == nil {
			return v
		}
	}
	return ""
}
