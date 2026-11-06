package bilibili

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/heybox/omnimcp/plugin"
)

// Message centre endpoints, keyed by the public tab names.
var notificationPaths = map[string]string{
	"replies":  "https://api.bilibili.com/x/msgfeed/reply?platform=web&build=0&mobi_app=web",
	"mentions": "https://api.bilibili.com/x/msgfeed/at?platform=web&build=0&mobi_app=web",
}

// Notification mirrors the shape other platforms return, so the operations
// layer can treat every inbox the same way.
type Notification struct {
	ID          string           `json:"id"`
	Type        string           `json:"type"`
	Title       string           `json:"title"`
	Time        int64            `json:"time"`
	From        NotificationUser `json:"from"`
	CommentID   string           `json:"comment_id,omitempty"`
	RootID      string           `json:"root_id,omitempty"`
	CommentText string           `json:"comment_text,omitempty"`
	FeedID      string           `json:"feed_id,omitempty"`
	FeedTitle   string           `json:"feed_title,omitempty"`
	URL         string           `json:"url,omitempty"`
}

type NotificationUser struct {
	UserID   string `json:"user_id"`
	Nickname string `json:"nickname"`
}

type notificationPage struct {
	Cursor struct {
		IsEnd bool `json:"is_end"`
	} `json:"cursor"`
	Items []struct {
		ID   int64 `json:"id"`
		User struct {
			Mid      int64  `json:"mid"`
			Nickname string `json:"nickname"`
		} `json:"user"`
		Item struct {
			SubjectID     int64  `json:"subject_id"`
			RootID        int64  `json:"root_id"`
			SourceID      int64  `json:"source_id"`
			Type          string `json:"type"`
			BusinessID    int    `json:"business_id"`
			Title         string `json:"title"`
			SourceContent string `json:"source_content"`
			URI           string `json:"uri"`
		} `json:"item"`
		ReplyTime int64 `json:"reply_time"`
		AtTime    int64 `json:"at_time"`
	} `json:"items"`
}

func convertNotifications(raw json.RawMessage, tab string, limit int) (map[string]any, error) {
	var page notificationPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("解析 B 站通知失败: %w", err)
	}
	items := []Notification{}
	for _, it := range page.Items {
		if len(items) >= limit {
			break
		}
		at := it.ReplyTime
		if at == 0 {
			at = it.AtTime
		}
		n := Notification{ID: strconv.FormatInt(it.ID, 10), Type: it.Item.Type, Title: it.Item.Title, Time: at,
			From:        NotificationUser{UserID: strconv.FormatInt(it.User.Mid, 10), Nickname: it.User.Nickname},
			CommentText: it.Item.SourceContent, FeedTitle: it.Item.Title, URL: it.Item.URI}
		if it.Item.SubjectID != 0 {
			n.FeedID = strconv.FormatInt(it.Item.SubjectID, 10)
		}
		if it.Item.SourceID != 0 {
			n.CommentID = strconv.FormatInt(it.Item.SourceID, 10)
			// A top-level comment has root 0 and is its own thread root when replied to.
			n.RootID = n.CommentID
			if it.Item.RootID != 0 {
				n.RootID = strconv.FormatInt(it.Item.RootID, 10)
			}
		}
		items = append(items, n)
	}
	return map[string]any{"tab": tab, "items": items, "loaded": len(page.Items), "complete": page.Cursor.IsEnd && len(items) == len(page.Items)}, nil
}

func dataOf(v any) (json.RawMessage, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("B 站接口返回格式异常")
	}
	return json.Marshal(m["data"])
}

func (p *Plugin) checkLogin(ctx context.Context, c *plugin.Context) (any, error) {
	out, err := p.get(ctx, c, "https://api.bilibili.com/x/web-interface/nav")
	if err != nil {
		// nav answers code -101 for a guest, which is an answer rather than a failure.
		if strings.Contains(err.Error(), "code:-101") {
			return map[string]any{"logged_in": false}, nil
		}
		return nil, err
	}
	raw, err := dataOf(out)
	if err != nil {
		return nil, err
	}
	var nav struct {
		IsLogin bool   `json:"isLogin"`
		Mid     int64  `json:"mid"`
		Uname   string `json:"uname"`
	}
	if err := json.Unmarshal(raw, &nav); err != nil {
		return nil, err
	}
	if !nav.IsLogin {
		return map[string]any{"logged_in": false}, nil
	}
	return map[string]any{"logged_in": true, "user": map[string]string{"user_id": strconv.FormatInt(nav.Mid, 10), "nickname": nav.Uname}}, nil
}

