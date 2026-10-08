package xiaohongshu

import (
	"encoding/json"
	"testing"
)

func TestInputUsesPublicToolFieldNames(t *testing.T) {
	var got input
	err := json.Unmarshal([]byte(`{"feed_id":"note-1","xsec_token":"token-1","load_comments":true,"unlike":true,"unfavorite":true}`), &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.FeedID != "note-1" || got.XSecToken != "token-1" || !got.LoadComments || !got.Unlike || !got.Unfavorite {
		t.Fatalf("tool input was not decoded: %#v", got)
	}
}
