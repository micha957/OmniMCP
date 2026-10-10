package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/heybox/omnimcp/plugin"
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

// Open navigates the account's single working tab, creating it on first use,
// so repeated calls do not accumulate tabs.
func (b *RodBrowser) Open(ctx context.Context, url string) error {
	if b.page == nil {
		p, err := b.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
		if err != nil {
			return err
		}
		b.page = p
	}
	p := b.page.Context(ctx).Timeout(30 * time.Second)
	if err := p.Navigate(url); err != nil {
		return err
	}
	return p.WaitLoad()
}
func (b *RodBrowser) current(ctx context.Context) (*rod.Page, error) {
	if b.page == nil {
		return nil, fmt.Errorf("no page open")
	}
	return b.page.Context(ctx), nil
}
func (b *RodBrowser) element(ctx context.Context, selector string) (*rod.Element, error) {
	p, err := b.current(ctx)
	if err != nil {
		return nil, err
	}
	el, err := p.Timeout(10 * time.Second).Element(selector)
	if err != nil {
		return nil, fmt.Errorf("selector not found: %s: %w", selector, err)
	}
	return el.CancelTimeout(), nil
}
func (b *RodBrowser) Snapshot(ctx context.Context) (string, error) {
	el, err := b.element(ctx, "body")
	if err != nil {
		return "", err
	}
	return el.Text()
}
func (b *RodBrowser) Click(ctx context.Context, selector string) error {
	el, err := b.element(ctx, selector)
	if err != nil {
		return err
	}
	return el.Click(proto.InputMouseButtonLeft, 1)
}
func (b *RodBrowser) Fill(ctx context.Context, selector, value string) error {
	el, err := b.element(ctx, selector)
	if err != nil {
		return err
	}
	return el.Input(value)
}
func (b *RodBrowser) Upload(ctx context.Context, selector, path string) error {
	el, err := b.element(ctx, selector)
	if err != nil {
		return err
	}
	return el.SetFiles([]string{path})
}

// queryScript mirrors the extension's content.js query command.
const queryScript = `(selector) => {
	const nodes = document.querySelectorAll(selector);
	const node = nodes[0];
	if (!node) return {exists: false, count: 0, visible: false, text: ""};
	const style = getComputedStyle(node), rect = node.getBoundingClientRect();
	const attrs = {};
	for (const a of node.attributes) attrs[a.name] = String(a.value).slice(0, 500);
	return {exists: true, count: nodes.length, visible: style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0,
		text: (node.innerText || node.value || "").trim().slice(0, 2000), attrs};
}`

