package agentops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Engine struct {
	Store     *Store
	Generator Generator
	Reader    Reader
	Proposer  Proposer
	Decider   Decider
	Model     string
	root      context.Context
	mu        sync.Mutex
	busy      bool
	cancel    context.CancelFunc
	done      chan struct{}
	lastError string
}

func NewEngine(ctx context.Context, store *Store, generator Generator, reader Reader, model string) *Engine {
	e := &Engine{Store: store, Generator: generator, Reader: reader, Model: model, root: ctx}
	e.Proposer, _ = reader.(Proposer)
	e.Decider, _ = reader.(Decider)
	return e
}

func (e *Engine) Busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.busy
}

func (e *Engine) LastError() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastError
}

func (e *Engine) Cancel() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
	}
}

func (e *Engine) Wait(ctx context.Context) error {
	e.mu.Lock()
	done := e.done
	e.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) Start(trigger, mode string) (string, error) {
	if mode != "prepare" && mode != "discover" && mode != "all" && mode != "sync" {
		return "", fmt.Errorf("运行方式须为 prepare、discover、sync 或 all")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy {
		return "", fmt.Errorf("已有运行正在进行")
	}
	run := Run{ID: newID("run"), Trigger: trigger, Status: "running", StartedAt: time.Now().UTC()}
	if err := e.Store.Update(func(s *State) error {
		if len(s.Runs) >= 100 {
			s.Runs = s.Runs[len(s.Runs)-99:]
		}
		s.Runs = append(s.Runs, run)
		s.NextRunAt = run.StartedAt.Add(time.Duration(s.Config.IntervalMinutes) * time.Minute)
		return nil
	}); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(e.root, 5*time.Minute)
	e.busy, e.cancel, e.done, e.lastError = true, cancel, make(chan struct{}), ""
	go func(done chan struct{}) {
		defer func() {
			cancel()
			e.mu.Lock()
			e.busy, e.cancel = false, nil
			close(done)
			e.mu.Unlock()
		}()
		err := e.executeSafely(ctx, &run, mode)
		run.EndedAt = time.Now().UTC()
		if err != nil {
			run.Status, run.Summary = "failed", err.Error()
			if ctx.Err() != nil {
				run.Status, run.Summary = "interrupted", "运行已停止；已保存的草稿保留"
			}
		} else {
			run.Status = "completed"
			if run.Summary == "" {
				run.Summary = fmt.Sprintf("准备 %d 份草稿，跳过 %d 项，转交 %d 项", run.Drafts, run.Skipped, run.Escalated)
			}
		}
		// Persistence errors must be visible instead of reporting a completed run.
		if saveErr := e.Store.Update(func(s *State) error {
			s.NextRunAt = run.EndedAt.Add(time.Duration(s.Config.IntervalMinutes) * time.Minute)
			for i := range s.Runs {
				if s.Runs[i].ID == run.ID {
					s.Runs[i] = run
					return nil
				}
			}
			return fmt.Errorf("运行记录不存在")
		}); saveErr != nil {
			// A running record remains on disk and is recovered as interrupted.
			fmt.Printf("agentops: persist run result failed: %v\n", saveErr)
			e.mu.Lock()
			e.lastError = "本轮结果保存失败，请检查数据目录空间和权限；运行状态在重启后会标记为中断"
			e.mu.Unlock()
		}
	}(e.done)
	return run.ID, nil
}

func (e *Engine) executeSafely(ctx context.Context, run *Run, mode string) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("本轮运行意外中断；已保存内容保留，请检查服务日志后重试")
		}
	}()
	return e.execute(ctx, run, mode)
}

func (e *Engine) Schedule(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			e.Cancel()
			return
		case <-ticker.C:
			s := e.Store.Snapshot()
			// Queue outcomes and due drafts move regardless of automation: approved
			// work must get its receipt, and the schedule was set by the user.
			_ = e.SyncApprovals(ctx)
			e.SubmitDue(ctx)
			if s.Config.Automation && !e.Busy() && !time.Now().Before(s.NextRunAt) {
				mode := "prepare"
				if s.Config.Discover {
					mode = "all"
				}
				_, _ = e.Start("schedule", mode)
			}
		}
	}
}

