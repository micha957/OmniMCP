package agentops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeMCP answers reads from a table and records proposals. It has no way to
// approve, which is the property these tests rely on.
type fakeMCP struct {
	reads     map[string]string
	errs      map[string]error
	proposals []proposal
	status    string
}

type proposal struct {
	tool    string
	args    map[string]any
	account string
}

func (f *fakeMCP) Call(_ context.Context, name string, _ map[string]any) (json.RawMessage, error) {
	if err := f.errs[name]; err != nil {
		return nil, err
	}
	if v, ok := f.reads[name]; ok {
		return json.RawMessage(v), nil
	}
	return nil, fmt.Errorf("unexpected read %s", name)
}

func (f *fakeMCP) Propose(_ context.Context, tool string, args map[string]any, account, _ string) (Proposal, error) {
	f.proposals = append(f.proposals, proposal{tool, args, account})
	return Proposal{ID: "apr_1", Hash: "h1", Status: "pending"}, nil
}

func opsEngine(t *testing.T, f *fakeMCP, mutate func(*State)) *Engine {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(s *State) error {
		s.Config.AccountID = "main"
		s.Drafts = nil
		if mutate != nil {
			mutate(s)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return NewEngine(context.Background(), store, nil, f, "")
}

func draftByID(t *testing.T, e *Engine, id string) Draft {
	t.Helper()
	for _, d := range e.Store.Snapshot().Drafts {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("draft %s missing", id)
	return Draft{}
}

func TestSubmitOnlyQueuesAndUsesPlatformTarget(t *testing.T) {
	f := &fakeMCP{}
	e := opsEngine(t, f, func(s *State) {
		s.Drafts = []Draft{{ID: "d1", Kind: "reply", Body: "可以这样做", Status: "draft", Origin: "model", FeedID: "n1", Token: "tok", CommentID: "c9"}}
	})
	a, err := e.Submit(context.Background(), "d1", "publish")
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "pending" || len(f.proposals) != 1 {
		t.Fatalf("%+v %+v", a, f.proposals)
	}
	p := f.proposals[0]
	if p.tool != "xiaohongshu_reply_comment" || p.account != "main" || p.args["comment_id"] != "c9" || p.args["feed_id"] != "n1" || p.args["xsec_token"] != "tok" || p.args["content"] != "可以这样做" {
		t.Fatalf("wrong proposal: %+v", p)
	}
	if d := draftByID(t, e, "d1"); d.Status == "sent" || d.Approval.ID != "apr_1" {
		t.Fatalf("submitting must not mark content sent: %+v", d)
	}
	if _, err := e.Submit(context.Background(), "d1", "publish"); err == nil {
		t.Fatal("a draft already in the queue must not be submitted again")
	}
}

func TestBuildActionRefusesWhatCannotRun(t *testing.T) {
	cases := map[string]Draft{
		"note without image": {Kind: "note", Title: "t", Body: "b"},
		"missing asset":      {Kind: "note", Title: "t", Body: "b", AssetPaths: []string{"/nonexistent/a.png"}},
		"relative asset":     {Kind: "note", Title: "t", Body: "b", AssetPaths: []string{"a.png"}},
		"reply without id":   {Kind: "reply", Body: "b", FeedID: "n", Token: "t"},
		"comment no target":  {Kind: "comment", Body: "b"},
		"video without file": {Kind: "note", Platform: "douyin", Title: "t", Body: "b"},
		"unsupported reply":  {Kind: "reply", Platform: "weibo", Body: "b", FeedID: "n", CommentID: "c"},
		"unknown platform":   {Kind: "note", Platform: "myspace", Title: "t", Body: "b"},
	}
	for name, d := range cases {
		if tool, _, err := BuildAction(d, "publish"); err == nil {
			t.Errorf("%s: expected refusal, got %s", name, tool)
		}
	}
	if _, _, err := BuildAction(Draft{Kind: "note", Title: "t", Body: "b", Platform: "zhihu"}, "now"); err == nil {
		t.Error("unknown mode must be refused")
	}
}

func TestBuildActionPicksToolByAsset(t *testing.T) {
	dir := t.TempDir()
	image, video := filepath.Join(dir, "a.png"), filepath.Join(dir, "b.mp4")
	for _, f := range []string{image, video} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tool, args, err := BuildAction(Draft{Kind: "note", Title: "t", Body: "b", AssetPaths: []string{image}}, "draft")
	if err != nil || tool != "xiaohongshu_publish_note" || args["mode"] != "draft" {
		t.Fatalf("%s %v %v", tool, args, err)
	}
	if tool, _, err = BuildAction(Draft{Kind: "note", Title: "t", Body: "b", AssetPaths: []string{video}}, "publish"); err != nil || tool != "xiaohongshu_publish_video" {
		t.Fatalf("%s %v", tool, err)
	}
	if !writeTools[tool] {
		t.Fatalf("%s must be allowlisted", tool)
	}
}

func TestEveryBuiltToolIsAllowlisted(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "b.mp4")
	if err := os.WriteFile(video, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for platform := range platforms {
		for _, kind := range []string{"note", "comment", "reply"} {
			d := Draft{Kind: kind, Platform: platform, Title: "t", Body: "b", FeedID: "f", Token: "t", CommentID: "c", SourceURL: "https://x", AssetPaths: []string{video}}
			tool, _, err := BuildAction(d, "publish")
			if err == nil && !writeTools[tool] {
				t.Errorf("%s/%s builds %s, which MCPReader would refuse", platform, kind, tool)
			}
		}
	}
}

func TestSuccessfulOutcomeWritesReceiptBackOnce(t *testing.T) {
	f := &fakeMCP{reads: map[string]string{"omnimcp_approval_status": `{"status":"succeeded","result":{"comment_id":"r7"}}`}}
	e := opsEngine(t, f, func(s *State) {
		s.Threads = []Thread{{ID: "t1", Status: "drafted", Messages: []Message{{ID: "m", Author: "小A", Text: "怎么用"}}}}
		s.Drafts = []Draft{{ID: "d1", Kind: "reply", Body: "这样用", Status: "draft", SourceID: "t1", Approval: &DraftApproval{ID: "apr_1", Status: "executing", Mode: "publish"}}}
	})
	for i := 0; i < 2; i++ {
		if err := e.SyncApprovals(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	d := draftByID(t, e, "d1")
	if d.Status != "sent" || d.PlatformID != "r7" || d.SentAt.IsZero() {
		t.Fatalf("receipt not recorded: %+v", d)
	}
	thread := e.Store.Snapshot().Threads[0]
	if len(thread.Messages) != 2 || !thread.Messages[1].Mine || thread.Messages[1].PlatformID != "r7" || thread.Status != "answered" {
		t.Fatalf("thread: %+v", thread)
	}
}

func TestPlatformDraftStaysEditable(t *testing.T) {
	f := &fakeMCP{reads: map[string]string{"omnimcp_approval_status": `{"status":"succeeded","result":{"status":"draft_saved"}}`}}
	e := opsEngine(t, f, func(s *State) {
		s.Drafts = []Draft{{ID: "d1", Kind: "note", Title: "t", Body: "b", Status: "ready", Approval: &DraftApproval{ID: "a", Status: "pending", Mode: "draft"}}}
	})
	if err := e.SyncApprovals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := draftByID(t, e, "d1"); d.Status != "ready" {
		t.Fatalf("saving a platform draft is not publishing: %+v", d)
	}
}

func TestInboxImportIsIdempotentAndFollowsLatestComment(t *testing.T) {
	first := `{"complete":false,"loaded":1,"items":[{"id":"e1","from":{"user_id":"u1","nickname":"小A"},"comment_id":"c1","comment_text":"怎么用","feed_id":"n1","feed_xsec_token":"tok","feed_title":"笔记"}]}`
	f := &fakeMCP{reads: map[string]string{"xiaohongshu_get_notifications": first}}
	e := opsEngine(t, f, nil)
	ctx := context.Background()
	for i, want := range []int{1, 0} {
		added, err := e.SyncInbox(ctx)
		if err != nil || added != want {
			t.Fatalf("round %d: added=%d err=%v", i, added, err)
		}
	}
	s := e.Store.Snapshot()
	if len(s.Threads) != 1 || s.Threads[0].CommentID != "c1" || s.Threads[0].Token != "tok" || s.Threads[0].Status != "pending" {
		t.Fatalf("thread: %+v", s.Threads)
	}
	if s.Inbox["xiaohongshu"].Complete || s.Inbox["xiaohongshu"].LastSuccess.IsZero() {
		t.Fatalf("partial read must be recorded: %+v", s.Inbox)
	}
	if err := e.Store.Update(func(st *State) error { st.Threads[0].Status = "answered"; return nil }); err != nil {
		t.Fatal(err)
	}
	f.reads["xiaohongshu_get_notifications"] = strings.Replace(first, `"comment_id":"c1","comment_text":"怎么用"`, `"comment_id":"c2","comment_text":"还有问题"`, 1)
	if added, err := e.SyncInbox(ctx); err != nil || added != 1 {
		t.Fatalf("added=%d err=%v", added, err)
	}
	s = e.Store.Snapshot()
	if len(s.Threads) != 1 || s.Threads[0].CommentID != "c2" || s.Threads[0].Status != "pending" || len(s.Threads[0].Messages) != 2 {
		t.Fatalf("a new reply must reopen the thread and retarget it: %+v", s.Threads[0])
	}
}

func TestOldNotificationsAreNotImported(t *testing.T) {
	st := &State{}
	old := inboxItem{CommentID: "old", CommentText: "三年前的评论", Time: time.Now().AddDate(-3, 0, 0).Unix()}
	fresh := inboxItem{CommentID: "new", CommentText: "刚刚的评论", Time: time.Now().Add(-time.Hour).UnixMilli()}
	if added := mergeInbox(st, "bilibili", []inboxItem{old, fresh}); added != 1 || len(st.Threads) != 1 || st.Threads[0].CommentID != "new" {
		t.Fatalf("added=%d threads=%+v", added, st.Threads)
	}
}

func TestFailedInboxKeepsLastSuccessAndSurfaces(t *testing.T) {
	f := &fakeMCP{reads: map[string]string{"xiaohongshu_get_notifications": `{"complete":true,"loaded":0,"items":[]}`}, errs: map[string]error{}}
	e := opsEngine(t, f, nil)
	if _, err := e.SyncInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	last := e.Store.Snapshot().Inbox["xiaohongshu"].LastSuccess
	f.errs["xiaohongshu_get_notifications"] = fmt.Errorf("未登录")
	if _, err := e.SyncInbox(context.Background()); err == nil {
		t.Fatal("failure must be reported")
	}
	s := e.Store.Snapshot()
	if !s.Inbox["xiaohongshu"].LastSuccess.Equal(last) || s.Inbox["xiaohongshu"].Error == "" {
		t.Fatalf("%+v", s.Inbox)
	}
	if items := Attention(s); len(items) != 1 || items[0].Kind != "inbox" {
		t.Fatalf("%+v", items)
	}
}

func TestDueDraftOnlyEntersQueue(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "a.png")
	if err := os.WriteFile(image, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	f := &fakeMCP{}
	e := opsEngine(t, f, func(s *State) {
		s.Drafts = []Draft{
			{ID: "due", Kind: "note", Title: "t", Body: "b", Status: "ready", ScheduledAt: past, AssetPaths: []string{image}},
			{ID: "later", Kind: "note", Title: "t", Body: "b", Status: "ready", ScheduledAt: future, AssetPaths: []string{image}},
			{ID: "unready", Kind: "note", Title: "t", Body: "b", Status: "draft", ScheduledAt: past, AssetPaths: []string{image}},
			{ID: "broken", Kind: "note", Title: "t", Body: "b", Status: "ready", ScheduledAt: past},
		}
	})
	for i := 0; i < 2; i++ {
		e.SubmitDue(context.Background())
	}
	if len(f.proposals) != 1 || f.proposals[0].tool != "xiaohongshu_publish_note" {
		t.Fatalf("%+v", f.proposals)
	}
	if d := draftByID(t, e, "due"); d.Status != "ready" || d.Approval.Status != "pending" {
		t.Fatalf("due draft must wait for confirmation: %+v", d)
	}
	if d := draftByID(t, e, "broken"); d.ScheduleError == "" {
		t.Fatalf("a due draft that cannot run must be surfaced: %+v", d)
	}
}

func TestMetricsAndInsights(t *testing.T) {
	f := &fakeMCP{reads: map[string]string{"xiaohongshu_get_my_account": `{"stats":{"fans":"1.2万"},"notes":[{"id":"a","title":"低","likes":"1","collects":"0","comments":"0"},{"id":"b","title":"高","likes":"1.5万","collects":"10+","comments":"3"}]}`}}
	e := opsEngine(t, f, nil)
	if err := e.SyncMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := e.Store.Snapshot()
	if len(s.Samples) != 1 || s.Samples[0].Notes[1].Likes != 15000 || s.Samples[0].Notes[1].Collects != 10 {
		t.Fatalf("%+v", s.Samples)
	}
	if got := Insights(s); !strings.HasPrefix(got, "当前账号互动较好的内容：《高》") {
		t.Fatal(got)
	}
}

func TestScheduledSyncRespectsMinimumGap(t *testing.T) {
	f := &fakeMCP{reads: map[string]string{"xiaohongshu_check_login": `{"logged_in":true}`, "xiaohongshu_get_notifications": `{"complete":true}`, "xiaohongshu_get_my_account": `{}`}}
	e := opsEngine(t, f, func(s *State) { s.LastSyncAt = time.Now() })
	run := &Run{Trigger: "schedule"}
	e.syncPlatforms(context.Background(), run)
	if len(e.Store.Snapshot().Samples) != 0 {
		t.Fatal("a scheduled sync inside the gap must not read the platforms")
	}
	e.syncPlatforms(context.Background(), &Run{Trigger: "manual"})
	if len(e.Store.Snapshot().Samples) != 1 {
		t.Fatal("a manual sync always runs")
	}
}

func TestStyleCheckCatchesTemplateNotHumanWriting(t *testing.T) {
	aiish := "转出来发糊，常见是这三个原因：\n\n1️⃣ 格子太少\n细节会丢。\n\n2️⃣ 颜色太少\n层次会没。\n\n3️⃣ 主体太小\n先裁剪。\n\n总的来说，你的图卡在哪了？"
	got := strings.Join(StyleCheck("note", aiish), "；")
	for _, want := range []string{"编号列表", "总的来说", "结尾提问"} {
		if !strings.Contains(got, want) {
			t.Errorf("missed %s in %s", want, got)
		}
	}
	human := "昨晚拿我家猫的照片转了六遍，胡子全糊成一坨。\n\n换到 87 格才看清，但这么大我肯定拼不完。\n\n#拼豆图纸"
	if w := StyleCheck("note", human); len(w) != 0 {
		t.Fatalf("plain writing flagged: %v", w)
	}
	if w := StyleCheck("note", "配图由程序生成。内容由 AI 协助撰写。"); len(w) == 0 {
		t.Fatal("the removed disclaimers should be flagged")
	}
}

func TestScorecardUsesCheckpointsAndNeverEstimates(t *testing.T) {
	sent := time.Now().Add(-100 * time.Hour)
	sample := func(after time.Duration, likes int64) MetricSample {
		return MetricSample{At: sent.Add(after), Platform: "xiaohongshu", Notes: []NoteStat{{ID: "n1", Likes: likes}, {ID: "other", Likes: 999}}}
	}
	st := State{
		Drafts:  []Draft{{ID: "d", Kind: "note", Title: "t", Status: "sent", PlatformID: "n1", SentAt: sent, Hypothesis: "短文"}, {ID: "unsent", Kind: "note", Status: "draft"}},
		Samples: []MetricSample{sample(time.Hour, 1), sample(25*time.Hour, 5), sample(30*time.Hour, 6), sample(80*time.Hour, 9), {At: sent.Add(26 * time.Hour), Platform: "xiaohongshu", Watch: "对标", Notes: []NoteStat{{ID: "n1", Likes: 500}}}},
	}
	cards := Scorecards(st)
	if len(cards) != 1 {
		t.Fatalf("%+v", cards)
	}
	c := cards[0]
	if c.Points["24h"].Likes != 5 || c.Points["72h"].Likes != 9 || c.Latest.Likes != 9 {
		t.Fatalf("%+v", c.Points)
	}
	if _, ok := c.Points["7d"]; ok {
		t.Fatal("a checkpoint with no sample must stay empty")
	}
}

func TestParseCount(t *testing.T) {
	for in, want := range map[string]int64{"12": 12, "1.2万": 12000, "10+": 10, "": 0, "赞": 0, "1,024": 1024, "3k": 3000, "2亿": 200000000} {
		if got := parseCount(in); got != want {
			t.Errorf("%q: %d != %d", in, got, want)
		}
	}
}

func TestMCPClientAllowlists(t *testing.T) {
	m := &MCPReader{URL: "http://127.0.0.1:1/mcp"}
	if _, err := m.Call(context.Background(), "xiaohongshu_post_comment", nil); err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("reads must not reach write tools: %v", err)
	}
	if _, err := m.Propose(context.Background(), "browser_click", nil, "main", ""); err == nil || !strings.Contains(err.Error(), "不能提交") {
		t.Fatalf("proposals must stay on the allowlist: %v", err)
	}
	if err := m.Decide(context.Background(), "x", "delete", nil); err == nil {
		t.Fatal("unknown decision must be refused")
	}
}

func TestVersionOneStateUpgrades(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var state map[string]any
	if err := json.Unmarshal(seed, &state); err != nil {
		t.Fatal(err)
	}
	state["version"] = 1
	b, _ := json.Marshal(state)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot(); got.Version != 2 || len(got.Drafts) == 0 || got.Drafts[0].platform() != "xiaohongshu" {
		t.Fatalf("%+v", got.Version)
	}
}
