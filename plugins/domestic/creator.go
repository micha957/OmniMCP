package domestic

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/heybox/omnimcp/plugin"
)

const (
	douyinDomain  = "douyin.com"
	douyinReferer = "https://creator.douyin.com/"
)

type douyinUser struct {
	Info struct {
		UniqueID  string `json:"douyin_unique_id"`
		Nickname  string `json:"nick_name"`
		Followers int64  `json:"follower_count"`
		Following int64  `json:"following_count"`
		Favorited int64  `json:"total_favorited"`
	} `json:"douyin_user_verify_info"`
}

func douyinLogin(ctx context.Context, c *plugin.Context) (*douyinUser, error) {
	var u douyinUser
	err := plugin.FetchJSON(ctx, c, douyinDomain, "https://creator.douyin.com/aweme/v1/creator/user/info/", douyinReferer, &u)
	if errors.Is(err, plugin.ErrNotLoggedIn) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if u.Info.UniqueID == "" && u.Info.Nickname == "" {
		return nil, nil
	}
	return &u, nil
}

func douyinCheckLogin(ctx context.Context, c *plugin.Context) (any, error) {
	u, err := douyinLogin(ctx, c)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return map[string]any{"logged_in": false}, nil
	}
	return map[string]any{"logged_in": true, "user": map[string]string{"user_id": u.Info.UniqueID, "nickname": u.Info.Nickname}}, nil
}

// douyinAccount reads the creator centre: follower counts and per-work statistics.
func douyinAccount(ctx context.Context, c *plugin.Context) (any, error) {
	u, err := douyinLogin(ctx, c)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, plugin.ErrNotLoggedIn
	}
	var works struct {
		List []struct {
			ID         string `json:"aweme_id"`
			Desc       string `json:"desc"`
			Statistics struct {
				Play    int64 `json:"play_count"`
				Digg    int64 `json:"digg_count"`
				Collect int64 `json:"collect_count"`
				Comment int64 `json:"comment_count"`
				Share   int64 `json:"share_count"`
			} `json:"statistics"`
		} `json:"aweme_list"`
	}
	if err := plugin.FetchJSON(ctx, c, douyinDomain, "https://creator.douyin.com/janus/douyin/creator/pc/work_list?status=0&count=20&max_cursor=0&scene=star_atlas&device_platform=android&aid=1128", douyinReferer, &works); err != nil {
		return nil, err
	}
	num := func(v int64) string { return strconv.FormatInt(v, 10) }
	notes := []map[string]string{}
	for _, w := range works.List {
		s := w.Statistics
		notes = append(notes, map[string]string{"id": w.ID, "title": w.Desc, "views": num(s.Play), "likes": num(s.Digg), "collects": num(s.Collect), "comments": num(s.Comment), "shares": num(s.Share)})
	}
	return map[string]any{"user": map[string]string{"user_id": u.Info.UniqueID, "nickname": u.Info.Nickname},
		"stats": map[string]string{"fans": num(u.Info.Followers), "follows": num(u.Info.Following), "interaction": num(u.Info.Favorited)},
		"notes": notes, "sampled_at": time.Now().UTC()}, nil
}
