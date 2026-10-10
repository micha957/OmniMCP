package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/plugin"
	"time"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "browser", Name: "通用浏览器", Version: "0.2.0", APIVersion: "v1", RequiresBrowser: true, BrowserMode: "extension", Capabilities: []string{"open", "snapshot", "query", "wait", "click", "type", "upload", "state"}}
}
func (*Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "open", Description: "在用户浏览器中打开网页", InputSchema: plugin.ToolSchema(`{"url":{"type":"string","format":"uri"}}`, []string{"url"})},
		{Name: "snapshot", Description: "读取当前页面文本、链接、输入框和按钮", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "url", Description: "读取当前页面地址", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "query", Description: "查询 CSS 选择器匹配的元素：数量、可见性、文本和属性", InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"}}`, []string{"selector"})},
		{Name: "wait_for", Description: "等待元素出现，或等待页面（或元素内）出现指定文本", InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"},"text":{"type":"string"},"timeout_ms":{"type":"integer","minimum":100,"maximum":120000}}`, nil)},
		{Name: "scroll", Description: "把元素滚动到可视区域；不传 selector 时滚到页面底部", InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"}}`, nil)},
		{Name: "read_state", Description: "读取页面全局 JSON 状态，例如 __INITIAL_STATE__.note", InputSchema: plugin.ToolSchema(`{"path":{"type":"string"}}`, []string{"path"})},
		{Name: "click", Description: "用真实鼠标事件点击元素；text 用于在多个匹配中按文本挑选", Mutating: true, InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"},"text":{"type":"string"}}`, []string{"selector"})},
		{Name: "fill", Description: "用真实键盘输入替换输入框内容", Mutating: true, InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"},"value":{"type":"string"}}`, []string{"selector", "value"})},
		{Name: "press_key", Description: "向当前焦点元素按键", Mutating: true, InputSchema: plugin.ToolSchema(`{"key":{"type":"string","enum":["Enter","Escape","Tab","Backspace"]}}`, []string{"key"})},
		{Name: "upload", Description: "向当前页面文件输入框上传本地文件", Mutating: true, InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"},"file_path":{"type":"string"}}`, []string{"selector", "file_path"})},
	}
}

type input struct {
	URL       string `json:"url"`
	Selector  string `json:"selector"`
	Text      string `json:"text"`
	Value     string `json:"value"`
	Key       string `json:"key"`
	Path      string `json:"path"`
	FilePath  string `json:"file_path"`
	TimeoutMS int    `json:"timeout_ms"`
}

func (*Plugin) Execute(ctx context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	b := c.Browser
	inspector, _ := b.(plugin.Inspector)
	trusted, _ := b.(plugin.TrustedInput)
	need := func(ok bool) error {
		if !ok {
			return fmt.Errorf("current browser connector does not support %s; reload the OmniMCP extension", name)
		}
		return nil
	}
	switch name {
	case "open":
		if err := b.Open(ctx, in.URL); err != nil {
			return nil, err
		}
		if inspector != nil {
			u, _ := inspector.CurrentURL(ctx)
			return map[string]any{"ok": true, "url": u}, nil
		}
		return map[string]any{"ok": true, "url": in.URL}, nil
	case "snapshot":
		v, e := b.Snapshot(ctx)
		return map[string]any{"text": v}, e
	case "url":
		if err := need(inspector != nil); err != nil {
			return nil, err
		}
		u, err := inspector.CurrentURL(ctx)
		return map[string]any{"url": u}, err
	case "query":
		if err := need(inspector != nil); err != nil {
			return nil, err
		}
		return inspector.Query(ctx, in.Selector)
	case "wait_for":
		if err := need(inspector != nil); err != nil {
			return nil, err
		}
		if in.Selector == "" && in.Text == "" {
			return nil, fmt.Errorf("selector or text is required")
		}
		timeout := 10 * time.Second
		if in.TimeoutMS > 0 {
			timeout = time.Duration(in.TimeoutMS) * time.Millisecond
		}
		if err := inspector.WaitFor(ctx, in.Selector, in.Text, timeout); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "scroll":
		if err := need(inspector != nil); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, inspector.Scroll(ctx, in.Selector)
	case "read_state":
		sr, ok := b.(plugin.StateReader)
		if err := need(ok); err != nil {
			return nil, err
		}
		return sr.ReadGlobal(ctx, in.Path)
	case "click":
		if trusted == nil {
			return map[string]any{"ok": true}, b.Click(ctx, in.Selector)
		}
		return map[string]any{"ok": true}, trusted.ClickElement(ctx, in.Selector, plugin.ClickOptions{Text: in.Text})
	case "fill":
		if trusted == nil {
			return map[string]any{"ok": true}, b.Fill(ctx, in.Selector, in.Value)
		}
		return map[string]any{"ok": true}, trusted.TypeText(ctx, in.Selector, in.Value)
	case "press_key":
		if err := need(trusted != nil); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, trusted.PressKey(ctx, in.Key)
	case "upload":
		u, ok := b.(plugin.Uploader)
		if err := need(ok); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, u.Upload(ctx, in.Selector, in.FilePath)
	default:
		return nil, fmt.Errorf("unknown browser tool %q", name)
	}
}