func (b *RodBrowser) Query(ctx context.Context, selector string) (plugin.ElementInfo, error) {
	var info plugin.ElementInfo
	p, err := b.current(ctx)
	if err != nil {
		return info, err
	}
	res, err := p.Eval(queryScript, selector)
	if err != nil {
		return info, err
	}
	err = res.Value.Unmarshal(&info)
	return info, err
}
func (b *RodBrowser) WaitFor(ctx context.Context, selector, text string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := b.matches(ctx, selector, text)
		if err == nil && ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait_for timed out: selector=%q text=%q", selector, text)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}
func (b *RodBrowser) matches(ctx context.Context, selector, text string) (bool, error) {
	if selector == "" {
		body, err := b.Snapshot(ctx)
		return strings.Contains(body, text), err
	}
	info, err := b.Query(ctx, selector)
	if err != nil || !info.Exists {
		return false, err
	}
	return text == "" || strings.Contains(info.Text, text), nil
}
func (b *RodBrowser) Scroll(ctx context.Context, selector string) error {
	p, err := b.current(ctx)
	if err != nil {
		return err
	}
	if selector == "" {
		_, err = p.Eval(`() => window.scrollTo(0, document.documentElement.scrollHeight)`)
		return err
	}
	el, err := b.element(ctx, selector)
	if err != nil {
		return err
	}
	return el.ScrollIntoView()
}
func (b *RodBrowser) CurrentURL(ctx context.Context) (string, error) {
	p, err := b.current(ctx)
	if err != nil {
		return "", err
	}
	info, err := p.Info()
	if err != nil {
		return "", err
	}
	return info.URL, nil
}

// readGlobalScript walks a dotted path from window, unwrapping Vue refs.
const readGlobalScript = `(path) => {
	const unwrap = v => (v && typeof v === "object" && ("_value" in v || "value" in v) && Object.keys(v).some(k => k === "__v_isRef" || k === "_rawValue" || k === "dep")) ? (v._value !== undefined ? v._value : v.value) : v;
	let cur = window;
	for (const key of path.split(".")) {
		cur = unwrap(cur);
		if (cur == null) return null;
		cur = cur[key];
	}
	cur = unwrap(cur);
	if (cur === undefined) return null;
	const seen = new WeakSet();
	return JSON.stringify(cur, (k, v) => {
		v = unwrap(v);
		if (v && typeof v === "object") { if (seen.has(v)) return undefined; seen.add(v); }
		return v;
	});
}`

func (b *RodBrowser) ReadGlobal(ctx context.Context, path string) (json.RawMessage, error) {
	p, err := b.current(ctx)
	if err != nil {
		return nil, err
	}
	res, err := p.Eval(readGlobalScript, path)
	if err != nil {
		return nil, err
	}
	s := res.Value.Str()
	if s == "" {
		return nil, fmt.Errorf("page global %s is not available", path)
	}
	return json.RawMessage(s), nil
}

func (b *RodBrowser) ClickElement(ctx context.Context, selector string, opt plugin.ClickOptions) error {
	p, err := b.current(ctx)
	if err != nil {
		return err
	}
	var target *rod.Element
	deadline := time.Now().Add(10 * time.Second)
	for target == nil {
		els, err := p.Elements(selector)
		if err != nil {
			return err
		}
		for _, el := range els {
			if v, _ := el.Visible(); !v {
				continue
			}
			if opt.Text != "" {
				t, _ := el.Text()
				t = strings.TrimSpace(t)
				if (opt.Exact && t != opt.Text) || !strings.Contains(t, opt.Text) {
					continue
				}
			}
			if dis, _ := el.Eval(`() => this.disabled || this.getAttribute("aria-disabled") === "true" || !!this.closest("[disabled],[aria-disabled='true']")`); dis != nil && dis.Value.Bool() {
				continue
			}
			target = el
			break
		}
		if target == nil {
			if time.Now().After(deadline) {
				return fmt.Errorf("selector not found: %s text=%q", selector, opt.Text)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	if opt.XRatio <= 0 {
		return target.Click(proto.InputMouseButtonLeft, 1)
	}
	if err := target.ScrollIntoView(); err != nil {
		return err
	}
	shape, err := target.Shape()
	if err != nil || len(shape.Quads) == 0 {
		return fmt.Errorf("element has no clickable box: %s", selector)
	}
	box := shape.Box()
	if err := p.Mouse.MoveTo(proto.Point{X: box.X + box.Width*opt.XRatio, Y: box.Y + box.Height/2}); err != nil {
		return err
	}
	return p.Mouse.Click(proto.InputMouseButtonLeft, 1)
}
func (b *RodBrowser) TypeText(ctx context.Context, selector, value string) error {
	el, err := b.element(ctx, selector)
	if err != nil {
		return err
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	return el.Page().InsertText(value)
}

func (b *RodBrowser) PressKey(ctx context.Context, key string) error {
	p, err := b.current(ctx)
	if err != nil {
		return err
	}
	k, ok := map[string]input.Key{"Enter": input.Enter, "Escape": input.Escape, "Tab": input.Tab, "Backspace": input.Backspace}[key]
	if !ok {
		return fmt.Errorf("unsupported key %q", key)
	}
	return p.Keyboard.Type(k)
}

// extractScript mirrors the extension's extract_list command.
const extractScript = `(spec) => {
	const value = (root, f) => {
		const el = f.selector ? root.querySelector(f.selector) : root;
		if (!el) return "";
		const attr = f.attr || "text";
		if (attr === "text") return (el.innerText || el.textContent || "").trim();
		if (attr === "href") return el.href || el.getAttribute("href") || "";
		if (attr === "src") return el.currentSrc || el.src || el.getAttribute("src") || "";
		return el.getAttribute(attr) || "";
	};
	const limit = spec.limit > 0 ? spec.limit : 50;
	return [...document.querySelectorAll(spec.item)].slice(0, limit).map(n => {
		const out = {};
		for (const [k, f] of Object.entries(spec.fields || {})) out[k] = value(n, f);
		return out;
	});
}`

func (b *RodBrowser) ExtractList(ctx context.Context, spec plugin.ExtractSpec) ([]map[string]string, error) {
	p, err := b.current(ctx)
	if err != nil {
		return nil, err
	}
	res, err := p.Eval(extractScript, spec)
	if err != nil {
		return nil, err
	}
	var out []map[string]string
	err = res.Value.Unmarshal(&out)
	return out, err
}
func (b *RodBrowser) ReadScriptJSON(ctx context.Context, selector string) (json.RawMessage, error) {
	el, err := b.element(ctx, selector)
	if err != nil {
		return nil, err
	}
	text, err := el.Eval(`() => this.textContent || ""`)
	if err != nil {
		return nil, err
	}
	raw := []byte(text.Value.Str())
	if !json.Valid(raw) {
		return nil, fmt.Errorf("%s does not contain valid JSON", selector)
	}
	return raw, nil
}

var _ plugin.Extractor = (*RodBrowser)(nil)
var _ plugin.TrustedInput = (*RodBrowser)(nil)
var _ plugin.Inspector = (*RodBrowser)(nil)
var _ plugin.StateReader = (*RodBrowser)(nil)
var _ plugin.Uploader = (*RodBrowser)(nil)

func (b *RodBrowser) Close() error {
	if b.browser == nil {
		return nil
	}
	return b.browser.Close()
}
