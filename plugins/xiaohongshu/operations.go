package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugins/xiaohongshu/engine"
)

const (
	selEngageBar    = ".engage-bar"
	creatorNotesURL = "https://creator.xiaohongshu.com/new/note-manager"
	notificationURL = "https://www.xiaohongshu.com/notification"
	selShowMore     = ".comments-container .show-more"
	selLastComment  = ".comments-container .parent-comment:last-child"
	selCommentEnd   = ".comments-container .end-container"
)

// commentIDs come back from the platform but end up inside a CSS selector.
var commentIDPattern = regexp.MustCompile(`^[0-9A-Za-z_-]{1,64}$`)

var notificationTabs = map[engine.NotificationTab]string{
	engine.TabMentions:    "评论和@",
	engine.TabLikes:       "赞和收藏",
	engine.TabConnections: "新增关注",
}

// selfUser reads the logged-in account from page state; userInfo may hydrate late.
func (p *extensionPage) selfUser(ctx context.Context) (id, nickname string, err error) {
	raw, err := p.PollGlobal(ctx, "__INITIAL_STATE__.user.userInfo", 6*time.Second, func(raw json.RawMessage) bool { return string(raw) != "null" })
	if err != nil {
		return "", "", fmt.Errorf("没有读到登录用户，请先登录小红书: %w", err)
	}
	var u struct {
		Guest    bool   `json:"guest"`
		Nickname string `json:"nickname"`
		UserID   string `json:"userId"`
	}
	if json.Unmarshal(raw, &u) != nil || u.Guest || u.UserID == "" {
		return "", "", fmt.Errorf("小红书未登录")
	}
	return u.UserID, u.Nickname, nil
}

func (p *extensionPage) unreadCount(ctx context.Context) (any, error) {
	if err := p.Open(ctx, "https://www.xiaohongshu.com/explore"); err != nil {
		return nil, err
	}
	raw, err := p.PollGlobal(ctx, "__INITIAL_STATE__.notification.notificationCount", 10*time.Second, func(raw json.RawMessage) bool { return string(raw) != "null" })
	if err != nil {
		return nil, fmt.Errorf("页面状态里没有未读数，可能未登录或页面结构已变化: %w", err)
	}
	return engine.ParseNotificationCount(raw)
}

// notifications scrolls until limit items are loaded. The result says whether
// it reached the end, so a partial read is never mistaken for "no new replies".
func (p *extensionPage) notifications(ctx context.Context, tabName string, limit int) (any, error) {
	tab, err := engine.ParseNotificationTab(tabName)
	if err != nil {
		return nil, err
	}
	if err := p.Open(ctx, notificationURL); err != nil {
		return nil, err
	}
	if u, _ := p.CurrentURL(ctx); strings.Contains(u, "login") {
		return nil, fmt.Errorf("小红书未登录，无法读取通知")
	}
	if tab != engine.TabMentions {
		if _, err := plugin.ClickFirst(ctx, p.Page, []plugin.ClickTarget{{Selector: ".reds-tab-item", Text: notificationTabs[tab], Exact: true}}, 10*time.Second); err != nil {
			return nil, fmt.Errorf("切换到通知分区 %s 失败: %w", notificationTabs[tab], err)
		}
	}
	path := "__INITIAL_STATE__.notification.notificationMap." + string(tab)
	raw, err := p.PollGlobal(ctx, path, 15*time.Second, func(raw json.RawMessage) bool {
		_, status, err := engine.ParseNotifications(raw, limit)
		return err == nil && (status.Loaded > 0 || !status.HasMore)
	})
	if err != nil {
		return nil, fmt.Errorf("读取通知失败，可能未登录或页面结构已变化: %w", err)
	}
	list, status, err := engine.ParseNotifications(raw, limit)
	if err != nil {
		return nil, err
	}
	for i, stalled := 0, 0; i < 20 && stalled < 3 && status.HasMore && status.Loaded < limit; i++ {
		if err := p.Scroll(ctx, ""); err != nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1200 * time.Millisecond):
		}
		next, err := p.ReadGlobal(ctx, path)
		if err != nil {
			break
		}
		l, st, err := engine.ParseNotifications(next, limit)
		if err != nil {
			break
		}
		if st.Loaded <= status.Loaded {
			stalled++
		} else {
			stalled = 0
		}
		list, status = l, st
	}
	return map[string]any{"tab": tab, "items": list.Items, "filtered": list.Filtered, "loaded": status.Loaded, "has_more": status.HasMore, "complete": !status.HasMore}, nil
}

