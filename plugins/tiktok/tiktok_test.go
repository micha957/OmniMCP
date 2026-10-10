package tiktok

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugin/plugintest"
)

const videoFixture = `{"__DEFAULT_SCOPE__":{"webapp.app-context":{"user":{"uid":"1","uniqueId":"me","nickName":"Me"}},
 "webapp.video-detail":{"statusCode":0,"itemInfo":{"itemStruct":{"id":"7300000000000000001","desc":"hello #go","createTime":"1700000000",
  "author":{"uniqueId":"alice","nickname":"Alice"},"stats":{"playCount":1000,"diggCount":"50","commentCount":3,"shareCount":1,"collectCount":"2"},
  "video":{"duration":15,"cover":"c.jpg"},"music":{"title":"Song","authorName":"DJ"},"challenges":[{"title":"go"}]}}}}}`

func TestParseVideoDetailAcceptsStringCounts(t *testing.T) {
	v, err := ParseVideoDetail([]byte(videoFixture))
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "7300000000000000001" || v.Likes != 50 || v.Saves != 2 || v.CreatedAt != 1700000000 || v.Author != "alice" ||
		v.URL != "https://www.tiktok.com/@alice/video/7300000000000000001" || v.Music != "Song - DJ" || v.Hashtags[0] != "go" {
		t.Fatalf("%+v", v)
	}
	if u, _, ok, _ := ParseLogin([]byte(videoFixture)); !ok || u != "me" {
		t.Fatal("login user not parsed")
	}
	if _, err := ParseVideoDetail([]byte(`{"__DEFAULT_SCOPE__":{"webapp.video-detail":{"statusCode":10204,"statusMsg":"item doesn't exist"}}}`)); err == nil {
		t.Fatal("missing item should be an error")
	}
}

func TestURLHelpers(t *testing.T) {
	if u, id, ok := ParseVideoURL("https://www.tiktok.com/@bob.x/video/123?lang=en"); !ok || u != "bob.x" || id != "123" {
		t.Fatalf("%s %s %v", u, id, ok)
	}
	if _, _, ok := ParseVideoURL("https://vm.tiktok.com/ZMabc/"); ok {
		t.Fatal("short links must be resolved by opening them")
	}
	for _, in := range []string{"bob", "@bob", "https://www.tiktok.com/@bob?lang=en"} {
		if u, err := NormalizeUser(in); err != nil || u != "bob" {
			t.Errorf("%s: %q %v", in, u, err)
		}
	}
}

func TestSearchFallsBackToAnchorsAndDedupes(t *testing.T) {
	page := plugintest.New()
	page.Lists[`a[href*="/video/"]`] = []map[string]string{
		{"url": "https://www.tiktok.com/@a/video/1", "alt": "first caption", "views": "1.2M"},
		{"url": "https://www.tiktok.com/@a/video/1", "alt": "dup"},
		{"url": "https://www.tiktok.com/@b/video/2", "alt": "second"},
		{"url": "https://www.tiktok.com/@b", "alt": "profile link, not a video"},
	}
	out, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "search_videos", json.RawMessage(`{"keyword":"go lang"}`))
	if err != nil {
		t.Fatal(err)
	}
	items := out.(map[string]any)["items"].([]Video)
	if len(items) != 2 || items[0].Desc != "first caption" || items[0].ViewsText != "1.2M" || items[1].Author != "b" {
		t.Fatalf("%+v", items)
	}
	if !strings.Contains(page.URL, "q=go+lang") {
		t.Fatalf("search URL %s", page.URL)
	}
}

func TestCommentsZipUsersAndTexts(t *testing.T) {
	page := plugintest.New()
	page.Scripts[selData] = videoFixture
	page.SetElement(selCommentText, plugin.ElementInfo{Exists: true})
	page.Lists[selCommentText] = []map[string]string{{"text": "nice"}, {"text": "cool"}}
	page.Lists[selCommentUser] = []map[string]string{{"user": "u1", "self_url": "/@u1"}, {"user": "u2", "url": "/@u2"}}
	out, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "get_comments", json.RawMessage(`{"url":"https://www.tiktok.com/@alice/video/7300000000000000001","limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	items := out.(map[string]any)["items"].([]map[string]string)
	if len(items) != 2 || items[0]["user"] != "u1" || items[0]["user_url"] != "/@u1" || items[1]["text"] != "cool" {
		t.Fatalf("%v", items)
	}
}

func TestUnlikeRefusedWhenStateUnknown(t *testing.T) {
	page := plugintest.New()
	page.Scripts[selData] = videoFixture
	page.SetElement(selLike, plugin.ElementInfo{Exists: true})
	_, err := New().Execute(context.Background(), &plugin.Context{Browser: page}, "like_video", json.RawMessage(`{"url":"https://www.tiktok.com/@alice/video/7300000000000000001","unlike":true}`))
	if err == nil || len(page.Clicks) != 0 {
		t.Fatalf("unlike with unknown state must not click: err=%v clicks=%v", err, page.Clicks)
	}
}
