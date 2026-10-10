// Package plugintest provides a scriptable fake browser connector for
// testing page-driven plugins without Chrome.
package plugintest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/heybox/omnimcp/plugin"
)

// Page implements every connector interface from in-memory state. Tests set
// the maps up front and may mutate state from OnClick to model page reactions.
// It is not safe for concurrent use.
type Page struct {
	URL      string
	Text     string                        // body text for Snapshot / WaitFor("", text)
	Globals  map[string]string             // ReadGlobal path -> JSON
	Scripts  map[string]string             // ReadScriptJSON selector -> JSON
	Elements map[string]plugin.ElementInfo // Query selector -> result
	Lists    map[string][]map[string]string
	OnClick  func(p *Page, selector string, opt plugin.ClickOptions) error
	Clicks   []string
	Typed    map[string]string
	Keys     []string
	Uploads  []string
	Opened   []string
}

func New() *Page {
	return &Page{Globals: map[string]string{}, Scripts: map[string]string{}, Elements: map[string]plugin.ElementInfo{},
		Lists: map[string][]map[string]string{}, Typed: map[string]string{}}
}

// SetElement updates a Query result; safe to call from OnClick.
func (p *Page) SetElement(selector string, info plugin.ElementInfo) { p.Elements[selector] = info }

func (p *Page) Open(_ context.Context, url string) error {
	p.URL = url
	p.Opened = append(p.Opened, url)
	return nil
}
func (p *Page) Snapshot(context.Context) (string, error) { return p.Text, nil }
func (p *Page) Click(ctx context.Context, sel string) error {
	return p.ClickElement(ctx, sel, plugin.ClickOptions{})
}
func (p *Page) Fill(ctx context.Context, sel, v string) error { return p.TypeText(ctx, sel, v) }

func (p *Page) Query(_ context.Context, sel string) (plugin.ElementInfo, error) {
	return p.Elements[sel], nil
}
func (p *Page) WaitFor(ctx context.Context, sel, text string, _ time.Duration) error {
	if sel == "" {
		if strings.Contains(p.Text, text) {
			return nil
		}
		return fmt.Errorf("text %q not on page", text)
	}
	info, _ := p.Query(ctx, sel)
	if info.Exists && strings.Contains(info.Text, text) {
		return nil
	}
	return fmt.Errorf("wait_for %s timed out", sel)
}
func (p *Page) Scroll(context.Context, string) error { return nil }
func (p *Page) CurrentURL(context.Context) (string, error) {
	return p.URL, nil
}
func (p *Page) ReadGlobal(_ context.Context, path string) (json.RawMessage, error) {
	if v, ok := p.Globals[path]; ok {
		return json.RawMessage(v), nil
	}
	return nil, fmt.Errorf("page global %s is not available", path)
}
func (p *Page) ReadScriptJSON(_ context.Context, sel string) (json.RawMessage, error) {
	if v, ok := p.Scripts[sel]; ok {
		return json.RawMessage(v), nil
	}
	return nil, fmt.Errorf("selector not found: %s", sel)
}
func (p *Page) ExtractList(_ context.Context, spec plugin.ExtractSpec) ([]map[string]string, error) {
	rows := p.Lists[spec.Item]
	if spec.Limit > 0 && len(rows) > spec.Limit {
		rows = rows[:spec.Limit]
	}
	return rows, nil
}
func (p *Page) ClickElement(_ context.Context, sel string, opt plugin.ClickOptions) error {
	p.Clicks = append(p.Clicks, sel)
	if p.OnClick != nil {
		return p.OnClick(p, sel, opt)
	}
	return nil
}
func (p *Page) TypeText(_ context.Context, sel, v string) error { p.Typed[sel] = v; return nil }
func (p *Page) PressKey(_ context.Context, k string) error      { p.Keys = append(p.Keys, k); return nil }
func (p *Page) Upload(_ context.Context, _, path string) error {
	p.Uploads = append(p.Uploads, path)
	return nil
}

var _ plugin.Browser = (*Page)(nil)
var _ plugin.Inspector = (*Page)(nil)
var _ plugin.StateReader = (*Page)(nil)
var _ plugin.TrustedInput = (*Page)(nil)
var _ plugin.Uploader = (*Page)(nil)
var _ plugin.Extractor = (*Page)(nil)
