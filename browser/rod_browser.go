package browser

import (
	"context"
	"fmt"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RodBrowser is the shared Chromium implementation. Profiles are isolated per account.
type RodBrowser struct {
	browser *rod.Browser
	page    *rod.Page
}

func (b *RodBrowser) Page() *rod.Page { return b.page }

func (b *RodBrowser) OpenLogin(ctx context.Context, url string) error { return b.Open(ctx, url) }

// CookieHeader returns cookies currently stored in this account's Chromium
// profile. It is used by API plugins that need a browser-authenticated session.
func (b *RodBrowser) CookieHeader(ctx context.Context, domain string) (string, error) {
	cookies, err := b.browser.GetCookies()
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if domain != "" && !strings.Contains(c.Domain, domain) {
			continue
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; "), nil
}

func NewRodBrowser(headless bool, dataDir, account string) (*RodBrowser, error) {
	if dataDir == "" {
		dataDir = "data"
	}
	profile := filepath.Join(dataDir, "profiles", account)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, err
	}
	u, err := launcher.New().UserDataDir(profile).Headless(headless).Leakless(false).Launch()
	if err != nil {
		return nil, fmt.Errorf("launch chromium: %w", err)
	}
	b := rod.New().ControlURL(u)
	if err := b.Connect(); err != nil {
		return nil, err
	}
	return &RodBrowser{browser: b}, nil
}
func (b *RodBrowser) Open(ctx context.Context, url string) error {
	p, err := b.browser.Page(proto.TargetCreateTarget{URL: url})
	if err != nil {
		return err
	}
	b.page = p.Context(ctx).Timeout(30 * time.Second)
	b.page.WaitLoad()
	return nil
}
func (b *RodBrowser) Snapshot(ctx context.Context) (string, error) {
	if b.page == nil {
		return "", fmt.Errorf("no page open")
	}
	text, err := b.page.Context(ctx).MustElement("body").Text()
	return text, err
}
func (b *RodBrowser) Click(ctx context.Context, selector string) error {
	if b.page == nil {
		return fmt.Errorf("no page open")
	}
	return b.page.Context(ctx).MustElement(selector).Click(proto.InputMouseButtonLeft, 1)
}
func (b *RodBrowser) Fill(ctx context.Context, selector, value string) error {
	if b.page == nil {
		return fmt.Errorf("no page open")
	}
	return b.page.Context(ctx).MustElement(selector).Input(value)
}
func (b *RodBrowser) Close() error {
	if b.browser == nil {
		return nil
	}
	return b.browser.Close()
}
