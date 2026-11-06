package agentops

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type Fact struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Evidence string `json:"evidence"`
}

type Topic struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Brief string `json:"brief"`
}

type Config struct {
	Name            string   `json:"name"`
	Product         string   `json:"product"`
	Stage           string   `json:"stage"`
	Entry           string   `json:"entry"`
	Persona         string   `json:"persona"`
	Goal            string   `json:"goal"`
	AccountID       string   `json:"account_id"`
	Facts           []Fact   `json:"facts"`
	Queries         []string `json:"queries"`
	Rules           []string `json:"rules"`
	Topics          []Topic  `json:"topics"`
	Automation      bool     `json:"automation"`
	Discover        bool     `json:"discover"`
	IntervalMinutes int      `json:"interval_minutes"`
	MaxTasks        int      `json:"max_tasks"`
	// Accounts binds further platforms; AccountID stays the Xiaohongshu binding.
	Accounts []PlatformAccount `json:"accounts,omitempty"`
	Watch    []WatchTarget     `json:"watch,omitempty"`
}

type PlatformAccount struct {
	Platform  string `json:"platform"`
	AccountID string `json:"account_id"`
}

// WatchTarget is a public account tracked for comparison.
type WatchTarget struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	UserID   string `json:"user_id"`
	Token    string `json:"xsec_token,omitempty"`
}

// Account returns the account bound to a platform, or "" when none is.
func (c Config) Account(platform string) string {
	if platform == "xiaohongshu" && c.AccountID != "" {
		return c.AccountID
	}
	for _, a := range c.Accounts {
		if a.Platform == platform {
			return a.AccountID
		}
	}
	return ""
}

func (c Config) Validate() error {
	if c.Stage != "prelaunch" && c.Stage != "live" {
		return fmt.Errorf("运营阶段须为 prelaunch 或 live")
	}
	if len(c.Accounts) > 20 || len(c.Watch) > 50 {
		return fmt.Errorf("最多绑定 20 个平台账号、跟踪 50 个对标账号")
	}
	for _, a := range c.Accounts {
		if _, ok := platforms[a.Platform]; !ok || strings.TrimSpace(a.AccountID) == "" || len(a.AccountID) > 100 {
			return fmt.Errorf("平台账号绑定无效: %s", a.Platform)
		}
	}
	for _, w := range c.Watch {
		if _, ok := platforms[w.Platform]; !ok || strings.TrimSpace(w.Name) == "" || strings.TrimSpace(w.UserID) == "" {
			return fmt.Errorf("对标账号须填写名称、平台和用户 ID")
		}
	}
	if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Product) == "" || strings.TrimSpace(c.Persona) == "" {
		return fmt.Errorf("请填写角色名称、产品和角色说明")
	}
	if len(c.Persona) > 10000 || len(c.Goal) > 10000 || len(c.Entry) > 2000 || len(c.AccountID) > 100 {
		return fmt.Errorf("配置内容过长")
	}
	if c.IntervalMinutes < 5 || c.IntervalMinutes > 1440 || c.MaxTasks < 1 || c.MaxTasks > 10 {
		return fmt.Errorf("运行间隔须为 5–1440 分钟，每轮处理 1–10 个任务")
	}
	if len(c.Queries) > 20 || len(c.Topics) > 100 || len(c.Facts) == 0 || len(c.Facts) > 100 || len(c.Rules) > 50 {
		return fmt.Errorf("请保留 1–100 条知识、最多 20 个搜索词、100 个选题和 50 条规则")
	}
	ids := map[string]bool{}
	for _, f := range c.Facts {
		if f.ID == "" || ids[f.ID] || strings.TrimSpace(f.Text) == "" || len(f.Text) > 10000 {
			return fmt.Errorf("产品知识的编号须唯一，内容不能为空或过长")
		}
		ids[f.ID] = true
	}
	ids = map[string]bool{}
	for _, t := range c.Topics {
		if t.ID == "" || len(t.ID) > 100 || ids[t.ID] || strings.TrimSpace(t.Title) == "" || utf8.RuneCountInString(t.Title) > 20 || len(t.Brief) > 10000 {
			return fmt.Errorf("选题的编号须唯一，标题不能为空，说明不能过长")
		}
		ids[t.ID] = true
	}
	for _, v := range append(append([]string{}, c.Queries...), c.Rules...) {
		if strings.TrimSpace(v) == "" || len(v) > 3000 {
			return fmt.Errorf("搜索词与规则不能为空或过长")
		}
	}
	if c.Discover && strings.TrimSpace(c.AccountID) == "" {
		return fmt.Errorf("开启定时搜索前，请先绑定平台账号标识")
	}
	return nil
}

type Opportunity struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	FeedID    string    `json:"feed_id"`
	Token     string    `json:"xsec_token,omitempty"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Origin    string    `json:"origin"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

type Message struct {
	ID         string    `json:"id"`
	Author     string    `json:"author"`
	Text       string    `json:"text"`
	Mine       bool      `json:"mine,omitempty"`
	PlatformID string    `json:"platform_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type Thread struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Context   string    `json:"context"`
	Messages  []Message `json:"messages"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
	// Set for threads imported from a platform inbox; a reply needs these targets.
	Key       string `json:"key,omitempty"`
	Platform  string `json:"platform,omitempty"`
	FeedID    string `json:"feed_id,omitempty"`
	Token     string `json:"xsec_token,omitempty"`
	CommentID string `json:"comment_id,omitempty"`
	RootID    string `json:"root_id,omitempty"`
	AuthorID  string `json:"author_id,omitempty"`
}

