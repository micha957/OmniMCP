// Command omni is a thin CLI over a running OmniMCP server. Platform commands
// are generated from the server's tool schemas, so a new plugin needs no CLI code.
//
//	omni tools [platform]
//	omni <platform> <action> [--flag value ...] [--account id]
//	omni approvals list|show|approve|reject|retry
//	omni doctor
//
// Results are JSON on stdout. Exit codes: 0 ok, 1 tool failed, 2 usage,
// 3 write queued for confirmation (not executed), 4 server unreachable.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	exitOK = iota
	exitTool
	exitUsage
	exitApproval
	exitServer
)

const usage = `omni: 通过 OmniMCP 操作内容平台

用法：
  omni tools [平台]                     列出可用命令
  omni <平台> <动作> --help             查看命令参数
  omni <平台> <动作> [--参数 值] [--account 账号] [--note 依据]
  omni <平台> <动作> --json '{"keyword":"AI"}'
  omni approvals list [--status pending]
  omni approvals show <id>
  omni approvals approve <id>           显示内容并等待终端确认
  omni approvals reject <id> [--reason 原因]
  omni approvals retry <id>             核对平台后重新执行
  omni doctor                           检查服务和待确认数量

写操作不会立即执行：命令返回退出码 3 和 approval_id，确认后才会发到平台。
环境变量：OMNIMCP_URL（默认 http://127.0.0.1:8090）、OMNIMCP_API_TOKEN、OMNIMCP_APPROVAL_TOKEN
`

type cli struct {
	base          string
	token         string
	approvalToken string
	stdout        io.Writer
	stderr        io.Writer
	// confirm opens the person's terminal. It is a device rather than stdin so
	// that a script or model piping "y" into this process cannot answer it.
	confirm func() (io.ReadCloser, error)
	client  *http.Client
}

func main() {
	c := &cli{
		base:          strings.TrimRight(envOr("OMNIMCP_URL", "http://127.0.0.1:8090"), "/"),
		token:         os.Getenv("OMNIMCP_API_TOKEN"),
		approvalToken: os.Getenv("OMNIMCP_APPROVAL_TOKEN"),
		stdout:        os.Stdout,
		stderr:        os.Stderr,
		confirm:       func() (io.ReadCloser, error) { return os.Open("/dev/tty") },
		client:        &http.Client{Timeout: 35 * time.Minute},
	}
	os.Exit(c.run(os.Args[1:]))
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

var errServer = errors.New("无法连接 OmniMCP")

func (c *cli) fail(code int, format string, args ...any) int {
	fmt.Fprintf(c.stderr, "omni: "+format+"\n", args...)
	return code
}

func (c *cli) run(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(c.stdout, usage)
		return exitOK
	}
	switch args[0] {
	case "tools":
		return c.tools(args[1:])
	case "approvals":
		return c.approvals(args[1:])
	case "doctor":
		return c.doctor()
	}
	if len(args) < 2 {
		return c.fail(exitUsage, "缺少动作，用 omni tools %s 查看", args[0])
	}
	return c.callTool(args[0], args[1], args[2:])
}

type tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
			Enum        []any  `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	} `json:"inputSchema"`
	Annotations struct {
		ReadOnly bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

type rpcResult struct {
	Error  json.RawMessage `json:"error"`
	Result json.RawMessage `json:"result"`
}

func (c *cli) rpc(method string, params any) (*rpcResult, error) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequest("POST", c.base+"/mcp", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w（%s）；请先运行 go run ./cmd/omnimcp", errServer, c.base)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("%w：认证失败，请设置 OMNIMCP_API_TOKEN", errServer)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w：HTTP %d", errServer, res.StatusCode)
	}
	var out rpcResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w：响应无效", errServer)
	}
	return &out, nil
}

func (c *cli) listTools() ([]tool, error) {
	res, err := c.rpc("tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []tool `json:"tools"`
	}
	if err := json.Unmarshal(res.Result, &out); err != nil {
		return nil, fmt.Errorf("%w：工具列表无效", errServer)
	}
	return out.Tools, nil
}

func (c *cli) tools(args []string) int {
	list, err := c.listTools()
	if err != nil {
		return c.fail(exitServer, "%v", err)
	}
	for _, t := range list {
		platform, action, _ := strings.Cut(t.Name, "_")
		if len(args) > 0 && platform != args[0] {
			continue
		}
		kind := "读"
		if !t.Annotations.ReadOnly {
			kind = "写·需确认"
		}
		fmt.Fprintf(c.stdout, "%s %s\t[%s] %s\n", platform, strings.ReplaceAll(action, "_", "-"), kind, t.Description)
	}
	return exitOK
}

func (c *cli) findTool(platform, action string) (*tool, int) {
	list, err := c.listTools()
	if err != nil {
		return nil, c.fail(exitServer, "%v", err)
	}
	name := platform + "_" + strings.ReplaceAll(action, "-", "_")
	for i := range list {
		if list[i].Name == name {
			return &list[i], exitOK
		}
	}
	return nil, c.fail(exitUsage, "没有命令 %s %s，用 omni tools %s 查看", platform, action, platform)
}

func (c *cli) printHelp(t *tool) {
	fmt.Fprintln(c.stdout, t.Description)
	required := map[string]bool{}
	for _, r := range t.InputSchema.Required {
		required[r] = true
	}
	names := make([]string, 0, len(t.InputSchema.Properties))
	for n := range t.InputSchema.Properties {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := t.InputSchema.Properties[n]
		flag, mark := n, ""
		switch n {
		case "_account_id":
			flag = "account"
		case "_note":
			flag = "note"
		}
		if required[n] {
			mark = " （必填）"
		}
		extra := ""
		if len(p.Enum) > 0 {
			extra = fmt.Sprintf(" 可选：%v", p.Enum)
		}
		fmt.Fprintf(c.stdout, "  --%s <%s>%s %s%s\n", strings.ReplaceAll(flag, "_", "-"), p.Type, mark, p.Description, extra)
	}
}

// parseFlags converts --flags using the tool's schema, so types and unknown
// names are reported here instead of failing inside a browser flow.
func parseFlags(t *tool, args []string) (map[string]any, bool, error) {
	out := map[string]any{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--help" || arg == "-h" {
			return nil, true, nil
		}
		if !strings.HasPrefix(arg, "--") {
			return nil, false, fmt.Errorf("多余的参数 %q", arg)
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		next := func() (string, error) {
			if hasValue {
				return value, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("--%s 缺少值", name)
			}
			i++
			return args[i], nil
		}
		if name == "json" {
			raw, err := next()
			if err != nil {
				return nil, false, err
			}
			if err := json.Unmarshal([]byte(raw), &out); err != nil {
				return nil, false, fmt.Errorf("--json 不是有效的 JSON 对象: %w", err)
			}
			continue
		}
		key := strings.ReplaceAll(name, "-", "_")
		switch name {
		case "account":
			key = "_account_id"
		case "note":
			key = "_note"
		}
		prop, ok := t.InputSchema.Properties[key]
		if !ok {
			return nil, false, fmt.Errorf("未知参数 --%s，用 --help 查看", name)
		}
		switch prop.Type {
		case "boolean":
			v := true
			if hasValue {
				parsed, err := strconv.ParseBool(value)
				if err != nil {
					return nil, false, fmt.Errorf("--%s 须为 true 或 false", name)
				}
				v = parsed
			}
			out[key] = v
		case "integer", "number":
			raw, err := next()
			if err != nil {
				return nil, false, err
			}
			n, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, false, fmt.Errorf("--%s 须为数字", name)
			}
			out[key] = n
		case "array":
			raw, err := next()
			if err != nil {
				return nil, false, err
			}
			list, _ := out[key].([]any)
			out[key] = append(list, raw)
		default:
			raw, err := next()
			if err != nil {
				return nil, false, err
			}
			out[key] = raw
		}
	}
	missing := []string{}
	for _, r := range t.InputSchema.Required {
		if _, ok := out[r]; !ok {
			missing = append(missing, "--"+strings.ReplaceAll(r, "_", "-"))
		}
	}
	if len(missing) > 0 {
		return nil, false, fmt.Errorf("缺少必填参数: %s", strings.Join(missing, ", "))
	}
	return out, false, nil
}

func (c *cli) callTool(platform, action string, args []string) int {
	t, code := c.findTool(platform, action)
	if t == nil {
		return code
	}
	in, help, err := parseFlags(t, args)
	if help {
		c.printHelp(t)
		return exitOK
	}
	if err != nil {
		return c.fail(exitUsage, "%v", err)
	}
	res, err := c.rpc("tools/call", map[string]any{"name": t.Name, "arguments": in})
	if err != nil {
		return c.fail(exitServer, "%v", err)
	}
	if len(res.Error) > 0 && string(res.Error) != "null" {
		return c.fail(exitTool, "%s", res.Error)
	}
	var out struct {
		IsError    bool            `json:"isError"`
		Structured json.RawMessage `json:"structuredContent"`
		Content    []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res.Result, &out); err != nil {
		return c.fail(exitServer, "响应无效")
	}
	text := ""
	if len(out.Content) > 0 {
		text = out.Content[0].Text
	}
	if len(out.Structured) > 0 && !out.IsError {
		c.printJSON(out.Structured)
		return exitOK
	}
	if !out.IsError {
		fmt.Fprintln(c.stdout, text)
		return exitOK
	}
	var state struct {
		ApprovalID string `json:"approval_id"`
	}
	if json.Unmarshal(out.Structured, &state) == nil && state.ApprovalID != "" {
		// A queued write is reported on stdout: it is the command's real answer.
		c.printJSON(out.Structured)
		return exitApproval
	}
	return c.fail(exitTool, "%s", text)
}

func (c *cli) printJSON(raw json.RawMessage) {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		fmt.Fprintln(c.stdout, string(raw))
		return
	}
	fmt.Fprintln(c.stdout, buf.String())
}

type approval struct {
	ID        string          `json:"id"`
	Tool      string          `json:"tool"`
	PluginID  string          `json:"plugin_id"`
	AccountID string          `json:"account_id"`
	Args      json.RawMessage `json:"args"`
	Hash      string          `json:"hash"`
	Status    string          `json:"status"`
	Note      string          `json:"note"`
	Error     string          `json:"error"`
	CreatedAt time.Time       `json:"created_at"`
}

// approvalAPI talks to the human decision endpoints, which the MCP token does
// not open: they need loopback access or OMNIMCP_APPROVAL_TOKEN.
func (c *cli) approvalAPI(method, path string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.approvalToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.approvalToken)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w（%s）", errServer, c.base)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("%w：响应读取失败", errServer)
	}
	if res.StatusCode != http.StatusOK {
		var out struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &out)
		if out.Error == "" {
			out.Error = fmt.Sprintf("HTTP %d", res.StatusCode)
		}
		return errors.New(out.Error)
	}
	if target != nil {
		return json.Unmarshal(b, target)
	}
	return nil
}

func (c *cli) apiFail(err error) int {
	if errors.Is(err, errServer) {
		return c.fail(exitServer, "%v", err)
	}
	return c.fail(exitTool, "%v", err)
}

func (c *cli) approvals(args []string) int {
	if len(args) == 0 {
		return c.fail(exitUsage, "用法：omni approvals list|show|approve|reject|retry")
	}
	switch args[0] {
	case "list":
		status := ""
		if len(args) == 3 && args[1] == "--status" {
			status = args[2]
		} else if len(args) != 1 {
			return c.fail(exitUsage, "用法：omni approvals list [--status 状态]")
		}
		var out struct {
			Items []approval `json:"items"`
		}
		if err := c.approvalAPI("GET", "/api/approvals?status="+url.QueryEscape(status), nil, &out); err != nil {
			return c.apiFail(err)
		}
		for _, a := range out.Items {
			fmt.Fprintf(c.stdout, "%s\t%s\t%s\t账号 %s\t%s\n", a.ID, a.Status, a.Tool, a.AccountID, a.CreatedAt.Local().Format("01-02 15:04"))
		}
		return exitOK
	case "show", "approve", "reject", "retry":
	default:
		return c.fail(exitUsage, "未知子命令 %s", args[0])
	}
	if len(args) < 2 {
		return c.fail(exitUsage, "缺少审批 ID")
	}
	id := args[1]
	var a approval
	if err := c.approvalAPI("GET", "/api/approvals/"+url.PathEscape(id), nil, &a); err != nil {
		return c.apiFail(err)
	}
	switch args[0] {
	case "show":
		c.describe(a)
		return exitOK
	case "reject":
		reason := ""
		if len(args) == 4 && args[2] == "--reason" {
			reason = args[3]
		}
		if err := c.approvalAPI("POST", "/api/approvals/"+url.PathEscape(id)+"/reject", map[string]string{"reason": reason}, nil); err != nil {
			return c.apiFail(err)
		}
		fmt.Fprintln(c.stdout, "已驳回")
		return exitOK
	}
	c.describe(a)
	prompt := "确认执行以上操作？输入 yes 继续："
	if args[0] == "retry" {
		prompt = "如果上次其实已生效，内容会重复出现。已核对平台并重新执行？输入 yes 继续："
	}
	ok, err := c.askTerminal(prompt)
	if err != nil {
		return c.fail(exitUsage, "确认需要在真实终端里进行，或使用确认页 %s/approvals：%v", c.base, err)
	}
	if !ok {
		fmt.Fprintln(c.stdout, "未执行")
		return exitOK
	}
	if err := c.approvalAPI("POST", "/api/approvals/"+url.PathEscape(id)+"/"+args[0], map[string]string{"hash": a.Hash}, nil); err != nil {
		return c.apiFail(err)
	}
	fmt.Fprintln(c.stdout, "已确认，正在执行。用 omni approvals show "+id+" 查看结果")
	return exitOK
}

func (c *cli) describe(a approval) {
	fmt.Fprintf(c.stdout, "%s  [%s]\n工具：%s\n账号：%s/%s\n", a.ID, a.Status, a.Tool, a.PluginID, a.AccountID)
	if a.Note != "" {
		fmt.Fprintf(c.stdout, "依据：%s\n", a.Note)
	}
	if a.Error != "" {
		fmt.Fprintf(c.stdout, "错误：%s\n", a.Error)
	}
	c.printJSON(a.Args)
}

func (c *cli) askTerminal(prompt string) (bool, error) {
	tty, err := c.confirm()
	if err != nil {
		return false, err
	}
	defer tty.Close()
	fmt.Fprint(c.stderr, prompt)
	var answer string
	if _, err := fmt.Fscanln(tty, &answer); err != nil {
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(answer), "yes"), nil
}

func (c *cli) doctor() int {
	list, err := c.listTools()
	if err != nil {
		return c.fail(exitServer, "%v", err)
	}
	fmt.Fprintf(c.stdout, "服务：%s 正常，%d 个工具\n", c.base, len(list))
	var out struct {
		Items []approval `json:"items"`
	}
	if err := c.approvalAPI("GET", "/api/approvals?status=pending", nil, &out); err != nil {
		fmt.Fprintf(c.stdout, "确认队列：无法读取（%v）\n", err)
		return exitOK
	}
	fmt.Fprintf(c.stdout, "确认队列：%d 项待确认，页面 %s/approvals\n", len(out.Items), c.base)
	return exitOK
}
