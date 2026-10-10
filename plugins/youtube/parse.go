package youtube

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Video is the normalised form of the several renderers YouTube uses for a
// video card (search, channel grid, related, shorts).
type Video struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Channel    string `json:"channel,omitempty"`
	ChannelURL string `json:"channel_url,omitempty"`
	Views      string `json:"views,omitempty"`
	Duration   string `json:"duration,omitempty"`
	Published  string `json:"published,omitempty"`
	Thumbnail  string `json:"thumbnail,omitempty"`
	Short      bool   `json:"short,omitempty"`
}

// text flattens YouTube's text objects: {simpleText}, {runs:[{text}]} or {content}.
func text(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		s, _ := v.(string)
		return s
	}
	if s, ok := m["simpleText"].(string); ok {
		return s
	}
	if s, ok := m["content"].(string); ok {
		return s
	}
	if runs, ok := m["runs"].([]any); ok {
		var b strings.Builder
		for _, r := range runs {
			b.WriteString(text(r.(map[string]any)["text"]))
		}
		return b.String()
	}
	return ""
}

// dig walks nested maps by key; numeric keys index into arrays.
func dig(v any, path ...string) any {
	for _, k := range path {
		switch cur := v.(type) {
		case map[string]any:
			v = cur[k]
		case []any:
			var i int
			if _, err := fmt.Sscan(k, &i); err != nil || i < 0 || i >= len(cur) {
				return nil
			}
			v = cur[i]
		default:
			return nil
		}
	}
	return v
}

func str(v any) string { s, _ := v.(string); return s }

func lastThumbnail(v any) string {
	thumbs, _ := dig(v, "thumbnails").([]any)
	if len(thumbs) == 0 {
		thumbs, _ = dig(v, "sources").([]any)
	}
	if len(thumbs) == 0 {
		return ""
	}
	return str(dig(thumbs[len(thumbs)-1], "url"))
}

func watchURL(id string) string { return "https://www.youtube.com/watch?v=" + id }

func fromRenderer(r map[string]any) Video {
	id := str(r["videoId"])
	owner := r["ownerText"]
	if owner == nil {
		owner = r["longBylineText"]
	}
	if owner == nil {
		owner = r["shortBylineText"]
	}
	channelURL := str(dig(owner, "runs", "0", "navigationEndpoint", "browseEndpoint", "canonicalBaseUrl"))
	if channelURL != "" {
		channelURL = "https://www.youtube.com" + channelURL
	}
	views := text(r["viewCountText"])
	if views == "" {
		views = text(r["shortViewCountText"])
	}
	return Video{ID: id, URL: watchURL(id), Title: text(r["title"]), Channel: text(owner), ChannelURL: channelURL,
		Views: views, Duration: text(r["lengthText"]), Published: text(r["publishedTimeText"]), Thumbnail: lastThumbnail(r["thumbnail"])}
}

// fromLockup handles the newer lockupViewModel cards.
func fromLockup(l map[string]any) (Video, bool) {
	if t := str(l["contentType"]); t != "" && t != "LOCKUP_CONTENT_TYPE_VIDEO" {
		return Video{}, false
	}
	id := str(l["contentId"])
	meta := dig(l, "metadata", "lockupMetadataViewModel")
	v := Video{ID: id, URL: watchURL(id), Title: text(dig(meta, "title")), Thumbnail: lastThumbnail(dig(l, "contentImage", "thumbnailViewModel", "image"))}
	rows, _ := dig(meta, "metadata", "contentMetadataViewModel", "metadataRows").([]any)
	var parts [][]string
	for _, row := range rows {
		var texts []string
		ps, _ := dig(row, "metadataParts").([]any)
		for _, p := range ps {
			if t := text(dig(p, "text")); t != "" {
				texts = append(texts, t)
			}
		}
		parts = append(parts, texts)
	}
	// Search/channel lockups: row 0 = channel, row 1 = views · published.
	if len(parts) >= 2 {
		if len(parts[0]) > 0 {
			v.Channel = parts[0][0]
		}
		if len(parts[1]) > 0 {
			v.Views = parts[1][0]
		}
		if len(parts[1]) > 1 {
			v.Published = parts[1][1]
		}
	} else if len(parts) == 1 && len(parts[0]) > 0 {
		v.Views = parts[0][0]
		if len(parts[0]) > 1 {
			v.Published = parts[0][1]
		}
	}
	return v, id != ""
}

func fromShortsLockup(s map[string]any) (Video, bool) {
	id := str(dig(s, "onTap", "innertubeCommand", "reelWatchEndpoint", "videoId"))
	if id == "" {
		id = strings.TrimPrefix(str(s["entityId"]), "shorts-shelf-item-")
	}
	return Video{ID: id, URL: "https://www.youtube.com/shorts/" + id, Title: text(dig(s, "overlayMetadata", "primaryText")),
		Views: text(dig(s, "overlayMetadata", "secondaryText")), Thumbnail: lastThumbnail(dig(s, "thumbnail")), Short: true}, id != ""
}