// scheduledSyncGap is the minimum time between automatic reads of the platforms.
// Reading too often gets real accounts limited, however often drafts are prepared.
const scheduledSyncGap = 2 * time.Hour

// syncPlatforms reads inbox, health and metrics. It stops at nothing short of
// cancellation: one platform being down must not hide another's new replies.
func (e *Engine) syncPlatforms(ctx context.Context, run *Run) {
	if e.Reader == nil {
		return
	}
	if run.Trigger == "schedule" && time.Since(e.Store.Snapshot().LastSyncAt) < scheduledSyncGap {
		return
	}
	if err := e.Store.Update(func(s *State) error {
		s.LastSyncAt = time.Now().UTC()
		return nil
	}); err != nil {
		run.Summary = err.Error()
		return
	}
	_ = e.CheckHealth(ctx)
	added, inboxErr := e.SyncInbox(ctx)
	metricsErr := e.SyncMetrics(ctx)
	watchErr := e.SyncWatchlist(ctx)
	run.Summary = fmt.Sprintf("同步到 %d 条新回复", added)
	for _, err := range []error{inboxErr, metricsErr, watchErr} {
		if err != nil {
			run.Summary += "；" + err.Error()
		}
	}
}

func (e *Engine) execute(ctx context.Context, run *Run, mode string) error {
	if mode == "sync" {
		e.syncPlatforms(ctx, run)
		return ctx.Err()
	}
	if mode == "all" {
		e.syncPlatforms(ctx, run)
		run.Summary = ""
	}
	if mode == "discover" || mode == "all" {
		n, err := e.discover(ctx)
		if err != nil {
			return err
		}
		if mode == "discover" {
			run.Summary = fmt.Sprintf("已读取平台，新增 %d 个待判断帖子", n)
			return nil
		}
	}
	snapshot := e.Store.Snapshot()
	tasks := pending(snapshot)
	if len(tasks) == 0 {
		run.Summary = "没有新的待处理内容；现有草稿已保留"
		return nil
	}
	if e.Generator == nil {
		return fmt.Errorf("模型尚未连接。可先编辑已有内容；连接模型后可根据新帖子和对话准备草稿")
	}
	for i, task := range tasks {
		if i >= snapshot.Config.MaxTasks {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Store.Snapshot().Revision != snapshot.Revision {
			return fmt.Errorf("角色或规则已更新，后续任务留待下次运行")
		}
		decision, err := e.Generator.Generate(ctx, snapshot.Config, task)
		if err != nil {
			return err
		}
		run.Tokens += decision.Tokens
		if err := validateDecision(snapshot.Config, task, decision); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.commit(snapshot.Revision, task, decision); err != nil {
			return err
		}
		switch decision.Action {
		case "draft":
			run.Drafts++
		case "skip":
			run.Skipped++
		case "escalate":
			run.Escalated++
		}
	}
	return nil
}

func hash(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:12])
}

func pending(s State) []Task {
	var tasks []Task
	for _, t := range s.Threads {
		if t.Status == "pending" && len(t.Messages) > 0 {
			b, _ := json.Marshal(t.Messages)
			messages := t.Messages
			if len(messages) > 6 {
				messages = messages[len(messages)-6:]
			}
			recent, _ := json.Marshal(messages)
			tasks = append(tasks, Task{Kind: "reply", Platform: t.Platform, SourceID: t.ID, SourceKey: "reply:" + t.ID + ":" + hash(string(b)), SourceURL: t.URL, Title: t.Title, Context: t.Context + "\n最近收到的消息：" + string(recent)})
		}
	}
	for _, o := range s.Opportunities {
		if o.Status == "pending" {
			tasks = append(tasks, Task{Kind: "comment", SourceID: o.ID, SourceKey: "comment:" + o.ID + ":" + hash(o.Body), SourceURL: o.URL, Title: o.Title, Context: o.Body})
		}
	}
	insights := Insights(s)
	for _, t := range s.Config.Topics {
		brief := t.Brief
		if insights != "" {
			brief += "\n" + insights
		}
		tasks = append(tasks, Task{Kind: "note", SourceID: t.ID, SourceKey: "note:" + t.ID, Title: t.Title, Context: brief})
	}
	seen := map[string]bool{}
	for _, d := range s.Drafts {
		seen[d.SourceKey] = true
	}
	out := []Task{}
	for _, t := range tasks {
		if !seen[t.SourceKey] {
			out = append(out, t)
		}
	}
	return out
}