func (p *Plugin) notifications(ctx context.Context, c *plugin.Context, tab string, limit int) (any, error) {
	if tab == "" {
		tab = "replies"
	}
	path, ok := notificationPaths[tab]
	if !ok {
		return nil, fmt.Errorf("未知的通知分区 %q，可选：replies / mentions", tab)
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	out, err := p.get(ctx, c, path)
	if err != nil {
		return nil, err
	}
	raw, err := dataOf(out)
	if err != nil {
		return nil, err
	}
	return convertNotifications(raw, tab, limit)
}

// myAccount combines the follower count with the creator centre's archive list,
// which carries per-video view, like, coin, favorite, reply and share counts.
func (p *Plugin) myAccount(ctx context.Context, c *plugin.Context) (any, error) {
	login, err := p.checkLogin(ctx, c)
	if err != nil {
		return nil, err
	}
	m := login.(map[string]any)
	if m["logged_in"] != true {
		return nil, fmt.Errorf("B 站未登录")
	}
	user := m["user"].(map[string]string)
	out := map[string]any{"user": user, "sampled_at": time.Now().UTC()}
	relation, err := p.get(ctx, c, "https://api.bilibili.com/x/relation/stat?vmid="+user["user_id"])
	if err != nil {
		return nil, err
	}
	if raw, err := dataOf(relation); err == nil {
		var stat struct {
			Follower  int64 `json:"follower"`
			Following int64 `json:"following"`
		}
		if json.Unmarshal(raw, &stat) == nil {
			out["stats"] = map[string]string{"fans": strconv.FormatInt(stat.Follower, 10), "follows": strconv.FormatInt(stat.Following, 10)}
		}
	}
	archives, err := p.get(ctx, c, "https://member.bilibili.com/x/web/archives?status=is_pubing,pubed,not_pubed&pn=1&ps=20")
	if err != nil {
		return nil, fmt.Errorf("读取稿件列表失败: %w", err)
	}
	raw, err := dataOf(archives)
	if err != nil {
		return nil, err
	}
	var list struct {
		ArcAudits []struct {
			Archive struct {
				Aid   int64  `json:"aid"`
				BVID  string `json:"bvid"`
				Title string `json:"title"`
			} `json:"Archive"`
			Stat struct {
				View, Like, Favorite, Reply, Share, Coin int64
			} `json:"stat"`
		} `json:"arc_audits"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("解析稿件列表失败: %w", err)
	}
	notes := []map[string]string{}
	for _, a := range list.ArcAudits {
		num := func(v int64) string { return strconv.FormatInt(v, 10) }
		notes = append(notes, map[string]string{"id": num(a.Archive.Aid), "bvid": a.Archive.BVID, "title": a.Archive.Title, "views": num(a.Stat.View), "likes": num(a.Stat.Like), "collects": num(a.Stat.Favorite), "comments": num(a.Stat.Reply), "shares": num(a.Stat.Share), "coins": num(a.Stat.Coin)})
	}
	out["notes"] = notes
	return out, nil
}

// replyReceipt turns the API's answer into the receipt shape used everywhere.
func replyReceipt(v any, target string) (any, error) {
	raw, err := dataOf(v)
	if err != nil {
		return nil, err
	}
	var d struct {
		RPID int64 `json:"rpid"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || d.RPID == 0 {
		return nil, fmt.Errorf("B 站接受了请求，但没有返回评论 ID: %w", plugin.ErrOutcomeUnknown)
	}
	out := map[string]any{"success": true, "verified": true, "comment_id": strconv.FormatInt(d.RPID, 10)}
	if target != "" {
		out["reply_to"] = target
	}
	return out, nil
}
