package zhihu

import (
	"context"
	"encoding/json"
	"github.com/heybox/omnimcp/plugin"
	"testing"
)

type reader struct {
	opened, mode string
	limit        int
}

func (r *reader) Open(_ context.Context, url string) error { r.opened = url; return nil }
func (*reader) Snapshot(context.Context) (string, error)   { return "", nil }
func (*reader) Click(context.Context, string) error        { return nil }
func (*reader) Fill(context.Context, string, string) error { return nil }
func (r *reader) ReadZhihu(_ context.Context, mode string, limit int) (json.RawMessage, error) {
	r.mode = mode
	r.limit = limit
	return json.RawMessage(`{"status":"ok","body":"content"}`), nil
}
func TestCollectionTargets(t *testing.T) {
	for _, raw := range []string{"http://zhuanlan.zhihu.com/p/123", "https://evil.test/p/123", "https://zhuanlan.zhihu.com.evil.test/p/123", "https://u@zhuanlan.zhihu.com/p/123", "https://zhuanlan.zhihu.com/p/no", "https://zhuanlan.zhihu.com:99/p/123"} {
		if _, _, err := collectionTarget("article_detail", raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	r := &reader{}
	_, err := New().Execute(context.Background(), &plugin.Context{Browser: r}, "author_posts", json.RawMessage(`{"url":"https://www.zhihu.com/people/dai-zong-66/posts?tracking=x","limit":6}`))
	if err != nil || r.opened != "https://www.zhihu.com/people/dai-zong-66/posts" || r.mode != "author" || r.limit != 6 {
		t.Fatalf("wrong request: %+v %v", r, err)
	}
}
func TestCollectionLimit(t *testing.T) {
	r := &reader{}
	_, err := New().Execute(context.Background(), &plugin.Context{Browser: r}, "author_posts", json.RawMessage(`{"url":"https://www.zhihu.com/people/dai-zong-66/posts","limit":101}`))
	if err == nil || r.opened != "" {
		t.Fatal("invalid request navigated")
	}
}
