package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/heybox/omnimcp/plugin"
)

// Approval lifecycle. Only a human decision moves an entry out of pending, and
// an entry runs at most once per decision: executing is entered under the store
// lock, so concurrent approvals cannot both win.
const (
	ApprovalPending   = "pending"
	ApprovalExecuting = "executing"
	ApprovalSucceeded = "succeeded"
	ApprovalFailed    = "failed"
	// ApprovalUnknown means the platform may or may not have applied the action
	// (timeout, restart, unverifiable result). It is never retried automatically.
	ApprovalUnknown  = "unknown"
	ApprovalRejected = "rejected"
	ApprovalExpired  = "expired"
)

const (
	approvalTTL        = 72 * time.Hour
	approvalDedupe     = 24 * time.Hour
	approvalMaxEntries = 5000
	approvalMaxResult  = 64 << 10
)

// Approval is one proposed platform write and its outcome. The queue doubles as
// the audit ledger, so entries stay after they finish.
type Approval struct {
	ID         string          `json:"id"`
	Tool       string          `json:"tool"`
	PluginID   string          `json:"plugin_id"`
	AccountID  string          `json:"account_id"`
	Args       json.RawMessage `json:"args"`
	Hash       string          `json:"hash"`
	Status     string          `json:"status"`
	Note       string          `json:"note,omitempty"`
	Edited     bool            `json:"edited,omitempty"`
	Auto       bool            `json:"auto,omitempty"`
	Attempts   int             `json:"attempts,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	ExpiresAt  time.Time       `json:"expires_at"`
	DecidedAt  time.Time       `json:"decided_at,omitzero"`
	FinishedAt time.Time       `json:"finished_at,omitzero"`
}

// ApprovalStore persists the queue with the same atomic-replace approach as the
// account store; a write that cannot be saved does not take effect.
type ApprovalStore struct {
	mu      sync.Mutex
	path    string
	entries []Approval
	now     func() time.Time
}

// NewApprovalStore loads dir/approvals.json. Entries left executing by a crash
// become unknown, because the platform may already have applied them.
func NewApprovalStore(dir string) (*ApprovalStore, error) {
	if dir == "" {
		dir = "data"
	}
	s := &ApprovalStore{path: filepath.Join(dir, "approvals.json"), now: time.Now}
	b, err := os.ReadFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(b, &s.entries); err != nil {
			return nil, fmt.Errorf("读取审批队列失败，原文件保留: %w", err)
		}
	}
	changed := false
	for i := range s.entries {
		if s.entries[i].Status == ApprovalExecuting {
			s.entries[i].Status = ApprovalUnknown
			s.entries[i].Error = "服务在执行期间重启，结果未知；请先到平台核对，再决定是否重试"
			s.entries[i].FinishedAt = s.now().UTC()
			changed = true
		}
	}
	if changed {
		if err := s.persist(s.entries); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *ApprovalStore) persist(entries []Approval) error {
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".approvals-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), s.path)
}

// update applies fn to a copy so a failed save leaves memory unchanged.
func (s *ApprovalStore) update(fn func(entries []Approval) ([]Approval, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := fn(append([]Approval(nil), s.entries...))
	if err != nil {
		return err
	}
	next = s.expired(next)
	if err := s.persist(next); err != nil {
		return err
	}
	s.entries = next
	return nil
}

func (s *ApprovalStore) expired(entries []Approval) []Approval {
	now := s.now().UTC()
	for i := range entries {
		if entries[i].Status == ApprovalPending && now.After(entries[i].ExpiresAt) {
			entries[i].Status = ApprovalExpired
			entries[i].FinishedAt = now
		}
	}
	return entries
}

// Propose queues a write, or returns the entry already covering the same tool,
// account and content so a retrying caller cannot stack duplicate proposals.
func (s *ApprovalStore) Propose(tool, pluginID, accountID string, args json.RawMessage, note string) (Approval, bool, error) {
	hash := approvalHash(tool, accountID, args)
	var out Approval
	created := false
	err := s.update(func(entries []Approval) ([]Approval, error) {
		entries = s.expired(entries)
		now := s.now().UTC()
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.Hash != hash {
				continue
			}
			switch e.Status {
			case ApprovalPending, ApprovalExecuting, ApprovalFailed, ApprovalUnknown:
				out = e
				return entries, nil
			case ApprovalSucceeded, ApprovalRejected:
				if now.Sub(e.CreatedAt) < approvalDedupe {
					out = e
					return entries, nil
				}
			}
		}
		id, err := randomID("apr")
		if err != nil {
			return nil, err
		}
		out = Approval{ID: id, Tool: tool, PluginID: pluginID, AccountID: accountID, Args: args, Hash: hash, Status: ApprovalPending, Note: note, CreatedAt: now, ExpiresAt: now.Add(approvalTTL)}
		created = true
		entries = append(entries, out)
		return trimApprovals(entries), nil
	})
	return out, created, err
}

// trimApprovals drops the oldest finished entries; open ones are never dropped.
func trimApprovals(entries []Approval) []Approval {
	over := len(entries) - approvalMaxEntries
	if over <= 0 {
		return entries
	}
	kept := entries[:0]
	for _, e := range entries {
		if over > 0 && e.Status != ApprovalPending && e.Status != ApprovalExecuting && e.Status != ApprovalUnknown {
			over--
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

// List returns entries newest first, optionally limited to the given status.
func (s *ApprovalStore) List(status string, limit int) []Approval {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = s.expired(s.entries)
	out := []Approval{}
	for i := len(s.entries) - 1; i >= 0; i-- {
		if status != "" && s.entries[i].Status != status {
			continue
		}
		out = append(out, s.entries[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *ApprovalStore) Get(id string) (Approval, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = s.expired(s.entries)
	for _, e := range s.entries {
		if e.ID == id {
			return e, true
		}
	}
	return Approval{}, false
}

func (s *ApprovalStore) mutate(id string, fn func(*Approval) error) (Approval, error) {
	var out Approval
	err := s.update(func(entries []Approval) ([]Approval, error) {
		entries = s.expired(entries)
		for i := range entries {
			if entries[i].ID == id {
				if err := fn(&entries[i]); err != nil {
					return nil, err
				}
				out = entries[i]
				return entries, nil
			}
		}
		return nil, fmt.Errorf("审批项不存在")
	})
	return out, err
}

// begin moves an entry into executing. hash must match what the approver saw:
// content edited after the page rendered is refused rather than run unseen.
func (s *ApprovalStore) begin(id, hash string, from ...string) (Approval, error) {
	return s.mutate(id, func(a *Approval) error {
		allowed := false
		for _, st := range from {
			allowed = allowed || a.Status == st
		}
		if !allowed {
			return fmt.Errorf("当前状态为 %s，不能执行", a.Status)
		}
		if hash == "" || hash != a.Hash {
			return fmt.Errorf("内容已变化，请重新查看后再确认")
		}
		a.Status, a.DecidedAt, a.Error, a.Result = ApprovalExecuting, s.now().UTC(), "", nil
		a.Attempts++
		return nil
	})
}

// Reject closes a pending entry; the proposer sees the reason on its next read.
func (s *ApprovalStore) Reject(id, reason string) (Approval, error) {
	return s.mutate(id, func(a *Approval) error {
		if a.Status != ApprovalPending && a.Status != ApprovalFailed && a.Status != ApprovalUnknown {
			return fmt.Errorf("当前状态为 %s，不能驳回", a.Status)
		}
		now := s.now().UTC()
		a.Status, a.Reason, a.DecidedAt, a.FinishedAt = ApprovalRejected, reason, now, now
		return nil
	})
}

// Edit replaces the arguments of a pending entry and changes its hash, so any
// approval page still showing the old content can no longer confirm it.
func (s *ApprovalStore) Edit(id string, args json.RawMessage) (Approval, error) {
	return s.mutate(id, func(a *Approval) error {
		if a.Status != ApprovalPending {
			return fmt.Errorf("只有待确认的内容可以修改")
		}
		a.Args, a.Hash, a.Edited = args, approvalHash(a.Tool, a.AccountID, args), true
		return nil
	})
}

func (s *ApprovalStore) finish(id string, result any, execErr error) (Approval, error) {
	return s.mutate(id, func(a *Approval) error {
		a.FinishedAt = s.now().UTC()
		switch {
		case execErr == nil:
			a.Status = ApprovalSucceeded
			if b, err := json.Marshal(result); err == nil && len(b) <= approvalMaxResult {
				a.Result = b
			}
		case errors.Is(execErr, plugin.ErrOutcomeUnknown) || errors.Is(execErr, context.DeadlineExceeded):
			a.Status, a.Error = ApprovalUnknown, execErr.Error()
		default:
			a.Status, a.Error = ApprovalFailed, execErr.Error()
		}
		return nil
	})
}

// approvalHash binds a decision to one tool, account and exact content. Keys are
// normalised so that field order in the proposer's JSON does not matter.
func approvalHash(tool, accountID string, args json.RawMessage) string {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.UseNumber()
	canonical := []byte(args)
	if dec.Decode(&v) == nil {
		if b, err := json.Marshal(v); err == nil {
			canonical = b
		}
	}
	sum := sha256.Sum256([]byte(tool + "\x00" + accountID + "\x00" + string(canonical)))
	return hex.EncodeToString(sum[:16])
}

func randomID(prefix string) (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// requiredArgsMissing refuses a proposal the plugin would reject anyway, so the
// reviewer is never asked to approve something that cannot run.
func requiredArgsMissing(schema, args json.RawMessage) error {
	var s struct {
		Required []string `json:"required"`
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(schema, &s) != nil || json.Unmarshal(args, &m) != nil {
		return nil
	}
	missing := []string{}
	for _, k := range s.Required {
		if v, ok := m[k]; !ok || string(v) == "null" || string(v) == `""` {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("缺少必填参数: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Approve runs a pending entry with the stored arguments and returns at once;
// long uploads keep going in the background. Retry does the same for an entry
// whose earlier attempt failed or ended with an unknown result.
func (s *Server) Approve(id, hash string) (Approval, error) {
	return s.startApproved(id, hash, ApprovalPending)
}

func (s *Server) Retry(id, hash string) (Approval, error) {
	return s.startApproved(id, hash, ApprovalFailed, ApprovalUnknown)
}

func (s *Server) startApproved(id, hash string, from ...string) (Approval, error) {
	if s.Approvals == nil {
		return Approval{}, fmt.Errorf("审批队列未启用")
	}
	if existing, ok := s.Approvals.Get(id); ok {
		if ref, found := s.Registry.Lookup(existing.Tool); !found || !s.Registry.Enabled(ref.p.Manifest().ID) {
			return Approval{}, fmt.Errorf("工具 %s 当前不可用", existing.Tool)
		}
	}
	a, err := s.Approvals.begin(id, hash, from...)
	if err != nil {
		return Approval{}, err
	}
	s.approvalRuns.Add(1)
	go func() {
		defer s.approvalRuns.Done()
		s.runApproved(a)
	}()
	return a, nil
}

func (s *Server) runApproved(a Approval) {
	logger := s.Logger
	if logger == nil {
		logger = DefaultLogger()
	}
	started := time.Now()
	var out any
	err := fmt.Errorf("工具 %s 不存在", a.Tool)
	if ref, ok := s.Registry.Lookup(a.Tool); ok {
		out, err = s.invoke(context.Background(), ref, plugin.Account{PluginID: a.PluginID, AccountID: a.AccountID}, a.Args, a.ID, a.Tool)
	}
	logger.Info("approved tool call", "approval", a.ID, "tool", a.Tool, "account", a.AccountID, "error", err != nil, "duration", time.Since(started).Round(time.Millisecond))
	if _, saveErr := s.Approvals.finish(a.ID, out, err); saveErr != nil {
		// The entry stays executing on disk and is recovered as unknown on restart.
		logger.Error("persist approval result", "approval", a.ID, "error", saveErr)
	}
}

// WaitApprovals blocks until background executions finish; used on shutdown.
func (s *Server) WaitApprovals() { s.approvalRuns.Wait() }

// AutoPolicy lets the account owner run named tools without a confirmation click,
// each with a per-account limit over a rolling 24 hours. Tools not listed stay in
// the queue: generic browser tools in particular, because nothing can verify them.
type AutoPolicy map[string]int

// ParseAutoPolicy reads "tool:limit,tool:limit". Empty means every write waits
// for a person, and a malformed entry is an error rather than a wider policy.
func ParseAutoPolicy(raw string) (AutoPolicy, error) {
	policy := AutoPolicy{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tool, limit, ok := strings.Cut(part, ":")
		n, err := strconv.Atoi(limit)
		if !ok || err != nil || n < 1 || tool == "" {
			return nil, fmt.Errorf("OMNIMCP_AUTO_APPROVE 条目无效: %q，应为 工具名:每日上限", part)
		}
		policy[tool] = n
	}
	return policy, nil
}

// countAuto counts automatic runs of a tool for an account in the last 24 hours.
func (s *ApprovalStore) countAuto(tool, accountID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	since := s.now().Add(-24 * time.Hour)
	n := 0
	for _, e := range s.entries {
		if e.Auto && e.Tool == tool && e.AccountID == accountID && e.CreatedAt.After(since) {
			n++
		}
	}
	return n
}

// runAutomatically starts a new proposal when policy allows it. It returns false
// when the entry should keep waiting in the queue, for example over the limit.
func (s *Server) runAutomatically(a Approval) (Approval, bool, error) {
	limit, listed := s.Auto[a.Tool]
	if !listed {
		return a, false, nil
	}
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	if s.Approvals.countAuto(a.Tool, a.AccountID) >= limit {
		return a, false, nil
	}
	if _, err := s.Approvals.mutate(a.ID, func(e *Approval) error {
		e.Auto = true
		return nil
	}); err != nil {
		return a, false, err
	}
	started, err := s.startApproved(a.ID, a.Hash, ApprovalPending)
	if err != nil {
		return a, false, err
	}
	return started, true, nil
}

// proposeWrite is the only path a mutating tools/call can take. It runs a tool
// only under the owner's AutoPolicy; otherwise execution starts from Approve,
// which only a human surface can reach.
func (s *Server) proposeWrite(req request, name string, ref toolRef, accountID string, args json.RawMessage, note string) response {
	if s.Approvals == nil {
		return toolError(req.ID, fmt.Errorf("审批队列未启用，已拒绝写操作 %s", name))
	}
	if err := requiredArgsMissing(ref.t.InputSchema, args); err != nil {
		return toolError(req.ID, err)
	}
	a, created, err := s.Approvals.Propose(name, ref.p.Manifest().ID, accountID, args, note)
	if err != nil {
		return toolError(req.ID, fmt.Errorf("写入审批队列失败，操作未执行: %w", err))
	}
	if created {
		started, ran, err := s.runAutomatically(a)
		if err != nil {
			return toolError(req.ID, fmt.Errorf("自动执行未能开始，操作未执行: %w", err))
		}
		if ran {
			a = started
		}
	}
	return approvalResponse(req.ID, a, created)
}

func approvalResponse(id any, a Approval, created bool) response {
	out := map[string]any{"approval_id": a.ID, "approval_status": a.Status, "hash": a.Hash, "created": created}
	message := ""
	switch a.Status {
	case ApprovalPending:
		out["status"] = "approval_required"
		message = "操作尚未执行，已进入待确认队列。用户确认后由服务端执行，可用 omnimcp_approval_status 查询结果。不要告诉用户已经完成。"
	case ApprovalExecuting:
		out["status"] = "executing"
		message = "正在执行。可用 omnimcp_approval_status 查询结果。"
	case ApprovalSucceeded:
		out["status"] = "already_executed"
		out["result"] = a.Result
		message = "相同内容已经执行成功，这次没有重复提交。"
	case ApprovalRejected:
		out["status"] = "rejected"
		out["reason"] = a.Reason
		message = "用户已驳回相同内容，这次没有提交。请修改内容或询问用户。"
	default:
		out["status"] = a.Status
		out["error"] = a.Error
		message = "相同内容上次未确认成功。是否重试由用户决定，这次没有重复提交。"
	}
	out["message"] = message
	b, err := json.Marshal(out)
	if err != nil {
		return toolError(id, err)
	}
	// isError keeps a client that only checks the flag from assuming the write
	// happened; only a confirmed earlier success reads as a normal result.
	result := map[string]any{"isError": a.Status != ApprovalSucceeded, "content": []map[string]string{{"type": "text", "text": string(b)}}, "structuredContent": json.RawMessage(b)}
	return response{"2.0", id, result, nil}
}

// approvalsPlugin lets callers read outcomes. It deliberately has no tool that
// approves, rejects or edits: those decisions stay outside the MCP surface.
type approvalsPlugin struct{ store *ApprovalStore }

// NewApprovalsPlugin exposes the read-only omnimcp_* status tools.
func NewApprovalsPlugin(store *ApprovalStore) plugin.Plugin { return approvalsPlugin{store} }

func (approvalsPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "omnimcp", Name: "OmniMCP", Version: "0.1.0", APIVersion: "v1", Capabilities: []string{"approvals"}}
}

func (approvalsPlugin) Tools() []plugin.Tool {
	return []plugin.Tool{
		{Name: "approval_status", Description: "查询一项写操作的确认状态和执行结果（pending/executing/succeeded/failed/unknown/rejected/expired）", InputSchema: plugin.ToolSchema(`{"id":{"type":"string"}}`, []string{"id"})},
		{Name: "list_approvals", Description: "列出写操作提案及执行记录，可按状态过滤，最新的在前", InputSchema: plugin.ToolSchema(`{"status":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":200}}`, nil)},
	}
}

func (p approvalsPlugin) Execute(_ context.Context, _ *plugin.Context, name string, raw json.RawMessage) (any, error) {
	var in struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	switch name {
	case "approval_status":
		a, ok := p.store.Get(in.ID)
		if !ok {
			return nil, fmt.Errorf("审批项不存在: %s", in.ID)
		}
		return a, nil
	case "list_approvals":
		if in.Limit <= 0 || in.Limit > 200 {
			in.Limit = 50
		}
		return map[string]any{"items": p.store.List(in.Status, in.Limit)}, nil
	}
	return nil, fmt.Errorf("unknown tool %q", name)
}