func allCommentIDs(list []engine.Comment, out map[string]bool) {
	for _, c := range list {
		out[c.ID] = true
		allCommentIDs(c.SubComments, out)
	}
}

// findNewComment returns the id of a comment by selfID with this text that was
// not on the page before submitting: the only proof that identifies our comment.
func findNewComment(list []engine.Comment, seen map[string]bool, selfID, content string) string {
	want := strings.TrimSpace(content)
	for _, c := range list {
		if !seen[c.ID] && strings.TrimSpace(c.Content) == want && (selfID == "" || c.UserInfo.UserID == selfID) {
			return c.ID
		}
		if id := findNewComment(c.SubComments, seen, selfID, content); id != "" {
			return id
		}
	}
	return ""
}

// submitComment types into the open editor, sends and returns a receipt. After
// the click the comment may exist, so a missing confirmation is an unknown result.
func (p *extensionPage) submitComment(ctx context.Context, feedID, selfID string, seen map[string]bool, content string) (any, error) {
	if err := p.WaitFor(ctx, selCommentInput, "", 5*time.Second); err != nil {
		return nil, fmt.Errorf("评论输入框未出现: %w", err)
	}
	if err := p.TypeText(ctx, selCommentInput, content); err != nil {
		return nil, err
	}
	if err := p.ClickElement(ctx, selCommentSubmit, plugin.ClickOptions{}); err != nil {
		return nil, err
	}
	commentID := ""
	_ = plugin.Poll(ctx, 8*time.Second, 500*time.Millisecond, func() (bool, error) {
		d, err := p.noteDetail(ctx, feedID)
		if err != nil {
			return false, err
		}
		commentID = findNewComment(d.Comments.List, seen, selfID, content)
		return commentID != "", nil
	})
	if commentID != "" {
		return map[string]any{"success": true, "verified": true, "comment_id": commentID, "feed_id": feedID}, nil
	}
	if err := p.WaitFor(ctx, selComments, probe(content), 10*time.Second); err != nil {
		return nil, fmt.Errorf("已点击发送，但评论区未出现该评论（可能被审核拦截）: %w: %w", plugin.ErrOutcomeUnknown, err)
	}
	return map[string]any{"success": true, "verified": true, "comment_id": "", "feed_id": feedID, "receipt": "评论已显示，但页面状态里没有读到评论 ID"}, nil
}

// prepareNoteComments opens the note and records what exists before writing.
func (p *extensionPage) prepareNoteComments(ctx context.Context, in input) (selfID string, seen map[string]bool, err error) {
	if err := p.Open(ctx, noteURL(in.FeedID, in.XSecToken)); err != nil {
		return "", nil, err
	}
	d, err := p.noteDetail(ctx, in.FeedID)
	if err != nil {
		return "", nil, err
	}
	// Without an identified account the receipt cannot tell our comment apart.
	selfID, _, _ = p.selfUser(ctx)
	seen = map[string]bool{}
	allCommentIDs(d.Comments.List, seen)
	return selfID, seen, nil
}

func (p *extensionPage) postComment(ctx context.Context, in input) (any, error) {
	selfID, seen, err := p.prepareNoteComments(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := p.ClickElement(ctx, selCommentTrigger, plugin.ClickOptions{}); err != nil {
		return nil, fmt.Errorf("未找到评论输入框，该笔记可能不允许评论: %w", err)
	}
	return p.submitComment(ctx, in.FeedID, selfID, seen, in.Content)
}

// locateComment scrolls and expands threads until the comment is in the DOM;
// nested replies only render after their "展开" button is clicked.
func (p *extensionPage) locateComment(ctx context.Context, selector string) error {
	for i := 0; i < 25; i++ {
		if info, err := p.Query(ctx, selector); err == nil && info.Exists {
			return p.Scroll(ctx, selector)
		}
		expanded := p.ClickElement(ctx, selShowMore, plugin.ClickOptions{Text: "展开"}) == nil
		if !expanded {
			if end, err := p.Query(ctx, selCommentEnd); err == nil && end.Exists {
				break
			}
			_ = p.Scroll(ctx, selLastComment)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1200 * time.Millisecond):
		}
	}
	if info, err := p.Query(ctx, selector); err == nil && info.Exists {
		return p.Scroll(ctx, selector)
	}
	return fmt.Errorf("评论区里没有找到目标评论，它可能已被删除或折叠")
}

