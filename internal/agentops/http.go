package agentops

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed web/*
var web embed.FS

type App struct{ Engine *Engine }

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	files, _ := fs.Sub(web, "web")
	mux.Handle("GET /", http.FileServer(http.FS(files)))
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, map[string]any{"state": a.Engine.Store.Snapshot(), "running": a.Engine.Busy(), "model_configured": a.Engine.Generator != nil, "model": a.Engine.Model, "runtime_error": a.Engine.LastError(), "attention": Attention(a.Engine.Store.Snapshot()), "platforms": platformList(), "approvals_url": a.Engine.ApprovalsURL(), "scorecards": Scorecards(a.Engine.Store.Snapshot()), "style": styleWarnings(a.Engine.Store.Snapshot())})
	})
	mux.HandleFunc("PUT /api/config", a.config)
	mux.HandleFunc("POST /api/run", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Mode string `json:"mode"`
		}
		if !decode(w, r, &in) {
			return
		}
		id, err := a.Engine.Start("manual", in.Mode)
		if err != nil {
			fail(w, http.StatusConflict, err)
			return
		}
		reply(w, http.StatusAccepted, map[string]string{"id": id})
	})
	mux.HandleFunc("POST /api/cancel", func(w http.ResponseWriter, r *http.Request) {
		a.Engine.Cancel()
		reply(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/opportunities", a.opportunity)
	mux.HandleFunc("POST /api/threads", a.thread)
	mux.HandleFunc("POST /api/threads/{id}/messages", a.message)
	mux.HandleFunc("PATCH /api/drafts/{id}", a.draft)
	mux.HandleFunc("POST /api/drafts/{id}/submit", a.submit)
	mux.HandleFunc("POST /api/drafts/{id}/adapt", a.adapt)
	mux.HandleFunc("POST /api/approvals/{id}/{action}", a.decide)
	mux.HandleFunc("GET /api/assets", a.assets)
	mux.HandleFunc("POST /api/assets", a.uploadAsset)
	mux.HandleFunc("GET /assets/{name}", a.serveAsset)
	mux.HandleFunc("GET /api/export", a.export)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		}
		if host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
			fail(w, http.StatusForbidden, fmt.Errorf("请使用本机回环地址访问工作台"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
					fail(w, http.StatusForbidden, fmt.Errorf("请从运营工作台发起操作"))
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				fail(w, http.StatusForbidden, fmt.Errorf("操作须由工作台以 JSON 提交"))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("请求内容无效或过大"))
		return false
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		fail(w, http.StatusBadRequest, fmt.Errorf("请求只能包含一个 JSON 对象"))
		return false
	}
	return true
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = writeJSON(w, v)
}

func fail(w http.ResponseWriter, status int, err error) {
	reply(w, status, map[string]string{"error": err.Error()})
}

func (a *App) config(w http.ResponseWriter, r *http.Request) {
	var c Config
	if !decode(w, r, &c) {
		return
	}
	if err := c.Validate(); err != nil {
		fail(w, 400, err)
		return
	}
	if c.Automation && a.Engine.Generator == nil {
		fail(w, 400, fmt.Errorf("请先连接模型，再开启自动准备"))
		return
	}
	if err := a.Engine.Store.Update(func(s *State) error {
		s.Config = c
		s.Revision++
		s.NextRunAt = time.Now().UTC().Add(time.Duration(c.IntervalMinutes) * time.Minute)
		return nil
	}); err != nil {
		fail(w, 500, err)
		return
	}
	// Any configuration edit invalidates in-flight generation; stop promptly.
	a.Engine.Cancel()
	reply(w, 200, map[string]bool{"ok": true})
}

func (a *App) opportunity(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL   string `json:"url"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if !decode(w, r, &in) {
		return
	}
	u, err := noteURL(in.URL)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if strings.TrimSpace(in.Title) == "" || len(in.Title) > 500 || strings.TrimSpace(in.Body) == "" || utf8.RuneCountInString(in.Body) > 8000 {
		fail(w, 400, fmt.Errorf("请填写帖子标题与正文，正文最多 8000 字"))
		return
	}
	o := Opportunity{ID: newID("opportunity"), URL: u.String(), Title: in.Title, Body: in.Body, Origin: "manual", Status: "pending", CreatedAt: time.Now().UTC()}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Hostname() != "xhslink.com" && len(parts) == 2 && (parts[0] == "explore" || parts[0] == "discovery") {
		o.FeedID, o.Token = parts[1], u.Query().Get("xsec_token")
	}
	if err := a.Engine.Store.Update(func(s *State) error {
		if len(s.Opportunities) >= 1000 {
			return fmt.Errorf("候选帖子已达 1000 条")
		}
		for _, old := range s.Opportunities {
			oldURL, _ := url.Parse(old.URL)
			if (oldURL != nil && oldURL.Hostname() == u.Hostname() && oldURL.Path == u.Path) || (o.FeedID != "" && old.FeedID == o.FeedID) {
				return fmt.Errorf("这个帖子已经在机会列表中")
			}
		}
		s.Opportunities = append(s.Opportunities, o)
		return nil
	}); err != nil {
		fail(w, 409, err)
		return
	}
	reply(w, 201, o)
}

func (a *App) thread(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL     string `json:"url"`
		Title   string `json:"title"`
		Context string `json:"context"`
		Author  string `json:"author"`
		Message string `json:"message"`
	}
	if !decode(w, r, &in) {
		return
	}
	u, err := noteURL(in.URL)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if strings.TrimSpace(in.Title) == "" || len(in.Title) > 500 || len(in.Context) > 20000 || !validMessage(in.Author, in.Message) {
		fail(w, 400, fmt.Errorf("请填写对话标题、对方称呼与消息；上下文或消息不能过长"))
		return
	}
	now := time.Now().UTC()
	t := Thread{ID: newID("thread"), URL: u.String(), Title: in.Title, Context: in.Context, Status: "pending", UpdatedAt: now, Messages: []Message{{ID: newID("message"), Author: in.Author, Text: in.Message, CreatedAt: now}}}
	if err := a.Engine.Store.Update(func(s *State) error {
		if len(s.Threads) >= 500 {
			return fmt.Errorf("跟进对话已达 500 条")
		}
		s.Threads = append(s.Threads, t)
		return nil
	}); err != nil {
		fail(w, 409, err)
		return
	}
	reply(w, 201, t)
}

func validMessage(author, text string) bool {
	return strings.TrimSpace(author) != "" && len(author) <= 200 && strings.TrimSpace(text) != "" && utf8.RuneCountInString(text) <= 3000
}

func (a *App) message(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Author string `json:"author"`
		Text   string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validMessage(in.Author, in.Text) {
		fail(w, 400, fmt.Errorf("请填写对方称呼和消息，消息最多 3000 字"))
		return
	}
	if err := a.Engine.Store.Update(func(s *State) error {
		for i := range s.Threads {
			t := &s.Threads[i]
			if t.ID != r.PathValue("id") {
				continue
			}
			if len(t.Messages) >= 30 {
				return fmt.Errorf("该对话已有 30 条消息，请整理上下文后新建跟进")
			}
			if len(t.Messages) > 0 {
				last := t.Messages[len(t.Messages)-1]
				if last.Author == in.Author && last.Text == in.Text {
					return fmt.Errorf("这条消息已经记录")
				}
			}
			now := time.Now().UTC()
			t.Messages = append(t.Messages, Message{ID: newID("message"), Author: in.Author, Text: in.Text, CreatedAt: now})
			t.Status, t.Reason, t.UpdatedAt = "pending", "", now
			return nil
		}
		return fmt.Errorf("对话不存在")
	}); err != nil {
		fail(w, 409, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}

func (a *App) draft(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title       string    `json:"title"`
		Body        string    `json:"body"`
		Status      string    `json:"status"`
		Platform    *string   `json:"platform"`
		Hypothesis  *string   `json:"hypothesis"`
		AssetPaths  *[]string `json:"asset_paths"`
		ScheduledAt *string   `json:"scheduled_at"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := a.Engine.Store.Update(func(s *State) error {
		for i := range s.Drafts {
			if s.Drafts[i].ID != r.PathValue("id") {
				continue
			}
			d := &s.Drafts[i]
			if d.Origin == "decision" {
				return fmt.Errorf("这是行动判断记录")
			}
			if openApproval(d.Approval) {
				return fmt.Errorf("这份内容正在确认队列中；请先驳回，或到确认页修改")
			}
			if d.Status == "sent" {
				return fmt.Errorf("这份内容已经发送，不能再修改")
			}
			if in.Status == "sent" {
				return fmt.Errorf("发送状态由平台回执决定")
			}
			if in.Platform != nil {
				d.Platform = *in.Platform
			}
			if in.AssetPaths != nil {
				d.AssetPaths = *in.AssetPaths
			}
			if in.Hypothesis != nil {
				d.Hypothesis = *in.Hypothesis
			}
			if in.ScheduledAt != nil {
				d.ScheduledAt = time.Time{}
				if *in.ScheduledAt != "" {
					at, err := time.Parse(time.RFC3339, *in.ScheduledAt)
					if err != nil {
						return fmt.Errorf("排期时间无效")
					}
					d.ScheduledAt = at.UTC()
				}
			}
			d.Title, d.Body, d.Status, d.UpdatedAt, d.ScheduleError = in.Title, in.Body, in.Status, time.Now().UTC(), ""
			// A decision on old content says nothing about the new content.
			if d.Approval != nil && (d.Approval.Status == "rejected" || d.Approval.Status == "expired") {
				d.Approval = nil
			}
			return d.Validate()
		}
		return fmt.Errorf("草稿不存在")
	}); err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}

// styleWarnings is computed on read, so editing a rule applies to existing drafts.
func styleWarnings(s State) map[string][]string {
	out := map[string][]string{}
	for _, d := range s.Drafts {
		if d.Origin == "decision" || d.Status == "archived" || d.Status == "sent" {
			continue
		}
		if w := StyleCheck(d.Kind, d.Body); len(w) > 0 {
			out[d.ID] = w
		}
	}
	return out
}

func platformList() []map[string]any {
	out := []map[string]any{}
	for id, p := range platforms {
		out = append(out, map[string]any{"id": id, "name": p.Name, "title_max": p.TitleMax, "body_max": p.BodyMax, "inbox": p.Inbox})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["id"].(string) < out[j]["id"].(string) })
	return out
}

func (a *App) submit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	approval, err := a.Engine.Submit(r.Context(), r.PathValue("id"), in.Mode)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	reply(w, http.StatusOK, approval)
}

func (a *App) adapt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Platform string `json:"platform"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	d, err := a.Engine.Adapt(ctx, r.PathValue("id"), in.Platform)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	reply(w, http.StatusCreated, d)
}

// decide relays a click on this page. The cross-site checks in Handler apply,
// and the model pipeline has no path to this handler.
func (a *App) decide(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Hash   string `json:"hash"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if a.Engine.Decider == nil {
		fail(w, http.StatusConflict, fmt.Errorf("没有连接 OmniMCP"))
		return
	}
	body := map[string]any{"hash": in.Hash}
	if r.PathValue("action") == "reject" {
		body = map[string]any{"reason": in.Reason}
	}
	if err := a.Engine.Decider.Decide(r.Context(), r.PathValue("id"), r.PathValue("action"), body); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	_ = a.Engine.SyncApprovals(r.Context())
	reply(w, http.StatusOK, map[string]bool{"ok": true})
}

var imageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif"}

func (a *App) assets(w http.ResponseWriter, r *http.Request) {
	dir := a.Engine.Store.AssetDir()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		fail(w, 500, err)
		return
	}
	out := []map[string]any{}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		_, image := imageTypes[strings.ToLower(filepath.Ext(e.Name()))]
		out = append(out, map[string]any{"name": e.Name(), "path": filepath.Join(dir, e.Name()), "image": image, "video": isVideo(e.Name())})
	}
	reply(w, 200, map[string]any{"dir": dir, "items": out})
}

// uploadAsset stores an image in the library. Videos are usually too large for
// a JSON body, so they are copied into the asset directory directly.
func (a *App) uploadAsset(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 40<<20)
	var in struct {
		Name string `json:"name"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, fmt.Errorf("素材无效或超过 30 MB"))
		return
	}
	name := filepath.Base(strings.TrimSpace(in.Name))
	if _, ok := imageTypes[strings.ToLower(filepath.Ext(name))]; !ok || strings.HasPrefix(name, ".") {
		fail(w, 400, fmt.Errorf("只能上传 png、jpg、webp 或 gif 图片"))
		return
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil || len(data) == 0 {
		fail(w, 400, fmt.Errorf("素材内容无效"))
		return
	}
	dir := a.Engine.Store.AssetDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail(w, 500, err)
		return
	}
	path := filepath.Join(dir, newID("asset")+"-"+name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fail(w, 500, err)
		return
	}
	reply(w, 201, map[string]string{"name": filepath.Base(path), "path": path})
}

func (a *App) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	contentType, ok := imageTypes[strings.ToLower(filepath.Ext(name))]
	if !ok || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeFile(w, r, filepath.Join(a.Engine.Store.AssetDir(), name))
}

func (a *App) export(w http.ResponseWriter, r *http.Request) {
	s := a.Engine.Store.Snapshot()
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="agentops-drafts.md"`)
	fmt.Fprintf(w, "# %s · 上线前内容\n\n导出时间：%s\n\n", s.Config.Product, time.Now().UTC().Format(time.RFC3339))
	for _, d := range s.Drafts {
		if d.Status == "archived" {
			continue
		}
		fmt.Fprintf(w, "## %s\n\n%s\n\n", d.Title, d.Body)
		if len(d.Assets) > 0 {
			fmt.Fprintln(w, "待准备素材：")
			for _, asset := range d.Assets {
				fmt.Fprintf(w, "\n- %s\n", asset)
			}
			fmt.Fprintln(w)
		}
	}
}
