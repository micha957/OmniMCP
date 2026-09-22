package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/plugin"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "browser", Name: "通用浏览器", Version: "0.1.0", APIVersion: "v1", RequiresBrowser: true, BrowserMode: "extension", Capabilities: []string{"open", "snapshot", "click", "fill", "upload"}}
}
func (*Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "open", Description: "在用户浏览器中打开网页", InputSchema: plugin.ToolSchema(`{"url":{"type":"string","format":"uri"}}`, []string{"url"})},
		{Name: "snapshot", Description: "读取当前浏览器页面文本", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "click", Description: "点击当前页面元素", Mutating: true, InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"}}`, []string{"selector"})},
		{Name: "fill", Description: "填写当前页面输入框", Mutating: true, InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"},"value":{"type":"string"}}`, []string{"selector", "value"})},
		{Name: "upload", Description: "向当前页面文件输入框上传本地文件", Mutating: true, InputSchema: plugin.ToolSchema(`{"selector":{"type":"string"},"file_path":{"type":"string"}}`, []string{"selector", "file_path"})},
	}
}
func (*Plugin) Execute(ctx context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in struct{ URL, Selector, Value, FilePath string }
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	switch name {
	case "open":
		if err := c.Browser.Open(ctx, in.URL); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "url": in.URL}, nil
	case "snapshot":
		v, e := c.Browser.Snapshot(ctx)
		return map[string]any{"text": v}, e
	case "click":
		return map[string]any{"ok": true}, c.Browser.Click(ctx, in.Selector)
	case "fill":
		return map[string]any{"ok": true}, c.Browser.Fill(ctx, in.Selector, in.Value)
	case "upload":
		u, ok := c.Browser.(plugin.Uploader)
		if !ok {
			return nil, fmt.Errorf("current browser connector does not support upload")
		}
		return map[string]any{"ok": true}, u.Upload(ctx, in.Selector, in.FilePath)
	default:
		return nil, fmt.Errorf("unknown browser tool %q", name)
	}
}
