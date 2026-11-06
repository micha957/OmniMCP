package zhihu

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/heybox/omnimcp/plugin"
)

func TestNotificationsKeepOnlyComments(t *testing.T) {
	raw := `[
	 {"id":"n1","create_time":100,"content":{"verb":" 评论了你的回答"},"target":{"id":11,"content":"<p>男版&amp;徐</p>","reply_root_id":11,"author":{"member":{"id":"u1","name":"小A"}},"target":{"id":"28","type":"answer","question":{"id":"45","title":"问题"}}}},
	 {"id":"n2","content":{"verb":"赞同了你的回答"},"target":{"id":"28"}},
	 {"id":"n3","content":{"verb":"喜欢了你的评论"},"target":{"id":12}},
	 {"id":"g","items":[{"id":"n4","create_time":90,"content":{"verb":"回复了你的评论"},"target":{"id":13,"content":"嗯","reply_root_id":11,"author":{"member":{"id":"u2","name":"小B"}},"target":{"id":99,"type":"article","title":"文章"}}}]}
	]`
	var list []rawNotification
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	got := convertNotifications(list, 10)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	a, b := got[0], got[1]
	if a.CommentID != "11" || a.CommentText != "男版&徐" || a.FeedID != "28" || a.Title != "问题" || a.URL != "https://www.zhihu.com/question/45/answer/28" || a.From.Nickname != "小A" {
		t.Fatalf("%+v", a)
	}
	if b.CommentID != "13" || b.RootID != "11" || b.URL != "https://zhuanlan.zhihu.com/p/99" || b.Title != "文章" {
		t.Fatalf("%+v", b)
	}
}

type noCookies struct{}

func (noCookies) OpenLogin(context.Context, string) error { return nil }
func (noCookies) CookieHeader(context.Context, string) (string, error) {
	return "", nil
}

func TestGuestIsAnAnswerNotAFailure(t *testing.T) {
	out, err := New().Execute(context.Background(), &plugin.Context{Auth: noCookies{}}, "check_login", json.RawMessage(`{}`))
	if err != nil || out.(map[string]any)["logged_in"] != false {
		t.Fatalf("%v %v", out, err)
	}
	if _, err := New().Execute(context.Background(), &plugin.Context{Auth: noCookies{}}, "get_my_account", json.RawMessage(`{}`)); !errors.Is(err, plugin.ErrNotLoggedIn) {
		t.Fatalf("%v", err)
	}
}