func validateDecision(c Config, task Task, d Decision) error {
	if strings.TrimSpace(d.Reason) == "" || len(d.Reason) > 3000 {
		return fmt.Errorf("模型没有给出有效的行动依据")
	}
	if d.Action == "skip" || d.Action == "escalate" {
		return nil
	}
	if d.Action != "draft" {
		return fmt.Errorf("模型返回了不支持的行动，未保存")
	}
	if task.Kind == "note" && d.Title == "" {
		d.Title = task.Title
	}
	if err := (Draft{Platform: task.Platform, Kind: task.Kind, Title: d.Title, Body: d.Body, Status: "draft"}).Validate(); err != nil {
		return fmt.Errorf("模型草稿不符合要求: %w", err)
	}
	facts := map[string]bool{}
	for _, f := range c.Facts {
		facts[f.ID] = true
	}
	if len(d.FactIDs) == 0 || len(d.FactIDs) > len(c.Facts) || len(d.Assets) > 10 {
		return fmt.Errorf("模型草稿缺少产品事实依据或素材数量过多")
	}
	for _, id := range d.FactIDs {
		if !facts[id] {
			return fmt.Errorf("模型引用了不存在的产品事实，未保存")
		}
	}
	return nil
}

func (e *Engine) commit(revision int, task Task, d Decision) error {
	return e.Store.Update(func(s *State) error {
		if s.Revision != revision {
			return fmt.Errorf("角色或规则已更新，本次旧结果未保存")
		}
		found := false
		for _, current := range pending(*s) {
			if current.SourceKey == task.SourceKey {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("来源已更新或处理，旧结果未覆盖新上下文")
		}
		status := map[string]string{"draft": "drafted", "skip": "skipped", "escalate": "attention"}[d.Action]
		for i := range s.Opportunities {
			if s.Opportunities[i].ID == task.SourceID {
				s.Opportunities[i].Status, s.Opportunities[i].Reason = status, d.Reason
			}
		}
		for i := range s.Threads {
			if s.Threads[i].ID == task.SourceID {
				s.Threads[i].Status, s.Threads[i].Reason = status, d.Reason
			}
		}
		if d.Action == "draft" {
			if len(s.Drafts) >= 2000 {
				return fmt.Errorf("草稿已达 2000 份，请导出并归档数据")
			}
			now := time.Now().UTC()
			title := d.Title
			if title == "" {
				title = task.Title
			}
			draft := Draft{ID: newID("draft"), Platform: task.Platform, Kind: task.Kind, Title: title, Body: d.Body, Assets: d.Assets, FactIDs: d.FactIDs, SourceKey: task.SourceKey, SourceURL: task.SourceURL, SourceID: task.SourceID, Origin: "model", Status: "draft", Reason: d.Reason, CreatedAt: now, UpdatedAt: now}
			// The send target comes from what was read off the platform, not the model.
			for _, o := range s.Opportunities {
				if o.ID == task.SourceID {
					draft.FeedID, draft.Token = o.FeedID, o.Token
				}
			}
			for _, t := range s.Threads {
				if t.ID == task.SourceID {
					draft.FeedID, draft.Token, draft.CommentID, draft.RootID = t.FeedID, t.Token, t.CommentID, t.RootID
				}
			}
			s.Drafts = append(s.Drafts, draft)
		} else if task.Kind == "note" {
			// Remember a topic decision without presenting it as publishable content.
			s.Drafts = append(s.Drafts, Draft{ID: newID("decision"), Kind: "note", Title: task.Title, Body: d.Reason, SourceKey: task.SourceKey, SourceID: task.SourceID, Origin: "decision", Status: "archived", Reason: d.Reason, CreatedAt: time.Now().UTC()})
		}
		return nil
	})
}

func (e *Engine) discover(ctx context.Context) (int, error) {
	s := e.Store.Snapshot()
	if s.Config.AccountID == "" || e.Reader == nil {
		return 0, fmt.Errorf("请先在角色设置中填写浏览器扩展对应的平台账号标识，并启动 OmniMCP")
	}
	raw, err := e.Reader.Call(ctx, "xiaohongshu_check_login", map[string]any{"_account_id": s.Config.AccountID})
	if err != nil {
		return 0, err
	}
	var login struct {
		LoggedIn bool `json:"logged_in"`
	}
	if json.Unmarshal(raw, &login) != nil || !login.LoggedIn {
		return 0, fmt.Errorf("平台账号尚未登录，请先完成浏览器登录")
	}
	added := 0
	seen := map[string]bool{}
	for _, o := range s.Opportunities {
		seen[o.FeedID] = o.FeedID != ""
	}
	for qi, query := range s.Config.Queries {
		if qi >= 3 || added >= 5 {
			break
		}
		raw, err := e.Reader.Call(ctx, "xiaohongshu_search_notes", map[string]any{"_account_id": s.Config.AccountID, "keyword": query, "limit": 5})
		if err != nil {
			return added, err
		}
		var results struct {
			Items []struct {
				ID    string `json:"id"`
				Token string `json:"xsecToken"`
			} `json:"items"`
		}
		if json.Unmarshal(raw, &results) != nil {
			return added, fmt.Errorf("无法解析平台搜索结果")
		}
		for _, item := range results.Items {
			if added >= 5 {
				break
			}
			if item.ID == "" || item.Token == "" || seen[item.ID] {
				continue
			}
			raw, err := e.Reader.Call(ctx, "xiaohongshu_get_note_detail", map[string]any{"_account_id": s.Config.AccountID, "feed_id": item.ID, "xsec_token": item.Token, "load_comments": false})
			if err != nil {
				return added, err
			}
			var detail struct {
				Note struct {
					Title string `json:"title"`
					Desc  string `json:"desc"`
				} `json:"note"`
				Comments struct {
					List []struct {
						Content string `json:"content"`
					} `json:"list"`
				} `json:"comments"`
			}
			if json.Unmarshal(raw, &detail) != nil || strings.TrimSpace(detail.Note.Desc) == "" {
				continue
			}
			body := detail.Note.Desc
			for i, c := range detail.Comments.List {
				if i >= 5 {
					break
				}
				body += "\n已加载的评论：" + c.Content
			}
			if len([]rune(body)) > 8000 {
				body = string([]rune(body)[:8000])
			}
			o := Opportunity{ID: newID("opportunity"), URL: "https://www.xiaohongshu.com/explore/" + url.PathEscape(item.ID) + "?xsec_token=" + url.QueryEscape(item.Token) + "&xsec_source=pc_search", FeedID: item.ID, Token: item.Token, Title: detail.Note.Title, Body: body, Origin: "platform", Status: "pending", CreatedAt: time.Now().UTC()}
			if err := e.Store.Update(func(current *State) error {
				if current.Revision != s.Revision {
					return fmt.Errorf("配置已更新，搜索停止")
				}
				if len(current.Opportunities) >= 1000 {
					return fmt.Errorf("候选帖子已达 1000 条，请先归档数据")
				}
				for _, existing := range current.Opportunities {
					if existing.FeedID == item.ID {
						return nil
					}
				}
				current.Opportunities = append(current.Opportunities, o)
				return nil
			}); err != nil {
				return added, err
			}
			seen[item.ID], added = true, added+1
		}
	}
	return added, nil
}
