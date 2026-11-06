package zhihu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/heybox/omnimcp/plugin"
)

const (
	zhihuDomain  = "zhihu.com"
	zhihuReferer = "https://www.zhihu.com/"
	// recent is the only notification feed that carries comments.
	notificationsURL = "https://www.zhihu.com/api/v4/notifications/v2/recent"
)

var htmlTag = regexp.MustCompile(`<[^>]+>`)

func plainText(s string) string {
	return strings.TrimSpace(html.UnescapeString(htmlTag.ReplaceAllString(s, "")))
}

type me struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URLToken string `json:"url_token"`
}

func checkLogin(ctx context.Context, c *plugin.Context) (*me, error) {
	var u me
	err := plugin.FetchJSON(ctx, c, zhihuDomain, "https://www.zhihu.com/api/v4/me", zhihuReferer, &u)
	if errors.Is(err, plugin.ErrNotLoggedIn) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if u.ID == "" {
		return nil, nil
	}
	return &u, nil
}

type notificationUser struct {
	UserID   string `json:"user_id"`
	Nickname string `json:"nickname"`
}

// Notification mirrors the shape other platforms return.
type Notification struct {
	ID          string           `json:"id"`
	Type        string           `json:"type"`
	Title       string           `json:"title"`
	Time        int64            `json:"time"`
	From        notificationUser `json:"from"`
	CommentID   string           `json:"comment_id"`
	RootID      string           `json:"root_id,omitempty"`
	CommentText string           `json:"comment_text"`
	FeedID      string           `json:"feed_id,omitempty"`
	FeedTitle   string           `json:"feed_title,omitempty"`
	URL         string           `json:"url,omitempty"`
}

type rawNotification struct {
	ID         string `json:"id"`
	CreateTime int64  `json:"create_time"`
	Content    struct {
		Verb string `json:"verb"`
	} `json:"content"`
	Target json.RawMessage   `json:"target"`
	Items  []rawNotification `json:"items"`
}