// ParseVideos collects every video card in an ytInitialData document, in
// page order and without duplicates.
func ParseVideos(raw []byte) ([]Video, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("decode ytInitialData: %w", err)
	}
	var out []Video
	seen := map[string]bool{}
	add := func(v Video) {
		if v.ID != "" && !seen[v.ID] {
			seen[v.ID] = true
			out = append(out, v)
		}
	}
	var walk func(any)
	walk = func(n any) {
		switch cur := n.(type) {
		case map[string]any:
			for _, key := range []string{"videoRenderer", "gridVideoRenderer", "compactVideoRenderer"} {
				if r, ok := cur[key].(map[string]any); ok {
					add(fromRenderer(r))
				}
			}
			if l, ok := cur["lockupViewModel"].(map[string]any); ok {
				if v, ok := fromLockup(l); ok {
					add(v)
				}
			}
			if s, ok := cur["shortsLockupViewModel"].(map[string]any); ok {
				if v, ok := fromShortsLockup(s); ok {
					add(v)
				}
			}
			if r, ok := cur["reelItemRenderer"].(map[string]any); ok {
				id := str(r["videoId"])
				add(Video{ID: id, URL: "https://www.youtube.com/shorts/" + id, Title: text(r["headline"]), Views: text(r["viewCountText"]), Short: true})
			}
			for _, v := range cur {
				walk(v)
			}
		case []any:
			for _, v := range cur {
				walk(v)
			}
		}
	}
	walk(root)
	return out, nil
}

// Channel holds channel metadata from a channel page's ytInitialData.
type Channel struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
	Avatar      string `json:"avatar,omitempty"`
}

func ParseChannel(raw []byte) (Channel, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return Channel{}, err
	}
	m := dig(root, "metadata", "channelMetadataRenderer")
	if m == nil {
		return Channel{}, fmt.Errorf("channel metadata not found")
	}
	return Channel{ID: str(dig(m, "externalId")), Title: str(dig(m, "title")), Description: str(dig(m, "description")),
		URL: str(dig(m, "vanityChannelUrl")), Avatar: lastThumbnail(dig(m, "avatar"))}, nil
}

// VideoDetail is built from ytInitialPlayerResponse.
type VideoDetail struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Channel     string   `json:"channel"`
	ChannelID   string   `json:"channel_id"`
	Seconds     string   `json:"length_seconds"`
	Views       string   `json:"views"`
	Keywords    []string `json:"keywords,omitempty"`
	Published   string   `json:"publish_date,omitempty"`
	Category    string   `json:"category,omitempty"`
	IsLive      bool     `json:"is_live,omitempty"`
	Thumbnail   string   `json:"thumbnail,omitempty"`
	Likes       string   `json:"likes,omitempty"`
}

func ParsePlayer(raw []byte) (VideoDetail, error) {
	var p struct {
		VideoDetails struct {
			VideoID          string   `json:"videoId"`
			Title            string   `json:"title"`
			ShortDescription string   `json:"shortDescription"`
			Author           string   `json:"author"`
			ChannelID        string   `json:"channelId"`
			LengthSeconds    string   `json:"lengthSeconds"`
			ViewCount        string   `json:"viewCount"`
			Keywords         []string `json:"keywords"`
			IsLiveContent    bool     `json:"isLiveContent"`
			Thumbnail        any      `json:"thumbnail"`
		} `json:"videoDetails"`
		Microformat struct {
			R struct {
				PublishDate string `json:"publishDate"`
				Category    string `json:"category"`
			} `json:"playerMicroformatRenderer"`
		} `json:"microformat"`
		PlayabilityStatus struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"playabilityStatus"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return VideoDetail{}, err
	}
	d := p.VideoDetails
	if d.VideoID == "" {
		if p.PlayabilityStatus.Reason != "" {
			return VideoDetail{}, fmt.Errorf("视频不可用: %s", p.PlayabilityStatus.Reason)
		}
		return VideoDetail{}, fmt.Errorf("videoDetails not found")
	}
	return VideoDetail{ID: d.VideoID, URL: watchURL(d.VideoID), Title: d.Title, Description: d.ShortDescription, Channel: d.Author,
		ChannelID: d.ChannelID, Seconds: d.LengthSeconds, Views: d.ViewCount, Keywords: d.Keywords, IsLive: d.IsLiveContent,
		Published: p.Microformat.R.PublishDate, Category: p.Microformat.R.Category, Thumbnail: lastThumbnail(d.Thumbnail)}, nil
}

var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// VideoID accepts a bare id or any watch / youtu.be / shorts / embed URL.
func VideoID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if videoIDPattern.MatchString(s) {
		return s, nil
	}
	u, err := url.Parse(s)
	if err == nil {
		host := strings.TrimPrefix(u.Hostname(), "www.")
		host = strings.TrimPrefix(host, "m.")
		var id string
		switch {
		case host == "youtu.be":
			id = strings.Trim(u.Path, "/")
		case host == "youtube.com" || host == "music.youtube.com":
			if v := u.Query().Get("v"); v != "" {
				id = v
			} else if parts := strings.Split(strings.Trim(u.Path, "/"), "/"); len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "live") {
				id = parts[1]
			}
		}
		if videoIDPattern.MatchString(id) {
			return id, nil
		}
	}
	return "", fmt.Errorf("无法识别的 YouTube 视频 ID 或链接: %q", s)
}

// ChannelURL accepts "@handle", a "UC…" channel id, or a channel URL, and
// returns the URL of the requested tab (videos, shorts or streams).
func ChannelURL(s, tab string) (string, error) {
	s = strings.TrimSpace(s)
	if tab == "" {
		tab = "videos"
	}
	var base string
	switch {
	case strings.HasPrefix(s, "@"):
		base = "https://www.youtube.com/" + url.PathEscape(s)
	case strings.HasPrefix(s, "UC") && len(s) == 24:
		base = "https://www.youtube.com/channel/" + s
	default:
		u, err := url.Parse(s)
		if err != nil || !strings.HasSuffix(u.Hostname(), "youtube.com") {
			return "", fmt.Errorf("无法识别的频道: %q（可用 @handle、UC 开头的频道 ID 或频道链接）", s)
		}
		path := strings.TrimRight(u.Path, "/")
		for _, t := range []string{"/videos", "/shorts", "/streams", "/featured"} {
			path = strings.TrimSuffix(path, t)
		}
		base = "https://www.youtube.com" + path
	}
	return base + "/" + tab, nil
}
