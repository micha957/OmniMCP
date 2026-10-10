package plugin

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// CreatorFlow describes a creator-centre upload page: open, upload, fill in,
// then either publish or save a draft, and confirm the outcome. Selectors in
// each list are tried in order.
type CreatorFlow struct {
	URL string
	// LoginHint is a URL substring meaning the page redirected to login.
	LoginHint string
	// Tab is clicked before uploading, e.g. switching to "上传视频".
	Tab       *ClickTarget
	FileInput string
	// Ready appears once the file is accepted and the form is shown.
	Ready    []string
	Title    []string
	TitleMax int
	Body     []string
	BodyMax  int
	// TagInput receives each tag followed by Enter.
	TagInput []string
	// UploadDone texts appear when the upload/transcode has finished. Leave
	// empty when the submit button itself waits for the upload.
	UploadDone []string
	Publish    []ClickTarget
	Draft      []ClickTarget
	// Confirm targets are clicked if a confirmation dialog appears after
	// publishing (never in draft mode).
	Confirm []ClickTarget
	// The outcome is confirmed when the page navigates away from the form
	// (RedirectOnSuccess) or when one of the texts appears. Texts must be
	// specific: "草稿" alone also matches a "草稿箱" menu link.
	RedirectOnSuccess bool
	PublishedTexts    []string
	DraftTexts        []string
	UploadTimeout     time.Duration
}

// CreatorPost is what to publish.
type CreatorPost struct {
	Files []string
	Title string
	Body  string
	Tags  []string
	Draft bool
}

// Mode values shared by every publishing tool.
const (
	ModePublish = "publish"
	ModeDraft   = "draft"
)

// ParseMode validates a publishing tool's required mode argument.
func ParseMode(mode string) (draft bool, err error) {
	switch mode {
	case ModePublish:
		return false, nil
	case ModeDraft:
		return true, nil
	}
	return false, fmt.Errorf(`mode must be "publish" or "draft"`)
}

// Validate checks a post against the flow before any page is opened.
func (f CreatorFlow) Validate(post CreatorPost) error {
	if len(post.Files) == 0 {
		return fmt.Errorf("at least one file is required")
	}
	for _, file := range post.Files {
		if _, err := os.Stat(file); err != nil {
			return fmt.Errorf("file %s: %w", file, err)
		}
	}
	if f.TitleMax > 0 && utf8.RuneCountInString(post.Title) > f.TitleMax {
		return fmt.Errorf("title must be <=%d characters", f.TitleMax)
	}
	if f.BodyMax > 0 && utf8.RuneCountInString(post.Body) > f.BodyMax {
		return fmt.Errorf("content must be <=%d characters", f.BodyMax)
	}
	if post.Title != "" && len(f.Title) == 0 {
		return fmt.Errorf("this platform has no title field; put the text in content")
	}
	if post.Draft && len(f.Draft) == 0 {
		return fmt.Errorf("this platform does not support saving drafts")
	}
	if len(post.Tags) > 0 && len(f.TagInput) == 0 {
		return fmt.Errorf("this platform does not support tags here; add #话题 to the content instead")
	}
	return nil
}

// ClickFirst clicks the first target that can be found within timeout.
func ClickFirst(ctx context.Context, p *Page, targets []ClickTarget, timeout time.Duration) (ClickTarget, error) {
	var hit ClickTarget
	err := Poll(ctx, timeout, 500*time.Millisecond, func() (bool, error) {
		var last error
		for _, t := range targets {
			if last = p.ClickElement(ctx, t.Selector, t.options()); last == nil {
				hit = t
				return true, nil
			}
		}
		return false, last
	})
	return hit, err
}

// FirstExisting returns the first selector that matches a visible element.
func (p *Page) FirstExisting(ctx context.Context, selectors []string, timeout time.Duration) string {
	return firstExisting(ctx, p, selectors, timeout)
}

// PageHasAny reports which of texts is currently on the page, if any.
func (p *Page) PageHasAny(ctx context.Context, texts []string) string {
	for _, t := range texts {
		if p.WaitFor(ctx, "", t, 100*time.Millisecond) == nil {
			return t
		}
	}
	return ""
}

