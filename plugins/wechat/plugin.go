package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/heybox/omnimcp/plugin"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Plugin struct {
	mu     sync.Mutex
	tokens map[string]token
}
type token struct {
	Value   string
	Expires time.Time
}

func New() *Plugin { return &Plugin{tokens: map[string]token{}} }
func (*Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "wechat", Name: "微信公众号", Version: "0.1.0", APIVersion: "v1", Capabilities: []string{"official-api", "media", "draft"}}
}
func (*Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "get_access_token", Description: "获取公众号 AccessToken", InputSchema: plugin.ToolSchema(`{}`, nil)},
		{Name: "upload_image", Description: "上传正文图片并返回微信图片 URL", InputSchema: plugin.ToolSchema(`{"file_path":{"type":"string"}}`, []string{"file_path"})},
		{Name: "create_draft", Description: "创建微信公众号图文草稿", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"title":{"type":"string","maxLength":64},"content":{"type":"string"},"cover_path":{"type":"string"},"cover_media_id":{"type":"string"},"author":{"type":"string"},"digest":{"type":"string","maxLength":120},"content_source_url":{"type":"string"},"need_open_comment":{"type":"boolean"},"only_fans_can_comment":{"type":"boolean"}}`, []string{"title", "content"})},
		{Name: "list_drafts", Description: "分页获取公众号草稿", InputSchema: plugin.ToolSchema(`{"offset":{"type":"integer"},"count":{"type":"integer","minimum":1,"maximum":20}}`, nil)},
		{Name: "get_draft", Description: "获取公众号草稿详情", InputSchema: plugin.ToolSchema(`{"media_id":{"type":"string"}}`, []string{"media_id"})},
		{Name: "delete_draft", Description: "删除公众号草稿", Mutating: true, Confirm: true, InputSchema: plugin.ToolSchema(`{"media_id":{"type":"string"}}`, []string{"media_id"})},
	}
}

type input struct {
	FilePath, Title, Content, CoverPath, CoverMediaID, Author, Digest, ContentSourceURL, MediaID string
	Offset, Count                                                                                int
	NeedOpenComment, OnlyFansCanComment                                                          bool
}

func (p *Plugin) Execute(ctx context.Context, c *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	switch name {
	case "get_access_token":
		return p.access(ctx, c)
	case "upload_image":
		tok, err := p.access(ctx, c)
		if err != nil {
			return nil, err
		}
		return uploadImage(ctx, tok.(map[string]any)["access_token"].(string), in.FilePath)
	case "create_draft":
		return p.createDraft(ctx, c, in)
	case "list_drafts":
		tok, err := p.access(ctx, c)
		if err != nil {
			return nil, err
		}
		return p.api(ctx, "POST", "https://api.weixin.qq.com/cgi-bin/draft/batchget?access_token="+tok.(map[string]any)["access_token"].(string), map[string]any{"offset": in.Offset, "count": in.Count})
	case "get_draft":
		tok, err := p.access(ctx, c)
		if err != nil {
			return nil, err
		}
		return p.api(ctx, "POST", "https://api.weixin.qq.com/cgi-bin/draft/get?access_token="+tok.(map[string]any)["access_token"].(string), map[string]any{"media_id": in.MediaID})
	case "delete_draft":
		tok, err := p.access(ctx, c)
		if err != nil {
			return nil, err
		}
		return p.api(ctx, "POST", "https://api.weixin.qq.com/cgi-bin/draft/delete?access_token="+tok.(map[string]any)["access_token"].(string), map[string]any{"media_id": in.MediaID})
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}
func (p *Plugin) creds(c *plugin.Context) (string, string, error) {
	key := strings.ToUpper(strings.NewReplacer("-", "_", "/", "_", " ", "_").Replace(c.Account.AccountID))
	id := os.Getenv("OMNIMCP_WECHAT_" + key + "_APP_ID")
	secret := os.Getenv("OMNIMCP_WECHAT_" + key + "_APP_SECRET")
	if id == "" {
		id = os.Getenv("WECHAT_APP_ID")
	}
	if secret == "" {
		secret = os.Getenv("WECHAT_APP_SECRET")
	}
	if id == "" || secret == "" {
		return "", "", fmt.Errorf("missing WeChat credentials: set OMNIMCP_WECHAT_%s_APP_ID and _APP_SECRET", key)
	}
	return id, secret, nil
}
func (p *Plugin) access(ctx context.Context, c *plugin.Context) (any, error) {
	id, secret, err := p.creds(c)
	if err != nil {
		return nil, err
	}
	key := c.Account.AccountID
	p.mu.Lock()
	if t, ok := p.tokens[key]; ok && time.Until(t.Expires) > time.Minute {
		p.mu.Unlock()
		return map[string]any{"access_token": t.Value, "expires_in": int(time.Until(t.Expires).Seconds())}, nil
	}
	p.mu.Unlock()
	u := "https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=" + url.QueryEscape(id) + "&secret=" + url.QueryEscape(secret)
	var out map[string]any
	if err := requestJSON(ctx, "GET", u, nil, &out); err != nil {
		return nil, err
	}
	if n, ok := out["errcode"]; ok && n != nil {
		return nil, fmt.Errorf("wechat token: %v", out)
	}
	v, _ := out["access_token"].(string)
	secs, _ := out["expires_in"].(float64)
	if v == "" {
		return nil, fmt.Errorf("wechat token response missing access_token")
	}
	p.mu.Lock()
	p.tokens[key] = token{v, time.Now().Add(time.Duration(secs) * time.Second)}
	p.mu.Unlock()
	return out, nil
}
func (p *Plugin) createDraft(ctx context.Context, c *plugin.Context, in input) (any, error) {
	tok, err := p.access(ctx, c)
	if err != nil {
		return nil, err
	}
	access := tok.(map[string]any)["access_token"].(string)
	cover := in.CoverMediaID
	if cover == "" && in.CoverPath != "" {
		cover, err = uploadThumb(ctx, access, in.CoverPath)
		if err != nil {
			return nil, err
		}
	}
	if cover == "" {
		return nil, fmt.Errorf("cover_path or cover_media_id is required")
	}
	article := map[string]any{"title": in.Title, "author": in.Author, "digest": in.Digest, "content": in.Content, "content_source_url": in.ContentSourceURL, "thumb_media_id": cover, "need_open_comment": in.NeedOpenComment, "only_fans_can_comment": in.OnlyFansCanComment}
	return p.api(ctx, "POST", "https://api.weixin.qq.com/cgi-bin/draft/add?access_token="+access, map[string]any{"articles": []any{article}})
}
func (p *Plugin) api(ctx context.Context, method, u string, body any) (any, error) {
	var out map[string]any
	if err := requestJSON(ctx, method, u, body, &out); err != nil {
		return nil, err
	}
	if n, ok := out["errcode"]; ok && n != float64(0) {
		return nil, fmt.Errorf("wechat api: %v", out)
	}
	return out, nil
}
func requestJSON(ctx context.Context, method, u string, body any, out *map[string]any) error {
	var r *http.Request
	var err error
	if body != nil {
		b, _ := json.Marshal(body)
		r, err = http.NewRequestWithContext(ctx, method, u, bytes.NewReader(b))
		if err == nil {
			r.Header.Set("Content-Type", "application/json")
		}
	} else {
		r, err = http.NewRequestWithContext(ctx, method, u, nil)
	}
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("http %s: %s", resp.Status, u)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
func uploadImage(ctx context.Context, access, path string) (any, error) {
	return uploadMedia(ctx, access, path, "https://api.weixin.qq.com/cgi-bin/media/uploadimg")
}
func uploadThumb(ctx context.Context, access, path string) (string, error) {
	v, e := uploadMedia(ctx, access, path, "https://api.weixin.qq.com/cgi-bin/material/add_material?type=thumb")
	if e != nil {
		return "", e
	}
	m := v.(map[string]any)
	s, _ := m["media_id"].(string)
	return s, nil
}
func uploadMedia(ctx context.Context, access, path, endpoint string) (any, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, err := w.CreateFormFile("media", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if _, err = io.Copy(part, f); err != nil {
		return nil, err
	}
	w.Close()
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+sep+"access_token="+url.QueryEscape(access), &b)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out["errcode"] != nil {
		return nil, fmt.Errorf("wechat media api: %v", out)
	}
	return out, nil
}
