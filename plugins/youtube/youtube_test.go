package youtube

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugin/plugintest"
)

const searchFixture = `{"contents":{"twoColumnSearchResultsRenderer":{"primaryContents":{"sectionListRenderer":{"contents":[
 {"itemSectionRenderer":{"contents":[
  {"videoRenderer":{"videoId":"aaaaaaaaaaa","title":{"runs":[{"text":"Go "},{"text":"tutorial"}]},
   "ownerText":{"runs":[{"text":"Gopher","navigationEndpoint":{"browseEndpoint":{"canonicalBaseUrl":"/@gopher"}}}]},
   "viewCountText":{"simpleText":"1,234 views"},"lengthText":{"simpleText":"12:34"},"publishedTimeText":{"simpleText":"2 days ago"},
   "thumbnail":{"thumbnails":[{"url":"s.jpg"},{"url":"l.jpg"}]}}},
  {"lockupViewModel":{"contentId":"bbbbbbbbbbb","contentType":"LOCKUP_CONTENT_TYPE_VIDEO","metadata":{"lockupMetadataViewModel":{
   "title":{"content":"New card"},"metadata":{"contentMetadataViewModel":{"metadataRows":[
    {"metadataParts":[{"text":{"content":"Chan B"}}]},{"metadataParts":[{"text":{"content":"9K views"}},{"text":{"content":"1 year ago"}}]}]}}}}}},
  {"lockupViewModel":{"contentId":"PLxyz","contentType":"LOCKUP_CONTENT_TYPE_PLAYLIST"}},
  {"reelShelfRenderer":{"items":[{"shortsLockupViewModel":{"onTap":{"innertubeCommand":{"reelWatchEndpoint":{"videoId":"ccccccccccc"}}},
   "overlayMetadata":{"primaryText":{"content":"A short"},"secondaryText":{"content":"5M views"}}}}]}},
  {"videoRenderer":{"videoId":"aaaaaaaaaaa","title":{"simpleText":"dup"}}}
 ]}}]}}}}}`

func TestParseVideosHandlesAllCardKinds(t *testing.T) {
	vs, err := ParseVideos([]byte(searchFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 {
		t.Fatalf("want 3 unique videos (playlist skipped, duplicate dropped), got %d: %+v", len(vs), vs)
	}
	first := vs[0]
	if first.Title != "Go tutorial" || first.Channel != "Gopher" || first.ChannelURL != "https://www.youtube.com/@gopher" || first.Duration != "12:34" || first.Thumbnail != "l.jpg" {
		t.Fatalf("videoRenderer parsed wrong: %+v", first)
	}
	byID := map[string]Video{}
	for _, v := range vs {
		byID[v.ID] = v
	}
	if l := byID["bbbbbbbbbbb"]; l.Title != "New card" || l.Channel != "Chan B" || l.Views != "9K views" || l.Published != "1 year ago" {
		t.Fatalf("lockup parsed wrong: %+v", l)
	}
	if s := byID["ccccccccccc"]; !s.Short || s.URL != "https://www.youtube.com/shorts/ccccccccccc" {
		t.Fatalf("short parsed wrong: %+v", s)
	}
}

func TestVideoIDAndChannelURL(t *testing.T) {
	for _, in := range []string{"dQw4w9WgXcQ", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=1", "https://youtu.be/dQw4w9WgXcQ", "https://m.youtube.com/shorts/dQw4w9WgXcQ"} {
		if id, err := VideoID(in); err != nil || id != "dQw4w9WgXcQ" {
			t.Errorf("%s: %q %v", in, id, err)
		}
	}
	if _, err := VideoID("https://example.com/watch?v=dQw4w9WgXcQ"); err == nil {
		t.Error("foreign host accepted")
	}
	cases := map[string]string{
		"@gopher":                  "https://www.youtube.com/@gopher/videos",
		"UCabcdefghijklmnopqrstuv": "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos",
		"https://www.youtube.com/@gopher/featured": "https://www.youtube.com/@gopher/videos",
	}
	for in, want := range cases {
		if got, err := ChannelURL(in, ""); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
}

func TestParsePlayerReportsUnavailable(t *testing.T) {
	d, err := ParsePlayer([]byte(`{"videoDetails":{"videoId":"aaaaaaaaaaa","title":"T","author":"A","viewCount":"10","keywords":["k"]},"microformat":{"playerMicroformatRenderer":{"publishDate":"2024-01-02"}}}`))
	if err != nil || d.Title != "T" || d.Published != "2024-01-02" || d.Views != "10" {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := ParsePlayer([]byte(`{"playabilityStatus":{"status":"ERROR","reason":"Video unavailable"}}`)); err == nil {
		t.Fatal("unavailable video should be an error")
	}
}

func TestLikeVideoIsVerifiedAndIdempotent(t *testing.T) {
	page := plugintest.New()
	page.SetElement(selLike, plugin.ElementInfo{Exists: true, Attrs: map[string]string{"aria-pressed": "false"}})
	page.OnClick = func(p *plugintest.Page, sel string, _ plugin.ClickOptions) error {
		if sel == selLike {
			p.SetElement(selLike, plugin.ElementInfo{Exists: true, Attrs: map[string]string{"aria-pressed": "true"}})
		}
		return nil
	}
	run := func() map[string]any {
		out, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "like_video", json.RawMessage(`{"video":"https://youtu.be/dQw4w9WgXcQ"}`))
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}
	if out := run(); out["changed"] != true {
		t.Fatalf("first like: %v", out)
	}
	if out := run(); out["changed"] != false || len(page.Clicks) != 1 {
		t.Fatalf("second like should not click again: %v clicks=%v", out, page.Clicks)
	}
	if page.URL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Fatalf("opened %s", page.URL)
	}
}
