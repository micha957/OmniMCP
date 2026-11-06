package agentops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Proposer queues a platform write for human confirmation. It cannot execute one.
type Proposer interface {
	Propose(ctx context.Context, tool string, args map[string]any, account, note string) (Proposal, error)
}

// Proposal is the queue's answer to a proposed write.
type Proposal struct {
	ID     string          `json:"id"`
	Hash   string          `json:"hash"`
	Status string          `json:"status"`
	Error  string          `json:"error"`
	Reason string          `json:"reason"`
	Result json.RawMessage `json:"result"`
}

// ApprovalsURL links to the queue page when connected to OmniMCP.
func (e *Engine) ApprovalsURL() string {
	if m, ok := e.Reader.(*MCPReader); ok {
		return m.ApprovalsURL()
	}
	return ""
}

// openApproval reports whether the queue still has a say over this draft.
func openApproval(a *DraftApproval) bool {
	return a != nil && (a.Status == "pending" || a.Status == "executing")
}

// Submit sends a reviewed draft to the confirmation queue. Nothing reaches the
// platform until a person approves it there.
func (e *Engine) Submit(ctx context.Context, draftID, mode string) (DraftApproval, error) {
	if e.Proposer == nil {
		return DraftApproval{}, fmt.Errorf("没有连接 OmniMCP，无法提交确认")
	}
	s := e.Store.Snapshot()
	var d *Draft
	for i := range s.Drafts {
		if s.Drafts[i].ID == draftID {
			d = &s.Drafts[i]
		}
	}
	if d == nil {
		return DraftApproval{}, fmt.Errorf("草稿不存在")
	}
	if d.Origin == "decision" || d.Status == "archived" {
		return DraftApproval{}, fmt.Errorf("这份内容不能提交")
	}
	if d.Status == "sent" {
		return DraftApproval{}, fmt.Errorf("这份内容已经发送")
	}
	if openApproval(d.Approval) {
		return DraftApproval{}, fmt.Errorf("这份内容已在确认队列中")
	}
	tool, args, err := BuildAction(*d, mode)
	if err != nil {
		return DraftApproval{}, err
	}
	account := s.Config.Account(d.platform())
	if account == "" {
		return DraftApproval{}, fmt.Errorf("请先在角色设置中绑定%s账号", platforms[d.platform()].Name)
	}
	note := d.Reason
	if d.Origin == "model" {
		note = "AI 草稿。依据：" + d.Reason
	}
	p, err := e.Proposer.Propose(ctx, tool, args, account, note)
	if err != nil {
		return DraftApproval{}, err
	}
	approval := DraftApproval{ID: p.ID, Hash: p.Hash, Status: p.Status, Mode: mode, Tool: tool, Account: account, Error: p.Error, Reason: p.Reason, SubmittedAt: time.Now().UTC()}
	err = e.Store.Update(func(st *State) error {
		for i := range st.Drafts {
			if st.Drafts[i].ID == draftID {
				st.Drafts[i].Approval = &approval
				st.Drafts[i].ScheduleError = ""
				return nil
			}
		}
		return fmt.Errorf("草稿不存在")
	})
	return approval, err
}

