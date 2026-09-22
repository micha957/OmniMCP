package plugin

import (
	"context"
	"fmt"
	"sync"
)

// MemoryBrowser is a deterministic browser substitute used by the example and tests.
// Real plugins can provide a Playwright/Rod backed implementation.
type MemoryBrowser struct {
	mu     sync.Mutex
	URL    string
	Values map[string]string
}

func NewMemoryBrowser() *MemoryBrowser { return &MemoryBrowser{Values: map[string]string{}} }
func (b *MemoryBrowser) Open(_ context.Context, url string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.URL = url
	return nil
}
func (b *MemoryBrowser) Snapshot(_ context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return fmt.Sprintf("url=%s values=%v", b.URL, b.Values), nil
}
func (b *MemoryBrowser) Click(_ context.Context, selector string) error {
	if selector == "" {
		return fmt.Errorf("selector is required")
	}
	return nil
}
func (b *MemoryBrowser) Fill(_ context.Context, selector, value string) error {
	if selector == "" {
		return fmt.Errorf("selector is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Values[selector] = value
	return nil
}