type rawComment struct {
	ID          json.Number `json:"id"`
	Content     string      `json:"content"`
	ReplyRootID json.Number `json:"reply_root_id"`
	Author      struct {
		Member struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"member"`
	} `json:"author"`
	Target struct {
		ID       json.RawMessage `json:"id"`
		Type     string          `json:"type"`
		Title    string          `json:"title"`
		Question struct {
			ID    json.RawMessage `json:"id"`
			Title string          `json:"title"`
		} `json:"question"`
	} `json:"target"`
}

// jsonID accepts ids the API sends as either a string or a number.
func jsonID(raw json.RawMessage) string {
	return strings.Trim(string(raw), `"`)
}

func commentContentURL(c rawComment) string {
	id := jsonID(c.Target.ID)
	switch c.Target.Type {
	case "answer":
		return "https://www.zhihu.com/question/" + jsonID(c.Target.Question.ID) + "/answer/" + id
	case "article":
		return "https://zhuanlan.zhihu.com/p/" + id
	}
	return ""
}

func convertNotifications(raw []rawNotification, limit int) []Notification {
	out := []Notification{}
	var walk func([]rawNotification)
	walk = func(list []rawNotification) {
		for _, n := range list {
			if len(n.Items) > 0 {
				walk(n.Items)
				continue
			}
			verb := strings.TrimSpace(n.Content.Verb)
			if len(out) >= limit || !(strings.Contains(verb, "评论") || strings.Contains(verb, "回复") || strings.Contains(verb, "@")) || strings.Contains(verb, "喜欢") || strings.Contains(verb, "赞同") {
				continue
			}
			var c rawComment
			if json.Unmarshal(n.Target, &c) != nil || c.ID == "" {
				continue
			}
			title := c.Target.Question.Title
			if title == "" {
				title = c.Target.Title
			}
			out = append(out, Notification{ID: n.ID, Type: verb, Title: title, Time: n.CreateTime,
				From:      notificationUser{UserID: c.Author.Member.ID, Nickname: c.Author.Member.Name},
				CommentID: c.ID.String(), RootID: c.ReplyRootID.String(), CommentText: plainText(c.Content),
				FeedID: jsonID(c.Target.ID), FeedTitle: title, URL: commentContentURL(c)})
		}
	}
	walk(raw)
	return out
}

// notifications pages through the feed until limit comments are found. The feed
// mixes in follows and likes, so a page can legitimately hold no comments.
func notifications(ctx context.Context, c *plugin.Context, limit int) (any, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var all []rawNotification
	next := notificationsURL + "?limit=20"
	complete := false
	// Few pages on purpose: Zhihu limits accounts that read too much too quickly.
	for page := 0; page < 2; page++ {
		var res struct {
			Data   []rawNotification `json:"data"`
			Paging struct {
				IsEnd bool   `json:"is_end"`
				Next  string `json:"next"`
			} `json:"paging"`
		}
		if err := plugin.FetchJSON(ctx, c, zhihuDomain, next, zhihuReferer, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Data...)
		if res.Paging.IsEnd {
			complete = true
			break
		}
		if len(convertNotifications(all, limit)) >= limit || res.Paging.Next == "" {
			break
		}
		// The API hands back an http:// link that only redirects.
		next = strings.Replace(res.Paging.Next, "http://", "https://", 1)
	}
	items := convertNotifications(all, limit)
	return map[string]any{"tab": "recent", "items": items, "loaded": len(all), "complete": complete}, nil
}

type contentStat struct {
	ID           json.RawMessage `json:"id"`
	Title        string          `json:"title"`
	VoteupCount  int64           `json:"voteup_count"`
	CommentCount int64           `json:"comment_count"`
	Question     struct {
		Title string `json:"title"`
	} `json:"question"`
	// Answers carry no vote count here, only reaction statistics.
	Reaction struct {
		Statistics struct {
			LikeCount int64 `json:"like_count"`
			Favorites int64 `json:"favorites"`
		} `json:"statistics"`
	} `json:"reaction"`
}

func myAccount(ctx context.Context, c *plugin.Context) (any, error) {
	u, err := checkLogin(ctx, c)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, plugin.ErrNotLoggedIn
	}
	base := "https://www.zhihu.com/api/v4/members/" + url.PathEscape(u.URLToken)
	var profile struct {
		FollowerCount  int64 `json:"follower_count"`
		FollowingCount int64 `json:"following_count"`
		VoteupCount    int64 `json:"voteup_count"`
		AnswerCount    int64 `json:"answer_count"`
		ArticlesCount  int64 `json:"articles_count"`
	}
	include := url.QueryEscape("follower_count,following_count,voteup_count,answer_count,articles_count")
	if err := plugin.FetchJSON(ctx, c, zhihuDomain, base+"?include="+include, zhihuReferer, &profile); err != nil {
		return nil, fmt.Errorf("读取账号数据失败: %w", err)
	}
	num := func(v int64) string { return strconv.FormatInt(v, 10) }
	notes := []map[string]string{}
	listInclude := url.QueryEscape("data[*].voteup_count,comment_count")
	for _, kind := range []string{"answers", "articles"} {
		var list struct {
			Data []contentStat `json:"data"`
		}
		if err := plugin.FetchJSON(ctx, c, zhihuDomain, base+"/"+kind+"?limit=20&sort_by=created&include="+listInclude, zhihuReferer, &list); err != nil {
			return nil, fmt.Errorf("读取%s失败: %w", kind, err)
		}
		for _, item := range list.Data {
			title := item.Title
			if title == "" {
				title = item.Question.Title
			}
			likes := item.VoteupCount
			if likes == 0 {
				likes = item.Reaction.Statistics.LikeCount
			}
			notes = append(notes, map[string]string{"id": jsonID(item.ID), "title": title, "likes": num(likes), "collects": num(item.Reaction.Statistics.Favorites), "comments": num(item.CommentCount)})
		}
	}
	return map[string]any{
		"user":       map[string]string{"user_id": u.ID, "nickname": u.Name, "url_token": u.URLToken},
		"stats":      map[string]string{"fans": num(profile.FollowerCount), "follows": num(profile.FollowingCount), "interaction": num(profile.VoteupCount), "answers": num(profile.AnswerCount), "articles": num(profile.ArticlesCount)},
		"notes":      notes,
		"sampled_at": time.Now().UTC(),
	}, nil
}
