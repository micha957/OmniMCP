package bilibili

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/heybox/omnimcp/plugin"
)

// fakeAPI answers by URL path and records POST forms.
type fakeAPI struct {
	routes map[string]string
	form   url.Values
}

func (f *fakeAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "POST" {
		b, _ := io.ReadAll(r.Body)
		f.form, _ = url.ParseQuery(string(b))
	}
	body, ok := f.routes[r.URL.Path]
	if !ok {
		return nil, errors.New("unexpected request " + r.URL.String())
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func run(t *testing.T, api *fakeAPI, tool, args string) (any, error) {
	t.Helper()
	t.Setenv("BILIBILI_COOKIE", "SESSDATA=a; bili_jct=csrf1")
	p := New()
	p.client = &http.Client{Transport: api}
	return p.Execute(context.Background(), &plugin.Context{}, tool, json.RawMessage(args))
}

func TestCheckLoginTreatsGuestAsAnswer(t *testing.T) {
	out, err := run(t, &fakeAPI{routes: map[string]string{"/x/web-interface/nav": `{"code":-101,"data":{"isLogin":false}}`}}, "check_login", `{}`)
	if err != nil || out.(map[string]any)["logged_in"] != false {
		t.Fatalf("%v %v", out, err)
	}
}

func TestNotificationsAreNormalised(t *testing.T) {
	api := &fakeAPI{routes: map[string]string{"/x/msgfeed/reply": `{"code":0,"data":{"cursor":{"is_end":false},"items":[
		{"id":7,"user":{"mid":11,"nickname":"小A"},"item":{"subject_id":100,"root_id":0,"source_id":55,"type":"video","title":"视频","source_content":"怎么做的"},"reply_time":9},
		{"id":8,"user":{"mid":12,"nickname":"小B"},"item":{"subject_id":100,"root_id":55,"source_id":66,"type":"video","title":"视频","source_content":"再问"},"reply_time":10}]}}`}}
	out, err := run(t, api, "get_notifications", `{"limit":5}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	items := m["items"].([]Notification)
	if len(items) != 2 || items[0].RootID != "55" || items[1].RootID != "55" || items[1].CommentID != "66" || items[0].FeedID != "100" || items[0].From.Nickname != "小A" {
		t.Fatalf("items: %+v", items)
	}
	if m["complete"] != false {
		t.Fatalf("a page with more behind it must be incomplete: %v", m)
	}
}

func TestReplyCommentTargetsThreadAndReturnsReceipt(t *testing.T) {
	api := &fakeAPI{routes: map[string]string{"/x/v2/reply/add": `{"code":0,"data":{"rpid":777}}`}}
	out, err := run(t, api, "reply_comment", `{"aid":"100","root":"55","parent":"66","content":"可以"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["comment_id"] != "777" || m["reply_to"] != "66" {
		t.Fatalf("receipt: %v", m)
	}
	if api.form.Get("root") != "55" || api.form.Get("parent") != "66" || api.form.Get("oid") != "100" || api.form.Get("csrf") != "csrf1" {
		t.Fatalf("form: %v", api.form)
	}
}

func TestReplyWithoutIDIsUnknown(t *testing.T) {
	api := &fakeAPI{routes: map[string]string{"/x/v2/reply/add": `{"code":0,"data":{}}`}}
	_, err := run(t, api, "post_comment", `{"aid":"100","content":"你好"}`)
	if !errors.Is(err, plugin.ErrOutcomeUnknown) {
		t.Fatalf("want unknown, got %v", err)
	}
}

func TestMyAccountReadsArchiveStats(t *testing.T) {
	api := &fakeAPI{routes: map[string]string{
		"/x/web-interface/nav": `{"code":0,"data":{"isLogin":true,"mid":5,"uname":"我"}}`,
		"/x/relation/stat":     `{"code":0,"data":{"follower":30,"following":2}}`,
		"/x/web/archives":      `{"code":0,"data":{"arc_audits":[{"Archive":{"aid":100,"bvid":"BV1","title":"T"},"stat":{"view":10,"like":2,"favorite":1,"reply":3,"share":4,"coin":5}}]}}`,
	}}
	out, err := run(t, api, "get_my_account", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	note := m["notes"].([]map[string]string)[0]
	if note["views"] != "10" || note["comments"] != "3" || m["stats"].(map[string]string)["fans"] != "30" {
		t.Fatalf("%v", m)
	}
}

func TestAPIFailureNamesCodeForLoginDetection(t *testing.T) {
	_, err := run(t, &fakeAPI{routes: map[string]string{"/x/msgfeed/at": `{"code":-101,"message":"账号未登录"}`}}, "get_notifications", `{"tab":"mentions"}`)
	if err == nil || !strings.Contains(err.Error(), "-101") {
		t.Fatalf("%v", err)
	}
}
