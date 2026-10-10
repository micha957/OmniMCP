package engine

import (
	"encoding/json"
	"fmt"
)

// Helpers for decoding __INITIAL_STATE__ fragments that were read by a
// connector other than rod (e.g. the browser extension's read_global).

// ParseFeeds decodes a feeds list, accepting either the bare array or an
// unserialised Vue ref ({"_value": [...]}) and dropping non-note entries.
func ParseFeeds(raw []byte) ([]Feed, error) {
	var feeds []Feed
	if err := json.Unmarshal(raw, &feeds); err != nil {
		var ref struct {
			Value  []Feed `json:"value"`
			XValue []Feed `json:"_value"`
		}
		if err2 := json.Unmarshal(raw, &ref); err2 != nil {
			return nil, fmt.Errorf("decode feeds: %w", err)
		}
		feeds = ref.Value
		if feeds == nil {
			feeds = ref.XValue
		}
	}
	return onlyNotes(feeds), nil
}

// ParseNoteDetail picks one note out of __INITIAL_STATE__.note.noteDetailMap.
func ParseNoteDetail(raw []byte, feedID string) (*FeedDetailResponse, error) {
	var m map[string]struct {
		Note     FeedDetail  `json:"note"`
		Comments CommentList `json:"comments"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode noteDetailMap: %w", err)
	}
	d, ok := m[feedID]
	if !ok || d.Note.NoteID == "" {
		return nil, fmt.Errorf("feed %s not found in noteDetailMap", feedID)
	}
	return &FeedDetailResponse{Note: d.Note, Comments: d.Comments}, nil
}
