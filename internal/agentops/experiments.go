package agentops

import (
	"sort"
	"time"
)

// Checkpoints compare posts at the same age; raw totals favour whatever is oldest.
var checkpoints = []struct {
	Label string
	After time.Duration
}{{"24h", 24 * time.Hour}, {"72h", 72 * time.Hour}, {"7d", 7 * 24 * time.Hour}}

// Scorecard is one published draft's result at each checkpoint.
type Scorecard struct {
	DraftID    string              `json:"draft_id"`
	Title      string              `json:"title"`
	Platform   string              `json:"platform"`
	Hypothesis string              `json:"hypothesis,omitempty"`
	SentAt     time.Time           `json:"sent_at"`
	Points     map[string]NoteStat `json:"points"`
	Latest     *NoteStat           `json:"latest,omitempty"`
	LatestAt   time.Time           `json:"latest_at,omitzero"`
}

// Scorecards joins published drafts with samples through the receipt's note id.
// A checkpoint uses the first sample taken after it, and is left empty until one
// exists: no number here is ever interpolated or estimated.
func Scorecards(s State) []Scorecard {
	out := []Scorecard{}
	for _, d := range s.Drafts {
		if d.Status != "sent" || d.Kind != "note" || d.PlatformID == "" {
			continue
		}
		card := Scorecard{DraftID: d.ID, Title: d.Title, Platform: d.platform(), Hypothesis: d.Hypothesis, SentAt: d.SentAt, Points: map[string]NoteStat{}}
		for _, m := range s.Samples {
			if m.Watch != "" || m.Platform != d.platform() || m.At.Before(d.SentAt) {
				continue
			}
			for _, n := range m.Notes {
				if n.ID != d.PlatformID {
					continue
				}
				stat := n
				card.Latest, card.LatestAt = &stat, m.At
				for _, c := range checkpoints {
					if _, done := card.Points[c.Label]; !done && m.At.Sub(d.SentAt) >= c.After {
						card.Points[c.Label] = n
					}
				}
			}
		}
		out = append(out, card)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SentAt.After(out[j].SentAt) })
	return out
}
