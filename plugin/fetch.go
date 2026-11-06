package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrNotLoggedIn reports that the platform refused the browser's cookies.
var ErrNotLoggedIn = errors.New("平台账号未登录或登录已失效")

// FetchClient is replaceable so plugins can be tested without a network.
var FetchClient = &http.Client{Timeout: 30 * time.Second}

// FetchJSON reads a GET endpoint with one site's browser cookies. It suits read
// endpoints because it leaves the person's tab where it is. Never use it for
// writes: those go through the page, where the result can be verified.
func FetchJSON(ctx context.Context, c *Context, domain, url, referer string, out any) error {
	if c.Auth == nil {
		return fmt.Errorf("当前浏览器连接不支持读取登录状态")
	}
	cookie, err := c.Auth.CookieHeader(ctx, domain)
	if err != nil {
		return fmt.Errorf("读取 %s 登录状态失败: %w", domain, err)
	}
	if cookie == "" {
		return ErrNotLoggedIn
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Referer", referer)
	req.Header.Set("User-Agent", "Mozilla/5.0 OmniMCP/0.1")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	res, err := FetchClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return ErrNotLoggedIn
	}
	if res.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%s 拒绝了请求（HTTP 403），账号可能被限制访问；请暂停读取，到平台确认状态", domain)
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s 返回 HTTP %d", domain, res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s 返回的内容无法解析: %w", domain, err)
	}
	return nil
}
