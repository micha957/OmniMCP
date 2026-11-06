package browser

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
)

const chromeMacPath = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

// SharedOptions configures the single browser all managed sessions run in.
type SharedOptions struct {
	Headless bool
	DataDir  string
	// Port is the fixed DevTools port that lets a restarted server find the
	// browser it started earlier. It listens on loopback only.
	Port string
	// Bin overrides the browser binary; empty prefers installed Google Chrome.
	Bin string
}

// Shared is one long-lived browser with one tab per platform.
type Shared struct {
	browser  *rod.Browser
	launched bool
	headless bool
}

// ConnectShared attaches to the browser already on the port, or starts one. The
// browser outlives this process, so restarting the server keeps tabs and logins.
func ConnectShared(o SharedOptions) (*Shared, error) {
	if o.DataDir == "" {
		o.DataDir = "data"
	}
	addr := net.JoinHostPort("127.0.0.1", o.Port)
	if u, err := launcher.ResolveURL(addr); err == nil {
		b := rod.New().ControlURL(u).NoDefaultDevice()
		if err := b.Connect(); err == nil {
			return &Shared{browser: b, headless: o.Headless}, nil
		}
	}
	profile := filepath.Join(o.DataDir, "profiles", "shared")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, err
	}
	l := launcher.New().UserDataDir(profile).Headless(o.Headless).Leakless(false).Set(flags.RemoteDebuggingPort, o.Port)
	bin := o.Bin
	if bin == "" {
		if _, err := os.Stat(chromeMacPath); err == nil {
			bin = chromeMacPath
		}
	}
	if bin != "" {
		l = l.Bin(bin)
	}
	u, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("启动浏览器失败: %w", err)
	}
	b := rod.New().ControlURL(u).NoDefaultDevice()
	if err := b.Connect(); err != nil {
		return nil, err
	}
	return &Shared{browser: b, launched: true, headless: o.Headless}, nil
}

// Alive reports whether the browser still answers; the person may have quit it.
func (s *Shared) Alive() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := (proto.BrowserGetVersion{}).Call(s.browser.Context(ctx))
	return err == nil
}

// Tab returns a handle on the tab with this target id, reopening it if the tab
// is gone. onCreate receives the id of a newly created tab so it can be saved.
func (s *Shared) Tab(targetID string, onCreate func(string)) *RodBrowser {
	b := &RodBrowser{browser: s.browser, onTabCreated: onCreate}
	if targetID == "" {
		return b
	}
	pages, err := s.browser.Pages()
	if err != nil {
		return b
	}
	for _, p := range pages {
		if string(p.TargetID) == targetID {
			b.page = keepActive(p)
			break
		}
	}
	return b
}

// Close leaves a visible browser running on purpose: it holds the person's tabs
// and logins. Only a headless browser this process started is shut down.
func (s *Shared) Close() error {
	if s.launched && s.headless {
		return s.browser.Close()
	}
	return nil
}

// keepActive lets a background tab keep rendering and lazy-loading, because lists
// such as comments only grow while the page believes it is focused.
func keepActive(p *rod.Page) *rod.Page {
	_ = (proto.EmulationSetFocusEmulationEnabled{Enabled: true}).Call(p)
	return p
}
