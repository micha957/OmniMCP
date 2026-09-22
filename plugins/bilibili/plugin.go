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
	}
}

type input struct {
	Keyword, BVID, AID, MID, Content string
	Page                             int
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