// RunCreatorFlow performs the upload and returns a result once the platform
// shows that the post was published or the draft saved.
func RunCreatorFlow(ctx context.Context, p *Page, f CreatorFlow, post CreatorPost) (map[string]any, error) {
	if err := f.Validate(post); err != nil {
		return nil, err
	}
	uploadTimeout := f.UploadTimeout
	if uploadTimeout == 0 {
		uploadTimeout = 15 * time.Minute
	}
	if err := p.Open(ctx, f.URL); err != nil {
		return nil, err
	}
	if u, _ := p.CurrentURL(ctx); f.LoginHint != "" && strings.Contains(u, f.LoginHint) {
		return nil, fmt.Errorf("创作者中心未登录（%s），请先在 Chrome 中登录", u)
	}
	if f.Tab != nil {
		if _, err := ClickFirst(ctx, p, []ClickTarget{*f.Tab}, 15*time.Second); err != nil {
			return nil, fmt.Errorf("切换到「%s」失败: %w", f.Tab.Text, err)
		}
	}
	if err := p.WaitFor(ctx, f.FileInput, "", 30*time.Second); err != nil {
		return nil, fmt.Errorf("没有找到上传入口: %w", err)
	}
	for i, file := range post.Files {
		if err := p.Upload(ctx, f.FileInput, file); err != nil {
			return nil, fmt.Errorf("上传第 %d 个文件失败: %w", i+1, err)
		}
	}
	if len(f.Ready) > 0 && p.FirstExisting(ctx, f.Ready, 3*time.Minute) == "" {
		return nil, fmt.Errorf("上传后没有进入填写页面")
	}
	time.Sleep(time.Second)
	if post.Title != "" {
		sel := p.FirstExisting(ctx, f.Title, 10*time.Second)
		if sel == "" {
			return nil, fmt.Errorf("没有找到标题输入框")
		}
		if err := p.TypeText(ctx, sel, post.Title); err != nil {
			return nil, fmt.Errorf("填写标题失败: %w", err)
		}
	}
	if post.Body != "" {
		sel := p.FirstExisting(ctx, f.Body, 10*time.Second)
		if sel == "" {
			return nil, fmt.Errorf("没有找到正文/简介输入框")
		}
		if err := p.TypeText(ctx, sel, post.Body); err != nil {
			return nil, fmt.Errorf("填写正文失败: %w", err)
		}
		// Hashtags and mentions open suggestion popups that swallow clicks.
		_ = p.PressKey(ctx, "Escape")
	}
	for _, tag := range post.Tags {
		sel := p.FirstExisting(ctx, f.TagInput, 5*time.Second)
		if sel == "" {
			return nil, fmt.Errorf("没有找到标签输入框")
		}
		if err := p.TypeText(ctx, sel, tag); err != nil {
			return nil, err
		}
		if err := p.PressKey(ctx, "Enter"); err != nil {
			return nil, err
		}
		time.Sleep(400 * time.Millisecond)
	}
	if len(f.UploadDone) > 0 {
		err := Poll(ctx, uploadTimeout, 2*time.Second, func() (bool, error) {
			return p.PageHasAny(ctx, f.UploadDone) != "", fmt.Errorf("文件仍在上传或处理中")
		})
		if err != nil {
			return nil, err
		}
	}

	buttons, texts, status := f.Publish, f.PublishedTexts, "published"
	if post.Draft {
		buttons, texts, status = f.Draft, f.DraftTexts, "draft_saved"
	}
	// Submit buttons stay disabled while uploading. Connectors refuse to click
	// disabled elements, so this retries until the button becomes enabled.
	formURL, _ := p.CurrentURL(ctx)
	if _, err := ClickFirst(ctx, p, buttons, uploadTimeout); err != nil {
		return nil, fmt.Errorf("没有找到可点击的%s按钮: %w", map[bool]string{true: "存草稿", false: "发布"}[post.Draft], err)
	}
	confirm := f.Confirm
	if post.Draft {
		confirm = nil
	}
	evidence, err := p.AwaitOutcome(ctx, formURL, f.RedirectOnSuccess, texts, confirm, 90*time.Second)
	if err != nil {
		return nil, err
	}
	u, _ := p.CurrentURL(ctx)
	return map[string]any{"success": true, "status": status, "evidence": evidence, "url": u}, nil
}

// AwaitOutcome waits for proof that a submit worked: navigation away from
// formURL (compared by host and path) when redirect is true, or one of texts
// on the page. Confirm targets are clicked whenever they appear.
func (p *Page) AwaitOutcome(ctx context.Context, formURL string, redirect bool, texts []string, confirm []ClickTarget, timeout time.Duration) (string, error) {
	var evidence string
	err := Poll(ctx, timeout, time.Second, func() (bool, error) {
		for _, c := range confirm {
			if p.ClickElement(ctx, c.Selector, c.options()) == nil {
				return false, nil
			}
		}
		if redirect {
			if u, err := p.CurrentURL(ctx); err == nil && !samePage(u, formURL) {
				evidence = "redirected to " + u
				return true, nil
			}
		}
		if t := p.PageHasAny(ctx, texts); t != "" {
			evidence = t
			return true, nil
		}
		return false, fmt.Errorf("提交后没有看到成功提示（可能有必填项未填写或内容未通过校验）")
	})
	return evidence, err
}

func samePage(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return ua.Host == ub.Host && strings.TrimRight(ua.Path, "/") == strings.TrimRight(ub.Path, "/")
}