func (p *extensionPage) replyComment(ctx context.Context, in input) (any, error) {
	selfID, seen, err := p.prepareNoteComments(ctx, in)
	if err != nil {
		return nil, err
	}
	selector := "#comment-" + in.CommentID
	target := ""
	if d, err := p.noteDetail(ctx, in.FeedID); err == nil {
		target = commentAuthor(d.Comments.List, in.CommentID)
	}
	if err := p.locateComment(ctx, selector); err != nil {
		return nil, err
	}
	if err := p.ClickElement(ctx, selector+" .right .interactions .reply", plugin.ClickOptions{}); err != nil {
		return nil, fmt.Errorf("无法点击这条评论的回复按钮: %w", err)
	}
	if err := p.verifyReplyTarget(ctx, target); err != nil {
		return nil, err
	}
	out, err := p.submitComment(ctx, in.FeedID, selfID, seen, in.Content)
	if err != nil {
		return nil, err
	}
	out.(map[string]any)["reply_to"] = in.CommentID
	return out, nil
}

func commentAuthor(list []engine.Comment, id string) string {
	for _, c := range list {
		if c.ID == id {
			return c.UserInfo.Nickname
		}
		if name := commentAuthor(c.SubComments, id); name != "" {
			return name
		}
	}
	return ""
}

// verifyReplyTarget checks the editor before anything is typed: the bar reads
// "回复 <昵称>" once it is really replying. Without that cue the text would go out
// as a top-level comment, or as a reply to someone else.
func (p *extensionPage) verifyReplyTarget(ctx context.Context, nickname string) error {
	want := "回复 " + nickname
	var last string
	err := plugin.Poll(ctx, 4*time.Second, 200*time.Millisecond, func() (bool, error) {
		bar, err := p.Query(ctx, selEngageBar)
		if err != nil {
			return false, err
		}
		last = bar.Text
		return strings.HasPrefix(strings.TrimSpace(bar.Text), want), nil
	})
	if err != nil {
		return fmt.Errorf("回复对象不符，已中止，没有输入内容：期望 %q，页面显示 %q", want, probe(last))
	}
	return nil
}