// SyncApprovals copies outcomes back from the queue and records receipts.
func (e *Engine) SyncApprovals(ctx context.Context) error {
	if e.Reader == nil {
		return nil
	}
	s := e.Store.Snapshot()
	var firstErr error
	for _, d := range s.Drafts {
		if !openApproval(d.Approval) {
			continue
		}
		raw, err := e.Reader.Call(ctx, "omnimcp_approval_status", map[string]any{"id": d.Approval.ID})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var p Proposal
		if json.Unmarshal(raw, &p) != nil || p.Status == "" {
			continue
		}
		if p.Status == d.Approval.Status {
			continue
		}
		if err := e.Store.Update(func(st *State) error { return applyOutcome(st, d.ID, p) }); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func applyOutcome(st *State, draftID string, p Proposal) error {
	for i := range st.Drafts {
		d := &st.Drafts[i]
		if d.ID != draftID || d.Approval == nil {
			continue
		}
		d.Approval.Status, d.Approval.Error, d.Approval.Reason = p.Status, p.Error, p.Reason
		if p.Status != "succeeded" {
			return nil
		}
		var receipt struct {
			CommentID string `json:"comment_id"`
			NoteID    string `json:"note_id"`
			URL       string `json:"url"`
			Evidence  string `json:"evidence"`
		}
		_ = json.Unmarshal(p.Result, &receipt)
		now := time.Now().UTC()
		d.SentAt, d.PlatformID, d.PlatformURL = now, receipt.CommentID, receipt.URL
		if receipt.NoteID != "" {
			d.PlatformID = receipt.NoteID
		}
		d.Receipt = receipt.Evidence
		// A platform draft is not public content; the local draft stays editable.
		if d.Approval.Mode == "publish" {
			d.Status = "sent"
		}
		for j := range st.Opportunities {
			if st.Opportunities[j].ID == d.SourceID && d.Kind == "comment" {
				st.Opportunities[j].Status = "sent"
			}
		}
		for j := range st.Threads {
			t := &st.Threads[j]
			if t.ID != d.SourceID || d.Kind != "reply" {
				continue
			}
			t.Messages = trimMessages(append(t.Messages, Message{ID: newID("message"), Author: "我", Text: d.Body, Mine: true, PlatformID: receipt.CommentID, CreatedAt: now}))
			if t.Status != "pending" {
				t.Status = "answered"
			}
			t.UpdatedAt = now
		}
		return nil
	}
	return nil
}

func trimMessages(m []Message) []Message {
	if len(m) > 30 {
		return m[len(m)-30:]
	}
	return m
}

// SubmitDue moves scheduled drafts into the confirmation queue. Reaching the
// scheduled time only asks for confirmation; it never publishes on its own.
func (e *Engine) SubmitDue(ctx context.Context) {
	s := e.Store.Snapshot()
	now := time.Now()
	for _, d := range s.Drafts {
		if d.ScheduledAt.IsZero() || d.ScheduledAt.After(now) || d.Status != "ready" || d.Approval != nil || d.ScheduleError != "" {
			continue
		}
		if _, err := e.Submit(ctx, d.ID, "publish"); err != nil {
			id, message := d.ID, err.Error()
			_ = e.Store.Update(func(st *State) error {
				for i := range st.Drafts {
					if st.Drafts[i].ID == id {
						st.Drafts[i].ScheduleError = message
					}
				}
				return nil
			})
		}
	}
}

// inboxItem is the notification shape shared by platform plugins.
type inboxItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Time  int64  `json:"time"`
	From  struct {
		UserID   string `json:"user_id"`
		Nickname string `json:"nickname"`
	} `json:"from"`
	CommentID     string `json:"comment_id"`
	RootID        string `json:"root_id"`
	CommentText   string `json:"comment_text"`
	FeedID        string `json:"feed_id"`
	FeedXsecToken string `json:"feed_xsec_token"`
	FeedTitle     string `json:"feed_title"`
	URL           string `json:"url"`
}

func threadURL(platform string, it inboxItem) string {
	if it.URL != "" {
		return it.URL
	}
	if platform == "xiaohongshu" && it.FeedID != "" {
		return "https://www.xiaohongshu.com/explore/" + it.FeedID
	}
	return ""
}

// inboxMaxAge stops a first sync from drafting replies to years-old comments.
const inboxMaxAge = 14 * 24 * time.Hour

// mergeInbox turns new comments into pending threads; an event already seen is
// skipped, so polling or a restart cannot schedule the same reply twice.
func mergeInbox(st *State, platform string, items []inboxItem) int {
	seen := map[string]bool{}
	for _, id := range st.SeenEvents {
		seen[id] = true
	}
	added := 0
	for _, it := range items {
		if it.CommentID == "" || strings.TrimSpace(it.CommentText) == "" {
			continue
		}
		key := platform + ":" + it.CommentID
		if seen[key] {
			continue
		}
		seen[key] = true
		st.SeenEvents = append(st.SeenEvents, key)
		at := time.Now().UTC()
		if it.Time > 0 {
			// Platforms report seconds or milliseconds depending on the endpoint.
			if it.Time > 1e11 {
				at = time.UnixMilli(it.Time).UTC()
			} else {
				at = time.Unix(it.Time, 0).UTC()
			}
			if time.Since(at) > inboxMaxAge {
				continue
			}
		}
		message := Message{ID: newID("message"), Author: it.From.Nickname, Text: it.CommentText, PlatformID: it.CommentID, CreatedAt: at}
		threadKey := platform + ":" + it.FeedID + ":" + it.From.UserID
		var thread *Thread
		for i := range st.Threads {
			if st.Threads[i].Key == threadKey {
				thread = &st.Threads[i]
			}
		}
		if thread == nil {
			if len(st.Threads) >= 500 {
				continue
			}
			st.Threads = append(st.Threads, Thread{ID: newID("thread"), Key: threadKey, Platform: platform, URL: threadURL(platform, it), Title: it.FeedTitle, Context: "来自平台通知：对方在《" + it.FeedTitle + "》下的评论。"})
			thread = &st.Threads[len(st.Threads)-1]
		}
		thread.Messages = trimMessages(append(thread.Messages, message))
		thread.Status, thread.Reason, thread.UpdatedAt = "pending", "", time.Now().UTC()
		thread.FeedID, thread.Token, thread.CommentID, thread.RootID, thread.AuthorID = it.FeedID, it.FeedXsecToken, it.CommentID, it.RootID, it.From.UserID
		added++
	}
	if len(st.SeenEvents) > 5000 {
		st.SeenEvents = st.SeenEvents[len(st.SeenEvents)-5000:]
	}
	return added
}

// SyncInbox imports new comments and records how much was actually read: a
// partial scan must not look like "no new replies".
func (e *Engine) SyncInbox(ctx context.Context) (int, error) {
	s := e.Store.Snapshot()
	total := 0
	var firstErr error
	for platform, info := range platforms {
		account := s.Config.Account(platform)
		if !info.Inbox || account == "" {
			continue
		}
		tool := platform + "_get_notifications"
		raw, err := e.Reader.Call(ctx, tool, map[string]any{"_account_id": account, "limit": 30})
		status := InboxStatus{CheckedAt: time.Now().UTC()}
		added := 0
		var page struct {
			Items    []inboxItem `json:"items"`
			Complete bool        `json:"complete"`
			Loaded   int         `json:"loaded"`
		}
		if err == nil {
			err = json.Unmarshal(raw, &page)
		}
		if err != nil {
			status.Error = err.Error()
			if firstErr == nil {
				firstErr = fmt.Errorf("%s通知读取失败: %w", info.Name, err)
			}
		} else {
			status.LastSuccess, status.Loaded, status.Complete = status.CheckedAt, page.Loaded, page.Complete
		}
		saveErr := e.Store.Update(func(st *State) error {
			if st.Inbox == nil {
				st.Inbox = map[string]InboxStatus{}
			}
			if err != nil {
				status.LastSuccess, status.Loaded, status.Complete = st.Inbox[platform].LastSuccess, st.Inbox[platform].Loaded, st.Inbox[platform].Complete
			}
			st.Inbox[platform] = status
			added = mergeInbox(st, platform, page.Items)
			return nil
		})
		if saveErr != nil && firstErr == nil {
			firstErr = saveErr
		}
		total += added
	}
	return total, firstErr
}

// CheckHealth records login state per bound account for the attention list.
func (e *Engine) CheckHealth(ctx context.Context) error {
	s := e.Store.Snapshot()
	for platform, info := range platforms {
		account := s.Config.Account(platform)
		if !info.Login || account == "" {
			continue
		}
		h := Health{CheckedAt: time.Now().UTC()}
		raw, err := e.Reader.Call(ctx, platform+"_check_login", map[string]any{"_account_id": account})
		if err != nil {
			h.Error = err.Error()
		} else {
			var login struct {
				LoggedIn bool `json:"logged_in"`
				User     struct {
					Nickname string `json:"nickname"`
				} `json:"user"`
			}
			if json.Unmarshal(raw, &login) != nil {
				h.Error = "无法解析登录状态"
			}
			h.LoggedIn, h.Nickname = login.LoggedIn, login.User.Nickname
		}
		if err := e.Store.Update(func(st *State) error {
			if st.Health == nil {
				st.Health = map[string]Health{}
			}
			st.Health[platform] = h
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// parseCount reads display counts such as "1.2万" or "10+". Blank means unknown.
func parseCount(s string) int64 {
	s = strings.TrimSuffix(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), "+")
	mult := 1.0
	for suffix, m := range map[string]float64{"万": 1e4, "亿": 1e8, "w": 1e4, "k": 1e3} {
		if strings.HasSuffix(strings.ToLower(s), suffix) {
			s, mult = s[:len(s)-len(suffix)], m
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f*mult + 0.5)
}

// SyncMetrics appends one sample per bound account; trends need the history.
func (e *Engine) SyncMetrics(ctx context.Context) error {
	s := e.Store.Snapshot()
	var firstErr error
	for platform, info := range platforms {
		account := s.Config.Account(platform)
		if !info.Account || account == "" {
			continue
		}
		raw, err := e.Reader.Call(ctx, platform+"_get_my_account", map[string]any{"_account_id": account})
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s数据读取失败: %w", info.Name, err)
			}
			continue
		}
		var page struct {
			Stats map[string]string `json:"stats"`
			Notes []struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				Views    string `json:"views"`
				Likes    string `json:"likes"`
				Collects string `json:"collects"`
				Comments string `json:"comments"`
				Shares   string `json:"shares"`
			} `json:"notes"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("无法解析%s数据", info.Name)
			}
			continue
		}
		sample := MetricSample{At: time.Now().UTC(), Platform: platform, Stats: page.Stats}
		for _, n := range page.Notes {
			sample.Notes = append(sample.Notes, NoteStat{ID: n.ID, Title: n.Title, Views: parseCount(n.Views), Likes: parseCount(n.Likes), Collects: parseCount(n.Collects), Comments: parseCount(n.Comments), Shares: parseCount(n.Shares)})
		}
		if err := e.Store.Update(func(st *State) error {
			st.Samples = append(st.Samples, sample)
			if len(st.Samples) > 500 {
				st.Samples = st.Samples[len(st.Samples)-500:]
			}
			return nil
		}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SyncWatchlist samples tracked accounts' public profiles for comparison.
func (e *Engine) SyncWatchlist(ctx context.Context) error {
	s := e.Store.Snapshot()
	account := s.Config.Account("xiaohongshu")
	if account == "" {
		return nil
	}
	var firstErr error
	for _, w := range s.Config.Watch {
		if w.Platform != "xiaohongshu" {
			continue
		}
		raw, err := e.Reader.Call(ctx, "xiaohongshu_get_user_profile", map[string]any{"_account_id": account, "user_id": w.UserID, "xsec_token": w.Token})
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("读取对标账号 %s 失败: %w", w.Name, err)
			}
			continue
		}
		var page struct {
			Interactions []struct {
				Type  string `json:"type"`
				Count string `json:"count"`
			} `json:"interactions"`
			Feeds []struct {
				ID       string `json:"id"`
				NoteCard struct {
					DisplayTitle string `json:"displayTitle"`
					InteractInfo struct {
						LikedCount, CollectedCount, CommentCount string
					} `json:"interactInfo"`
				} `json:"noteCard"`
			} `json:"feeds"`
		}
		if json.Unmarshal(raw, &page) != nil {
			continue
		}
		sample := MetricSample{At: time.Now().UTC(), Platform: w.Platform, Watch: w.Name, Stats: map[string]string{}}
		for _, i := range page.Interactions {
			sample.Stats[i.Type] = i.Count
		}
		for _, f := range page.Feeds {
			i := f.NoteCard.InteractInfo
			sample.Notes = append(sample.Notes, NoteStat{ID: f.ID, Title: f.NoteCard.DisplayTitle, Likes: parseCount(i.LikedCount), Collects: parseCount(i.CollectedCount), Comments: parseCount(i.CommentCount)})
		}
		if err := e.Store.Update(func(st *State) error {
			st.Samples = append(st.Samples, sample)
			if len(st.Samples) > 500 {
				st.Samples = st.Samples[len(st.Samples)-500:]
			}
			return nil
		}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Insights summarises what performed best so new topics can build on it. These
// are facts read from the platform, so the model may use them for topic choice.
func Insights(s State) string {
	latest := map[string]MetricSample{}
	for _, m := range s.Samples {
		if m.Watch == "" {
			latest[m.Platform] = m
		}
	}
	notes := []NoteStat{}
	for _, m := range latest {
		notes = append(notes, m.Notes...)
	}
	if len(notes) == 0 {
		return ""
	}
	score := func(n NoteStat) int64 { return n.Likes + 2*n.Collects + 3*n.Comments }
	sort.SliceStable(notes, func(i, j int) bool { return score(notes[i]) > score(notes[j]) })
	if len(notes) > 3 {
		notes = notes[:3]
	}
	parts := make([]string, 0, len(notes))
	for _, n := range notes {
		parts = append(parts, fmt.Sprintf("《%s》赞 %d 藏 %d 评 %d", n.Title, n.Likes, n.Collects, n.Comments))
	}
	return "当前账号互动较好的内容：" + strings.Join(parts, "；")
}

// Adapt rewrites a draft for another platform as a new draft for review.
func (e *Engine) Adapt(ctx context.Context, draftID, platform string) (Draft, error) {
	info, ok := platforms[platform]
	if !ok {
		return Draft{}, fmt.Errorf("不支持的平台 %s", platform)
	}
	if e.Generator == nil {
		return Draft{}, fmt.Errorf("模型尚未连接，无法改写")
	}
	s := e.Store.Snapshot()
	var src *Draft
	for i := range s.Drafts {
		if s.Drafts[i].ID == draftID {
			src = &s.Drafts[i]
		}
	}
	if src == nil || src.Kind != "note" || src.Origin == "decision" {
		return Draft{}, fmt.Errorf("只有笔记草稿可以改写到其他平台")
	}
	if src.platform() == platform {
		return Draft{}, fmt.Errorf("这份草稿已经是%s版本", info.Name)
	}
	key := "adapt:" + src.ID + ":" + platform
	for _, d := range s.Drafts {
		if d.SourceKey == key {
			return Draft{}, fmt.Errorf("已经有%s版本的草稿", info.Name)
		}
	}
	task := Task{Kind: "note", Platform: platform, SourceID: src.ID, SourceKey: key, Title: src.Title, Context: fmt.Sprintf("把下面的内容改写成适合%s的版本：标题不超过 %d 字，正文不超过 %d 字，符合该平台读者习惯。不增加原文和产品事实之外的内容。\n原标题：%s\n原文：%s", info.Name, info.TitleMax, info.BodyMax, src.Title, src.Body)}
	decision, err := e.Generator.Generate(ctx, s.Config, task)
	if err != nil {
		return Draft{}, err
	}
	if decision.Action != "draft" {
		return Draft{}, fmt.Errorf("模型没有生成改写：%s", decision.Reason)
	}
	if err := validateDecision(s.Config, task, decision); err != nil {
		return Draft{}, err
	}
	now := time.Now().UTC()
	title := decision.Title
	if title == "" {
		title = src.Title
	}
	d := Draft{ID: newID("draft"), Platform: platform, Kind: "note", Title: title, Body: decision.Body, Assets: decision.Assets, AssetPaths: src.AssetPaths, FactIDs: decision.FactIDs, SourceKey: key, SourceID: src.ID, AdaptedFrom: src.ID, Origin: "model", Status: "draft", Reason: decision.Reason, CreatedAt: now, UpdatedAt: now}
	if err := d.Validate(); err != nil {
		return Draft{}, err
	}
	return d, e.Store.Update(func(st *State) error {
		st.Drafts = append(st.Drafts, d)
		return nil
	})
}

// Attention lists what needs a person, for the overview page.
func Attention(s State) []AttentionItem {
	out := []AttentionItem{}
	add := func(kind, text, id string) { out = append(out, AttentionItem{Kind: kind, Text: text, ID: id}) }
	for p, h := range s.Health {
		name := platforms[p].Name
		switch {
		case h.Error != "":
			add("health", name+"账号检查失败："+h.Error, p)
		case !h.LoggedIn:
			add("health", name+"账号未登录或登录已失效", p)
		}
	}
	for p, i := range s.Inbox {
		if i.Error != "" {
			add("inbox", platforms[p].Name+"通知读取失败："+i.Error, p)
		}
	}
	for _, d := range s.Drafts {
		switch {
		case d.ScheduleError != "":
			add("schedule", "《"+d.Title+"》到期但未能提交："+d.ScheduleError, d.ID)
		case d.Approval == nil:
		case d.Approval.Status == "unknown":
			add("unknown", "《"+d.Title+"》结果不明，请到平台核对，不要直接重试", d.ID)
		case d.Approval.Status == "failed":
			add("failed", "《"+d.Title+"》执行失败："+d.Approval.Error, d.ID)
		}
	}
	for _, o := range s.Opportunities {
		if o.Status == "attention" {
			add("source", "帖子《"+o.Title+"》需要你判断："+o.Reason, o.ID)
		}
	}
	for _, t := range s.Threads {
		if t.Status == "attention" {
			add("source", "对话《"+t.Title+"》需要你处理："+t.Reason, t.ID)
		}
	}
	return out
}
