package agentops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type generateFunc func(context.Context, Config, Task) (Decision, error)

func (f generateFunc) Generate(ctx context.Context, c Config, task Task) (Decision, error) {
	return f(ctx, c, task)
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func runEngine(t *testing.T, e *Engine, mode string) {
	t.Helper()
	if _, err := e.Start("test", mode); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}
func goodDecision(task Task) Decision {
	return Decision{Action: "draft", Reason: "问题与已确认的功能相符", Title: task.Title, Body: "当前开发版本可将照片转换为拼豆图纸。", FactIDs: []string{"photo"}}
}

func TestPreparePrioritizesRepliesAndSurvivesRestart(t *testing.T) {
	s := testStore(t)
	if err := s.Update(func(s *State) error {
		s.Opportunities = append(s.Opportunities, Opportunity{ID: "post", Title: "照片制图", Body: "怎么把照片做成图纸", Status: "pending"})
		s.Threads = append(s.Threads, Thread{ID: "thread", Title: "裁剪问题", Status: "pending", Messages: []Message{{ID: "m1", Author: "提问者", Text: "如何调整主体"}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	g := generateFunc(func(_ context.Context, _ Config, task Task) (Decision, error) {
		kinds = append(kinds, task.Kind)
		return goodDecision(task), nil
	})
	e := NewEngine(context.Background(), s, g, nil, "test-model")
	runEngine(t, e, "prepare")
	if strings.Join(kinds, ",") != "reply,comment" {
		t.Fatalf("task order: %v", kinds)
	}
	if s.Snapshot().Runs[0].Drafts != 2 {
		t.Fatal("missing prepared drafts")
	}
	reopened, err := NewStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	e = NewEngine(context.Background(), reopened, g, nil, "test-model")
	runEngine(t, e, "prepare")
	if len(kinds) != 2 || len(reopened.Snapshot().Drafts) != 5 {
		t.Fatal("restart repeated completed work")
	}
	if err := reopened.Update(func(s *State) error {
		s.Threads[0].Messages = append(s.Threads[0].Messages, Message{ID: "m2", Author: "提问者", Text: "还能减少颜色吗"})
		s.Threads[0].Status = "pending"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runEngine(t, e, "prepare")
	if len(kinds) != 3 || kinds[2] != "reply" {
		t.Fatal("new reply was not prepared")
	}
}

func TestNewMessageInvalidatesInFlightReply(t *testing.T) {
	s := testStore(t)
	_ = s.Update(func(s *State) error {
		s.Threads = append(s.Threads, Thread{ID: "thread", Title: "照片问题", Status: "pending", Messages: []Message{{ID: "m1", Author: "用户", Text: "如何裁剪"}}})
		return nil
	})
	started, release := make(chan struct{}), make(chan struct{})
	g := generateFunc(func(_ context.Context, _ Config, task Task) (Decision, error) {
		close(started)
		<-release
		return goodDecision(task), nil
	})
	e := NewEngine(context.Background(), s, g, nil, "test")
	if _, err := e.Start("test", "prepare"); err != nil {
		t.Fatal(err)
	}
	<-started
	_ = s.Update(func(s *State) error {
		s.Threads[0].Messages = append(s.Threads[0].Messages, Message{ID: "m2", Author: "用户", Text: "改问导出怎么做"})
		return nil
	})
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	out := s.Snapshot()
	if len(out.Drafts) != 3 || out.Threads[0].Status != "pending" || out.Runs[0].Status != "failed" {
		t.Fatal("stale generation overwrote new context")
	}
}

func TestPauseCancelsWithoutSavingGeneratedContent(t *testing.T) {
	s := testStore(t)
	_ = s.Update(func(s *State) error {
		s.Config.Topics = append(s.Config.Topics, Topic{ID: "new", Title: "新选题"})
		return nil
	})
	started := make(chan struct{})
	g := generateFunc(func(ctx context.Context, _ Config, _ Task) (Decision, error) {
		close(started)
		<-ctx.Done()
		return Decision{}, ctx.Err()
	})
	e := NewEngine(context.Background(), s, g, nil, "test")
	_, err := e.Start("test", "prepare")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	e.Cancel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := e.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot().Drafts) != 3 || s.Snapshot().Runs[0].Status != "interrupted" {
		t.Fatal("cancel did not preserve state")
	}
}

func TestFailedPersistenceDoesNotChangeSnapshot(t *testing.T) {
	s := testStore(t)
	name := s.Snapshot().Config.Name
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(file, "state.json")
	if err := s.Update(func(s *State) error { s.Config.Name = "should not commit"; return nil }); err == nil {
		t.Fatal("expected persistence failure")
	}
	if s.Snapshot().Config.Name != name {
		t.Fatal("memory changed despite failed save")
	}
}

func TestRestartMarksInterruptedRun(t *testing.T) {
	s := testStore(t)
	_ = s.Update(func(s *State) error { s.Runs = append(s.Runs, Run{ID: "unfinished", Status: "running"}); return nil })
	reopened, err := NewStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().Runs[0].Status != "interrupted" {
		t.Fatal("unfinished run was reported as active")
	}
}

func TestChatTransportRejectsTruncationAndUnknownFacts(t *testing.T) {
	finish := "stop"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("unexpected request routing")
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "configured-model" || len(request.Messages) != 2 {
			t.Error("invalid chat request")
		}
		d, _ := json.Marshal(Decision{Action: "draft", Reason: "符合需求", Title: "照片图纸", Body: "照片可以转图纸。", FactIDs: []string{"unknown"}})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]string{"content": string(d)}}}})
	}))
	defer server.Close()
	g, err := NewChatGenerator(server.URL+"/v1", "configured-model", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	d, err := g.Generate(context.Background(), testStore(t).Snapshot().Config, Task{Kind: "note"})
	if err != nil {
		t.Fatal(err)
	}
	if validateDecision(testStore(t).Snapshot().Config, Task{Kind: "note"}, d) == nil {
		t.Fatal("accepted unknown product fact")
	}
	finish = "length"
	if _, err := g.Generate(context.Background(), Config{}, Task{}); err == nil {
		t.Fatal("accepted truncated result")
	}
}