// MyNote is the per-note data the operations layer samples over time.
type MyNote struct {
	Views     string `json:"views,omitempty"`
	ID        string `json:"id"`
	XSecToken string `json:"xsec_token"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	Likes     string `json:"likes"`
	Collects  string `json:"collects"`
	Comments  string `json:"comments"`
	Shares    string `json:"shares"`
}

func myNotes(feeds []engine.Feed) []MyNote {
	out := make([]MyNote, 0, len(feeds))
	for _, f := range feeds {
		i := f.NoteCard.InteractInfo
		out = append(out, MyNote{ID: f.ID, XSecToken: f.XsecToken, Title: f.NoteCard.DisplayTitle, Type: f.NoteCard.Type, Likes: i.LikedCount, Collects: i.CollectedCount, Comments: i.CommentCount, Shares: i.SharedCount})
	}
	return out
}

// myAccount reads the logged-in account's public profile: follower counts and
// the first loaded page of its notes, each with an xsec_token for follow-up calls.
func (p *extensionPage) myAccount(ctx context.Context) (any, error) {
	if err := p.Open(ctx, "https://www.xiaohongshu.com/explore"); err != nil {
		return nil, err
	}
	id, nickname, err := p.selfUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := p.Open(ctx, "https://www.xiaohongshu.com/user/profile/"+url.PathEscape(id)); err != nil {
		return nil, err
	}
	profile, err := p.readProfile(ctx, engine.TabNotes)
	if err != nil {
		return nil, err
	}
	stats := map[string]string{}
	for _, i := range profile.Interactions {
		stats[i.Type] = i.Count
	}
	notes := myNotes(profile.Feeds)
	out := map[string]any{"user": map[string]string{"user_id": id, "nickname": nickname}, "stats": stats, "notes": notes, "sampled_at": time.Now().UTC()}
	// The public profile only has like counts. The creator centre adds the rest,
	// and an account without creator access still gets the public numbers.
	if err := p.addCreatorStats(ctx, notes); err != nil {
		out["analytics_error"] = err.Error()
	}
	return out, nil
}

// Stat cells in .note-card__body, in display order. Views and likes are checked
// against a real account; the other three still need a note with non-zero values.
const selNoteCard = ".note-card__body"

var noteCardStats = []string{"views", "comments", "likes", "collects", "shares"}

func (p *extensionPage) addCreatorStats(ctx context.Context, notes []MyNote) error {
	if len(notes) == 0 {
		return nil
	}
	if err := p.Open(ctx, creatorNotesURL); err != nil {
		return err
	}
	fields := map[string]plugin.Field{"title": {Selector: ".note-card__title"}}
	for i, name := range noteCardStats {
		fields[name] = plugin.Field{Selector: fmt.Sprintf(".note-card__stat:nth-child(%d)", i+1)}
	}
	var rows []map[string]string
	err := plugin.Poll(ctx, 10*time.Second, 500*time.Millisecond, func() (bool, error) {
		var err error
		rows, err = p.ExtractList(ctx, plugin.ExtractSpec{Item: selNoteCard, Fields: fields, Limit: 50})
		return err == nil && len(rows) > 0, err
	})
	if err != nil {
		return fmt.Errorf("读取笔记管理页失败: %w", err)
	}
	for _, row := range rows {
		for i := range notes {
			if notes[i].Title == strings.TrimSpace(row["title"]) {
				notes[i].Views, notes[i].Comments, notes[i].Likes, notes[i].Collects, notes[i].Shares = row["views"], row["comments"], row["likes"], row["collects"], row["shares"]
			}
		}
	}
	return nil
}

// addNoteReceipt finds the new note on the account page, because publishing
// itself returns no id and follow-up needs one. The post already exists at this
// point, so a failed lookup only leaves the id out and never fails the publish.
func addNoteReceipt(ctx context.Context, c *plugin.Context, out map[string]any, title string) {
	p, err := newExtensionPage(c.Browser)
	if err != nil {
		return
	}
	account, err := p.myAccount(ctx)
	if err != nil {
		return
	}
	notes, _ := account.(map[string]any)["notes"].([]MyNote)
	for _, n := range notes {
		if n.Title == title {
			out["note_id"], out["xsec_token"] = n.ID, n.XSecToken
			return
		}
	}
}

// readProfile extracts the currently open profile page.
func (p *extensionPage) readProfile(ctx context.Context, tab engine.ProfileTab) (*engine.UserProfileResponse, error) {
	userRaw, err := p.PollGlobal(ctx, "__INITIAL_STATE__.user.userPageData", 10*time.Second, func(raw json.RawMessage) bool { return string(raw) != "null" })
	if err != nil {
		return nil, fmt.Errorf("读取用户主页失败: %w", err)
	}
	var page struct {
		Interactions []engine.UserInteractions `json:"interactions"`
		BasicInfo    engine.UserBasicInfo      `json:"basicInfo"`
	}
	if err := json.Unmarshal(userRaw, &page); err != nil {
		return nil, err
	}
	out := &engine.UserProfileResponse{UserBasicInfo: page.BasicInfo, Interactions: page.Interactions}
	if notesRaw, err := p.ReadGlobal(ctx, "__INITIAL_STATE__.user.notes"); err == nil {
		var notes [][]engine.Feed
		if json.Unmarshal(notesRaw, &notes) == nil {
			idx := map[engine.ProfileTab]int{engine.TabNotes: 0, engine.TabFavorites: 1, engine.TabLiked: 2}[tab]
			if idx < len(notes) {
				out.Feeds = notes[idx]
			}
		}
	}
	return out, nil
}