// DraftApproval tracks a draft's entry in the OmniMCP confirmation queue.
type DraftApproval struct {
	ID          string    `json:"id"`
	Hash        string    `json:"hash"`
	Status      string    `json:"status"`
	Mode        string    `json:"mode"`
	Tool        string    `json:"tool"`
	Account     string    `json:"account"`
	Error       string    `json:"error,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type Draft struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Assets    []string  `json:"assets"`
	FactIDs   []string  `json:"fact_ids"`
	SourceKey string    `json:"source_key"`
	SourceURL string    `json:"source_url"`
	SourceID  string    `json:"source_id"`
	Origin    string    `json:"origin"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Hypothesis says what this post is testing, so its result can teach something.
	Hypothesis    string         `json:"hypothesis,omitempty"`
	Platform      string         `json:"platform,omitempty"`
	FeedID        string         `json:"feed_id,omitempty"`
	Token         string         `json:"xsec_token,omitempty"`
	CommentID     string         `json:"comment_id,omitempty"`
	RootID        string         `json:"root_id,omitempty"`
	AssetPaths    []string       `json:"asset_paths,omitempty"`
	AdaptedFrom   string         `json:"adapted_from,omitempty"`
	ScheduledAt   time.Time      `json:"scheduled_at,omitzero"`
	ScheduleError string         `json:"schedule_error,omitempty"`
	Approval      *DraftApproval `json:"approval,omitempty"`
	SentAt        time.Time      `json:"sent_at,omitzero"`
	PlatformID    string         `json:"platform_id,omitempty"`
	PlatformURL   string         `json:"platform_url,omitempty"`
	Receipt       string         `json:"receipt,omitempty"`
}

// platform defaults to Xiaohongshu, the only platform of version 1 states.
func (d Draft) platform() string {
	if d.Platform == "" {
		return "xiaohongshu"
	}
	return d.Platform
}

func (d Draft) Validate() error {
	if d.Kind != "note" && d.Kind != "comment" && d.Kind != "reply" {
		return fmt.Errorf("未知草稿类型")
	}
	info, ok := platforms[d.platform()]
	if !ok {
		return fmt.Errorf("不支持的平台 %s", d.platform())
	}
	if strings.TrimSpace(d.Body) == "" || utf8.RuneCountInString(d.Body) > info.BodyMax {
		return fmt.Errorf("正文不能为空，%s最多 %d 字", info.Name, info.BodyMax)
	}
	if d.Kind == "note" && (strings.TrimSpace(d.Title) == "" || utf8.RuneCountInString(d.Title) > info.TitleMax) {
		return fmt.Errorf("%s标题须为 1–%d 字", info.Name, info.TitleMax)
	}
	if d.Status != "draft" && d.Status != "ready" && d.Status != "archived" && d.Status != "sent" {
		return fmt.Errorf("草稿状态须为 draft、ready、archived 或 sent")
	}
	if len(d.AssetPaths) > 18 {
		return fmt.Errorf("每份内容最多 18 个素材")
	}
	return nil
}

type Run struct {
	ID        string    `json:"id"`
	Trigger   string    `json:"trigger"`
	Status    string    `json:"status"`
	Summary   string    `json:"summary"`
	Drafts    int       `json:"drafts"`
	Skipped   int       `json:"skipped"`
	Escalated int       `json:"escalated"`
	Tokens    int       `json:"tokens"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

type Health struct {
	LoggedIn  bool      `json:"logged_in"`
	Nickname  string    `json:"nickname,omitempty"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// InboxStatus records how far the last read got; a partial read is not "no news".
type InboxStatus struct {
	CheckedAt   time.Time `json:"checked_at"`
	LastSuccess time.Time `json:"last_success,omitzero"`
	Loaded      int       `json:"loaded"`
	Complete    bool      `json:"complete"`
	Error       string    `json:"error,omitempty"`
}

type NoteStat struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Views    int64  `json:"views,omitempty"`
	Likes    int64  `json:"likes"`
	Collects int64  `json:"collects"`
	Comments int64  `json:"comments"`
	Shares   int64  `json:"shares,omitempty"`
}

// MetricSample is one timestamped reading; Watch names a tracked account.
type MetricSample struct {
	At       time.Time         `json:"at"`
	Platform string            `json:"platform"`
	Watch    string            `json:"watch,omitempty"`
	Stats    map[string]string `json:"stats,omitempty"`
	Notes    []NoteStat        `json:"notes,omitempty"`
}

type AttentionItem struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	ID   string `json:"id"`
}

type State struct {
	Version       int                    `json:"version"`
	Revision      int                    `json:"revision"`
	Config        Config                 `json:"config"`
	Opportunities []Opportunity          `json:"opportunities"`
	Threads       []Thread               `json:"threads"`
	Drafts        []Draft                `json:"drafts"`
	Runs          []Run                  `json:"runs"`
	NextRunAt     time.Time              `json:"next_run_at"`
	LastSyncAt    time.Time              `json:"last_sync_at,omitzero"`
	SeenEvents    []string               `json:"seen_events,omitempty"`
	Inbox         map[string]InboxStatus `json:"inbox,omitempty"`
	Health        map[string]Health      `json:"health,omitempty"`
	Samples       []MetricSample         `json:"samples,omitempty"`
}

func newID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func noteURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Hostname() != "www.xiaohongshu.com" && u.Hostname() != "xiaohongshu.com" && u.Hostname() != "xhslink.com") {
		return nil, fmt.Errorf("请填写完整的小红书 https 帖子链接")
	}
	return u, nil
}
