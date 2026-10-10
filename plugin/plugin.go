package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"
)

type Manifest struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	APIVersion      string   `json:"api_version"`
	Capabilities    []string `json:"capabilities,omitempty"`
	RequiresBrowser bool     `json:"requires_browser,omitempty"`
	LoginURL        string   `json:"login_url,omitempty"`
	CookieDomains   []string `json:"cookie_domains,omitempty"`
	BrowserMode     string   `json:"browser_mode,omitempty"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Mutating    bool            `json:"-"`
	Confirm     bool            `json:"-"`
	// Timeout overrides the runtime default for long browser workflows.
	Timeout time.Duration `json:"-"`
}

type Account struct{ PluginID, AccountID string }

type Browser interface {
	Open(context.Context, string) error
	Snapshot(context.Context) (string, error)
	Click(context.Context, string) error
	Fill(context.Context, string, string) error
}

type Auth interface {
	OpenLogin(context.Context, string) error
	CookieHeader(context.Context, string) (string, error)
}

// Uploader is optional because only browser based publishing workflows need it.
// Implementations stream the selected local file into the user's browser.
type Uploader interface {
	Upload(context.Context, string, string) error
}

type ArtifactStore interface {
	Save(context.Context, string, io.Reader) (string, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Remove(context.Context, string) error
}

type Context struct {
	Account   Account
	Browser   Browser
	Auth      Auth
	Logger    *slog.Logger
	Confirm   func(context.Context, string) error
	RequestID any
	ToolName  string
	Artifacts ArtifactStore
}

type Plugin interface {
	Manifest() Manifest
	Tools() []Tool
	Execute(context.Context, *Context, string, json.RawMessage) (any, error)
}

func ToolSchema(properties string, required []string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":%s,"required":%s}`, properties, mustJSON(required)))
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// StructuredReader exposes bounded platform DOM extraction without arbitrary JS.
type StructuredReader interface {
	ReadZhihu(context.Context, string, int) (json.RawMessage, error)
}

// ElementInfo describes the first element matching a selector.
type ElementInfo struct {
	Exists  bool              `json:"exists"`
	Count   int               `json:"count"`
	Visible bool              `json:"visible"`
	Text    string            `json:"text"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// Inspector lets plugins verify page state instead of assuming an action worked.
// Both the extension connector and the managed Chromium implement it.
type Inspector interface {
	Query(context.Context, string) (ElementInfo, error)
	// WaitFor polls until selector matches (when set) and text appears inside it,
	// or anywhere on the page when selector is empty.
	WaitFor(ctx context.Context, selector, text string, timeout time.Duration) error
	// Scroll scrolls the element into view, or the page to the bottom when selector is empty.
	Scroll(context.Context, string) error
	CurrentURL(context.Context) (string, error)
}

// StateReader reads a JSON-serialisable page global such as
// "__INITIAL_STATE__.note.noteDetailMap". Vue refs (.value/._value) are unwrapped.
type StateReader interface {
	ReadGlobal(context.Context, string) (json.RawMessage, error)
}

// ClickOptions refines TrustedInput.ClickElement.
type ClickOptions struct {
	// Text picks the first visible match whose text contains it.
	Text string
	// Exact requires the trimmed text to equal Text, so "发布" does not
	// match "定时发布".
	Exact bool
	// XRatio clicks at this horizontal fraction of the element (0 means centre),
	// for composite widgets such as buttons inside a shadow root.
	XRatio float64
}

// TrustedInput produces real (isTrusted) mouse and keyboard events. Platforms
// that ignore synthetic DOM events or render controls in shadow roots need it.
type TrustedInput interface {
	// ClickElement fails if no visible, enabled element matches.
	ClickElement(ctx context.Context, selector string, opt ClickOptions) error
	// TypeText focuses the element and inserts value as real keyboard input.
	TypeText(ctx context.Context, selector, value string) error
	// PressKey sends a real key press to the focused element, e.g. "Enter".
	PressKey(ctx context.Context, key string) error
}

// Field selects one value inside an extracted item. Selector is relative to
// the item (empty means the item itself); Attr is "text" (default), "href",
// or any attribute name.
type Field struct {
	Selector string `json:"selector,omitempty"`
	Attr     string `json:"attr,omitempty"`
}

// ExtractSpec declares a list to pull out of the DOM, so plugins can read
// structured data without shipping page JavaScript.
type ExtractSpec struct {
	Item   string           `json:"item"`
	Fields map[string]Field `json:"fields"`
	Limit  int              `json:"limit,omitempty"`
}

// Extractor reads structured data from the current page.
type Extractor interface {
	// ExtractList returns one map per matched item; missing fields are "".
	ExtractList(context.Context, ExtractSpec) ([]map[string]string, error)
	// ReadScriptJSON returns the JSON body of a <script type="application/json"> element.
	ReadScriptJSON(ctx context.Context, selector string) (json.RawMessage, error)
}
