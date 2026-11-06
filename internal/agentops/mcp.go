package agentops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Reader interface {
	Call(context.Context, string, map[string]any) (json.RawMessage, error)
}

// MCPReader exposes the read operations listed in readTools and the write
// proposals listed in writeTools, which only queue for human confirmation.
// Model output never becomes a tool name, account or arbitrary argument object.
type MCPReader struct {
	URL   string
	Token string
	// ApprovalToken opens the confirmation API when OMNIMCP_APPROVAL_TOKEN is set.
	ApprovalToken string
}

var readTools = map[string]bool{
	"xiaohongshu_search_notes": true, "xiaohongshu_get_note_detail": true, "xiaohongshu_check_login": true,
	"xiaohongshu_get_notifications": true, "xiaohongshu_get_my_account": true, "xiaohongshu_get_user_profile": true,
	"bilibili_check_login": true, "bilibili_get_notifications": true, "bilibili_get_my_account": true,
	"zhihu_check_login": true, "zhihu_get_notifications": true, "zhihu_get_my_account": true,
	"douyin_check_login": true, "douyin_get_my_account": true,
	"youtube_check_login": true, "tiktok_check_login": true,
	"omnimcp_approval_status": true,
}

func (m *MCPReader) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if !readTools[name] {
		return nil, fmt.Errorf("工作台不支持该平台操作")
	}
	envelope, err := m.rpc(ctx, name, args)
	if err != nil {
		return nil, err
	}
	if envelope.Result.IsError {
		for _, c := range envelope.Result.Content {
			if c.Type == "text" && c.Text != "" {
				return nil, fmt.Errorf("平台读取未成功：%s", truncate(c.Text, 300))
			}
		}
		return nil, fmt.Errorf("平台读取未成功，请检查登录状态、账号连接和页面")
	}
	if json.Valid(envelope.Result.Structured) {
		return envelope.Result.Structured, nil
	}
	for _, c := range envelope.Result.Content {
		if c.Type == "text" && json.Valid([]byte(c.Text)) {
			return json.RawMessage(c.Text), nil
		}
	}
	return nil, fmt.Errorf("平台未返回结构化结果")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Propose queues a write. A tool outside writeTools is refused here, before the
// request leaves this process.
func (m *MCPReader) Propose(ctx context.Context, tool string, args map[string]any, account, note string) (Proposal, error) {
	if !writeTools[tool] {
		return Proposal{}, fmt.Errorf("工作台不能提交 %s", tool)
	}
	call := map[string]any{"_account_id": account, "_note": note}
	for k, v := range args {
		call[k] = v
	}
	envelope, err := m.rpc(ctx, tool, call)
	if err != nil {
		return Proposal{}, err
	}
	var out struct {
		ApprovalID     string          `json:"approval_id"`
		ApprovalStatus string          `json:"approval_status"`
		Hash           string          `json:"hash"`
		Error          string          `json:"error"`
		Reason         string          `json:"reason"`
		Result         json.RawMessage `json:"result"`
	}
	if json.Unmarshal(envelope.Result.Structured, &out) != nil || out.ApprovalID == "" {
		for _, c := range envelope.Result.Content {
			if c.Type == "text" && c.Text != "" {
				return Proposal{}, fmt.Errorf("未能进入确认队列：%s", truncate(c.Text, 300))
			}
		}
		return Proposal{}, fmt.Errorf("未能进入确认队列")
	}
	return Proposal{ID: out.ApprovalID, Hash: out.Hash, Status: out.ApprovalStatus, Error: out.Error, Reason: out.Reason, Result: out.Result}, nil
}

// ApprovalsURL is the full confirmation page on the OmniMCP server.
func (m *MCPReader) ApprovalsURL() string { return strings.TrimSuffix(m.URL, "/mcp") + "/approvals" }

// Decider relays a decision the user made on this workbench to the queue.
type Decider interface {
	Decide(ctx context.Context, id, action string, body map[string]any) error
}

// Decide posts to OmniMCP's confirmation API. Only the workbench's own HTTP
// handlers call this; nothing in the model pipeline can reach it.
func (m *MCPReader) Decide(ctx context.Context, id, action string, body map[string]any) error {
	if action != "approve" && action != "reject" && action != "retry" {
		return fmt.Errorf("未知的确认操作")
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(m.URL, "/mcp")+"/api/approvals/"+url.PathEscape(id)+"/"+action, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("OmniMCP 地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	if m.ApprovalToken != "" {
		req.Header.Set("Authorization", "Bearer "+m.ApprovalToken)
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 OmniMCP")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var out struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&out)
		if out.Error == "" {
			out.Error = fmt.Sprintf("OmniMCP 返回 HTTP %d", res.StatusCode)
		}
		return fmt.Errorf("%s", out.Error)
	}
	return nil
}

type rpcEnvelope struct {
	Error  json.RawMessage `json:"error"`
	Result struct {
		IsError    bool            `json:"isError"`
		Structured json.RawMessage `json:"structuredContent"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

func (m *MCPReader) rpc(ctx context.Context, name string, args map[string]any) (*rpcEnvelope, error) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "agentops", "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	req, err := http.NewRequestWithContext(ctx, "POST", m.URL, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("MCP 地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	if m.Token != "" {
		req.Header.Set("Authorization", "Bearer "+m.Token)
	}
	client := &http.Client{Timeout: 150 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接 OmniMCP，请检查服务和浏览器连接")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OmniMCP 返回 HTTP %d", res.StatusCode)
	}
	b, err = io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(b) > 4<<20 {
		return nil, fmt.Errorf("平台结果读取失败或过大")
	}
	var envelope rpcEnvelope
	if json.Unmarshal(b, &envelope) != nil {
		return nil, fmt.Errorf("OmniMCP 返回了无效响应")
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		return nil, fmt.Errorf("OmniMCP 拒绝了请求：%s", truncate(string(envelope.Error), 200))
	}
	return &envelope, nil
}
