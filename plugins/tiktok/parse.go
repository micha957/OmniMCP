package tiktok

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Count accepts TikTok counters encoded as either numbers or strings.
type Count int64

func (c *Count) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*c = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("count %s: %w", b, err)
	}
	*c = Count(n)
	return nil
}

type Author struct {
	ID        string `json:"id"`
	UniqueID  string `json:"uniqueId"`
	Nickname  string `json:"nickname"`
	Signature string `json:"signature,omitempty"`
	Verified  bool   `json:"verified,omitempty"`
	Avatar    string `json:"avatarLarger,omitempty"`
}

type Stats struct {
	Plays    Count `json:"playCount"`
	Likes    Count `json:"diggCount"`
	Comments Count `json:"commentCount"`
	Shares   Count `json:"shareCount"`
	Saves    Count `json:"collectCount"`
}

type Item struct {
	ID         string `json:"id"`
	Desc       string `json:"desc"`
	CreateTime Count  `json:"createTime"`
	Author     Author `json:"author"`
	Stats      Stats  `json:"stats"`
	Video      struct {
		Duration Count  `json:"duration"`
		Cover    string `json:"cover"`
	} `json:"video"`
	Music struct {
		Title      string `json:"title"`
		AuthorName string `json:"authorName"`
	} `json:"music"`
	Challenges []struct {
		Title string `json:"title"`
	} `json:"challenges"`
}

// Video is the tool-facing shape of a TikTok post.
type Video struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Desc      string `json:"desc"`
	CreatedAt int64  `json:"create_time,omitempty"`
	Author    string `json:"author"`
	Nickname  string `json:"nickname,omitempty"`
	Duration  int64  `json:"duration_seconds,omitempty"`
	Cover     string `json:"cover,omitempty"`
	Plays     int64  `json:"plays,omitempty"`
	// ViewsText is the display count on list cards ("1.2M"), where exact
	// counters are not available; open the video for exact stats.
	ViewsText string   `json:"views_text,omitempty"`
	Likes     int64    `json:"likes,omitempty"`
	Comments  int64    `json:"comments,omitempty"`
	Shares    int64    `json:"shares,omitempty"`
	Saves     int64    `json:"saves,omitempty"`
	Music     string   `json:"music,omitempty"`
	Hashtags  []string `json:"hashtags,omitempty"`
}

func (it Item) Normalize() Video {
	v := Video{ID: it.ID, URL: VideoURL(it.Author.UniqueID, it.ID), Desc: it.Desc, CreatedAt: int64(it.CreateTime),
		Author: it.Author.UniqueID, Nickname: it.Author.Nickname, Duration: int64(it.Video.Duration), Cover: it.Video.Cover,
		Plays: int64(it.Stats.Plays), Likes: int64(it.Stats.Likes), Comments: int64(it.Stats.Comments), Shares: int64(it.Stats.Shares), Saves: int64(it.Stats.Saves)}
	if it.Music.Title != "" {
		v.Music = strings.TrimSpace(it.Music.Title + " - " + it.Music.AuthorName)
	}
	for _, c := range it.Challenges {
		v.Hashtags = append(v.Hashtags, c.Title)
	}
	return v
}

// rehydration is the subset of __UNIVERSAL_DATA_FOR_REHYDRATION__ we read.
type rehydration struct {
	Scope struct {
		VideoDetail *struct {
			StatusCode int    `json:"statusCode"`
			StatusMsg  string `json:"statusMsg"`
			ItemInfo   struct {
				ItemStruct Item `json:"itemStruct"`
			} `json:"itemInfo"`
		} `json:"webapp.video-detail"`
		UserDetail *struct {
			StatusCode int `json:"statusCode"`
			UserInfo   struct {
				User  Author `json:"user"`
				Stats struct {
					Followers Count `json:"followerCount"`
					Following Count `json:"followingCount"`
					Hearts    Count `json:"heartCount"`
					Videos    Count `json:"videoCount"`
				} `json:"stats"`
			} `json:"userInfo"`
		} `json:"webapp.user-detail"`
		AppContext *struct {
			User *struct {
				UID      string `json:"uid"`
				UniqueID string `json:"uniqueId"`
				Nickname string `json:"nickName"`
			} `json:"user"`
		} `json:"webapp.app-context"`
	} `json:"__DEFAULT_SCOPE__"`
}

func decode(raw []byte) (rehydration, error) {
	var r rehydration
	err := json.Unmarshal(raw, &r)
	return r, err
}

// ParseVideoDetail extracts the post from a video page's rehydration data.
func ParseVideoDetail(raw []byte) (Video, error) {
	r, err := decode(raw)
	if err != nil {
		return Video{}, err
	}
	d := r.Scope.VideoDetail
	if d == nil {
		return Video{}, fmt.Errorf("video detail not found in page data")
	}
	if d.ItemInfo.ItemStruct.ID == "" {
		return Video{}, fmt.Errorf("视频不可用（statusCode=%d %s）", d.StatusCode, d.StatusMsg)
	}
	return d.ItemInfo.ItemStruct.Normalize(), nil
}

type Profile struct {
	Author
	URL       string `json:"url"`
	Followers int64  `json:"followers"`
	Following int64  `json:"following"`
	Hearts    int64  `json:"likes_received"`
	Videos    int64  `json:"video_count"`
}

func ParseProfile(raw []byte) (Profile, error) {
	r, err := decode(raw)
	if err != nil {
		return Profile{}, err
	}
	d := r.Scope.UserDetail
	if d == nil || d.UserInfo.User.UniqueID == "" {
		return Profile{}, fmt.Errorf("用户不存在或页面数据缺失")
	}
	u := d.UserInfo
	return Profile{Author: u.User, URL: "https://www.tiktok.com/@" + u.User.UniqueID, Followers: int64(u.Stats.Followers),
		Following: int64(u.Stats.Following), Hearts: int64(u.Stats.Hearts), Videos: int64(u.Stats.Videos)}, nil
}

// ParseLogin reports the signed-in user, or ok=false for guests.
func ParseLogin(raw []byte) (uniqueID, nickname string, ok bool, err error) {
	r, err := decode(raw)
	if err != nil {
		return "", "", false, err
	}
	if c := r.Scope.AppContext; c != nil && c.User != nil && c.User.UID != "" {
		return c.User.UniqueID, c.User.Nickname, true, nil
	}
	return "", "", false, nil
}

var videoPath = regexp.MustCompile(`^/@([^/]+)/(?:video|photo)/(\d+)`)

// VideoURL builds the canonical post URL.
func VideoURL(user, id string) string {
	return "https://www.tiktok.com/@" + url.PathEscape(user) + "/video/" + id
}

// ParseVideoURL returns the author and id from a post URL; short links
// (vm.tiktok.com / vt.tiktok.com) return ok=false and must be opened to resolve.
func ParseVideoURL(raw string) (user, id string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.HasSuffix(u.Hostname(), "tiktok.com") {
		return "", "", false
	}
	m := videoPath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// NormalizeUser accepts "name", "@name" or a profile URL.
func NormalizeUser(s string) (string, error) {
	s = strings.TrimSpace(s)
	if u, err := url.Parse(s); err == nil && strings.HasSuffix(u.Hostname(), "tiktok.com") {
		s = strings.SplitN(strings.Trim(u.Path, "/"), "/", 2)[0]
	}
	s = strings.TrimPrefix(s, "@")
	if s == "" || strings.ContainsAny(s, "/?# ") {
		return "", fmt.Errorf("无法识别的 TikTok 用户: %q", s)
	}
	return s, nil
}
