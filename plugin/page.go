package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Page bundles every optional connector capability. Page-driven platform
// plugins call NewPage once instead of type-asserting each interface.
type Page struct {
	Browser
	Inspector
	StateReader
	TrustedInput
	Uploader
	Extractor
}

func NewPage(b Browser) (*Page, error) {
	i, ok1 := b.(Inspector)
	s, ok2 := b.(StateReader)
	t, ok3 := b.(TrustedInput)
	u, ok4 := b.(Uploader)
	e, ok5 := b.(Extractor)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
		return nil, fmt.Errorf("browser connector is missing inspect/state/input/upload/extract support; reload the OmniMCP extension")
	}
	return &Page{b, i, s, t, u, e}, nil
}

// Poll calls fn until it reports done, fails permanently via ctx, or the
// timeout passes. The last fn error is returned on timeout.
func Poll(ctx context.Context, timeout, interval time.Duration, fn func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		done, err := fn()
		if done {
			return nil
		}
		if err != nil {
			last = err
		}
		if time.Now().After(deadline) {
			if last == nil {
				last = fmt.Errorf("timed out after %s", timeout)
			}
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// PollGlobal re-reads a page global until accept returns true; SPA pages
// hydrate their state after the load event.
func (p *Page) PollGlobal(ctx context.Context, path string, timeout time.Duration, accept func(json.RawMessage) bool) (json.RawMessage, error) {
	var raw json.RawMessage
	err := Poll(ctx, timeout, 400*time.Millisecond, func() (bool, error) {
		v, err := p.ReadGlobal(ctx, path)
		if err != nil {
			return false, err
		}
		raw = v
		return accept == nil || accept(v), nil
	})
	if err != nil {
		return nil, fmt.Errorf("page state %s not ready: %w", path, err)
	}
	return raw, nil
}

// ExtractUntil scrolls and re-extracts until at least want items are loaded or
// the list stops growing.
func (p *Page) ExtractUntil(ctx context.Context, spec ExtractSpec, want int, scrollSelector string, rounds int) ([]map[string]string, error) {
	spec.Limit = want
	var items []map[string]string
	stalled := 0
	for i := 0; i <= rounds; i++ {
		got, err := p.ExtractList(ctx, spec)
		if err != nil {
			return nil, err
		}
		if len(got) <= len(items) {
			stalled++
		} else {
			stalled = 0
		}
		items = got
		if len(items) >= want || stalled >= 3 || i == rounds {
			break
		}
		_ = p.Scroll(ctx, scrollSelector)
		select {
		case <-ctx.Done():
			return items, ctx.Err()
		case <-time.After(1200 * time.Millisecond):
		}
	}
	return items, nil
}
