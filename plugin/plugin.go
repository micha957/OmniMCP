package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