type readFunc func(context.Context, string, map[string]any) (json.RawMessage, error)

func (f readFunc) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	return f(ctx, name, args)
}

func TestDiscoveryReadsFullContextAndDeduplicates(t *testing.T) {
	s := testStore(t)
	_ = s.Update(func(s *State) error { s.Config.AccountID = "main"; return nil })
	var mu sync.Mutex
	var calls []string
	r := readFunc(func(_ context.Context, name string, args map[string]any) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, name)
		if args["_account_id"] != "main" {
			return nil, fmt.Errorf("wrong account")
		}
		switch name {
		case "xiaohongshu_check_login":
			return json.RawMessage(`{"logged_in":true}`), nil
		case "xiaohongshu_search_notes":
			return json.RawMessage(`{"items":[{"id":"123456789012345678901234","xsecToken":"test-token"}]}`), nil
		case "xiaohongshu_get_note_detail":
			return json.RawMessage(`{"note":{"title":"照片怎么制图","desc":"需要拼豆图纸"},"comments":{"list":[{"content":"还想知道色号"}]}}`), nil
		default:
			return nil, fmt.Errorf("unexpected tool")
		}
	})
	e := NewEngine(context.Background(), s, nil, r, "")
	runEngine(t, e, "discover")
	runEngine(t, e, "discover")
	out := s.Snapshot()
	if len(out.Opportunities) != 1 || !strings.Contains(out.Opportunities[0].Body, "还想知道色号") {
		t.Fatal("discovery lost full context or duplicated the post")
	}
	for _, name := range calls {
		if name != "xiaohongshu_check_login" && name != "xiaohongshu_search_notes" && name != "xiaohongshu_get_note_detail" {
			t.Fatal("unexpected write operation")
		}
	}
}
