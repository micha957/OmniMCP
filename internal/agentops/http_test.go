package agentops

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPDraftEditingAndRequestBoundaries(t *testing.T) {
	s := testStore(t)
	app := (&App{Engine: NewEngine(context.Background(), s, nil, nil, "")}).Handler()
	request := func(method, path, body, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "127.0.0.1:8091"
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	before := s.Snapshot().Drafts[0]
	if w := request("PATCH", "/api/drafts/"+before.ID, `{"title":"开发中的照片转图纸","body":"当前版本的照片转图纸流程正在完善。","status":"ready"}`, ""); w.Code != 200 {
		t.Fatalf("save: %s", w.Body.String())
	}
	if s.Snapshot().Drafts[0].Status != "ready" {
		t.Fatal("draft edit was not saved")
	}
	if w := request("PATCH", "/api/drafts/"+before.ID, `{"title":"","body":"empty title","status":"ready"}`, ""); w.Code != 400 {
		t.Fatal("invalid draft accepted")
	}
	if s.Snapshot().Drafts[0].Title != "开发中的照片转图纸" {
		t.Fatal("invalid edit changed persisted content")
	}
	if w := request("POST", "/api/run", `{"mode":"prepare"}`, "https://another-site.example"); w.Code != 403 {
		t.Fatal("cross-origin write accepted")
	}
	if w := request("POST", "/api/run", `{"mode":"prepare"} garbage`, ""); w.Code != 400 {
		t.Fatal("malformed trailing data accepted")
	}
	config := s.Snapshot().Config
	config.Stage = "someday"
	b, _ := json.Marshal(config)
	if w := request("PUT", "/api/config", string(b), ""); w.Code != 400 {
		t.Fatal("unknown stage accepted")
	}
	if w := request("GET", "/api/export", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "开发中的照片转图纸") {
		t.Fatal("draft export failed")
	}
}

func TestHTTPManualSourcesAndNewMessages(t *testing.T) {
	s := testStore(t)
	app := (&App{Engine: NewEngine(context.Background(), s, nil, nil, "")}).Handler()
	post := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Host = "127.0.0.1:8091"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	body := `{"url":"https://www.xiaohongshu.com/explore/123456789012345678901234?xsec_token=first","title":"照片图纸","body":"如何制作拼豆图纸"}`
	if w := post("/api/opportunities", body); w.Code != 201 {
		t.Fatalf("add: %s", w.Body.String())
	}
	if w := post("/api/opportunities", strings.ReplaceAll(body, "first", "second")); w.Code != 409 {
		t.Fatal("token change bypassed source deduplication")
	}
	if w := post("/api/opportunities", strings.ReplaceAll(body, "www.xiaohongshu.com", "evil.example")); w.Code != 400 {
		t.Fatal("non-platform link accepted")
	}
	w := post("/api/threads", `{"url":"https://www.xiaohongshu.com/explore/123456789012345678901234","title":"导出问题","context":"此前询问如何裁图","author":"提问者","message":"导出在哪里"}`)
	if w.Code != 201 {
		t.Fatalf("thread: %s", w.Body.String())
	}
	var thread Thread
	_ = json.Unmarshal(w.Body.Bytes(), &thread)
	if w := post("/api/threads/"+thread.ID+"/messages", `{"author":"提问者","text":"可以分板吗"}`); w.Code != 200 {
		t.Fatal("new message not accepted")
	}
	if len(s.Snapshot().Threads[0].Messages) != 2 || s.Snapshot().Threads[0].Status != "pending" {
		t.Fatal("new message lost conversation state")
	}
}
