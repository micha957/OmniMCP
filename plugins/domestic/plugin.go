package domestic

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/plugin"
	"net/url"
	"strings"
)

type Plugin struct {
	id, name, host, searchTemplate, detailTemplate string
	likeSelectors, commentSelectors                []string
}

func New(id, name, host, searchTemplate, detailTemplate string, likeSelectors, commentSelectors []string) *Plugin {
	return &Plugin{id: id, name: name, host: host, searchTemplate: searchTemplate, detailTemplate: detailTemplate, likeSelectors: likeSelectors, commentSelectors: commentSelectors}
}
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: p.id, Name: p.name, Version: "0.1.0", APIVersion: "v1", RequiresBrowser: true, BrowserMode: "extension", LoginURL: "https://" + p.host, Capabilities: []string{"login", "search", "read", "like", "comment"}}
}
func (p *Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "search", Description: "搜索" + p.name + "内容", InputSchema: plugin.ToolSchema(`{"keyword":{"type":"string"}}`, []string{"keyword"})},
		{Name: "open", Description: "打开" + p.name + "网页", InputSchema: plugin.ToolSchema(`{"url":{"type":"string","format":"uri"}}`, []string{"url"})},
		{Name: "snapshot", Description: "读取当前" + p.name + "页面", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "get_detail", Description: "读取" + p.name + "内容详情", InputSchema: plugin.ToolSchema(`{"id":{"type":"string"}}`, []string{"id"})},
		{Name: "like", Description: "点赞" + p.name + "内容", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"id":{"type":"string"}}`, []string{"id"})},
		{Name: "comment", Description: "评论" + p.name + "内容", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"id":{"type":"string"},"content":{"type":"string"}}`, []string{"id", "content"})},
	}
}
func (p *Plugin) Execute(ctx context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in struct{ Keyword, URL, ID, Content string }
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	switch name {
	case "search":
		if strings.TrimSpace(in.Keyword) == "" {
			return nil, fmt.Errorf("keyword is required")
		}
		target := fmt.Sprintf(p.searchTemplate, url.QueryEscape(in.Keyword))
		if err := c.Browser.Open(ctx, target); err != nil {
			return nil, err
		}
		text, err := c.Browser.Snapshot(ctx)
		return map[string]any{"keyword": in.Keyword, "text": text, "source": "browser-extension"}, err
	case "open":
		if err := c.Browser.Open(ctx, in.URL); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "url": in.URL}, nil
	case "snapshot":
		text, err := c.Browser.Snapshot(ctx)
		return map[string]any{"text": text, "source": "browser-extension"}, err
	case "get_detail":
		if in.ID == "" {
			return nil, fmt.Errorf("id is required")
		}
		if err := c.Browser.Open(ctx, fmt.Sprintf(p.detailTemplate, url.PathEscape(in.ID))); err != nil {
			return nil, err
		}
		text, err := c.Browser.Snapshot(ctx)
		return map[string]any{"id": in.ID, "text": text, "source": "browser-extension"}, err
	case "like":
		if in.ID == "" {
			return nil, fmt.Errorf("id is required")
		}
		if err := c.Browser.Open(ctx, fmt.Sprintf(p.detailTemplate, url.PathEscape(in.ID))); err != nil {
			return nil, err
		}
		if err := clickCandidates(ctx, c.Browser, p.likeSelectors); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "source": "browser-extension"}, nil
	case "comment":
		if in.ID == "" || strings.TrimSpace(in.Content) == "" {
			return nil, fmt.Errorf("id and content are required")
		}
		if err := c.Browser.Open(ctx, fmt.Sprintf(p.detailTemplate, url.PathEscape(in.ID))); err != nil {
			return nil, err
		}
		if err := fillCandidates(ctx, c.Browser, p.commentSelectors, in.Content); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "source": "browser-extension"}, nil
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}
func clickCandidates(ctx context.Context, b plugin.Browser, ss []string) error {
	var last error
	for _, s := range ss {
		if err := b.Click(ctx, s); err == nil {
			return nil
		} else {
			last = err
		}
	}
	return fmt.Errorf("找不到可点击元素: %v", last)
}
func fillCandidates(ctx context.Context, b plugin.Browser, ss []string, v string) error {
	var last error
	for _, s := range ss {
		if err := b.Fill(ctx, s, v); err == nil {
			return nil
		} else {
			last = err
		}
	}
	return fmt.Errorf("找不到输入框: %v", last)
}
