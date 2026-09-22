package core

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugins/xiaohongshu"
	"os"
	"strings"
	"testing"
)

func TestToolsAreRegisteredFromPlugin(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(xiaohongshu.New()); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("xiaohongshu_search_notes"); !ok {
		t.Fatal("tool missing")
	}
}

func TestDisabledPluginIsHidden(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(xiaohongshu.New()); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEnabled("xiaohongshu", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Lookup("xiaohongshu_search_notes"); !ok {
		t.Fatal("lookup should remain available for direct internal use")
	}
	if len(r.Tools()) != 0 {
		t.Fatal("disabled plugin tools should not be listed")
	}
}

func TestArtifactsStayInsideRoot(t *testing.T) {
	d, err := os.MkdirTemp("", "omnimcp-artifacts-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(d)
	a, err := NewLocalArtifacts(d)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Save(context.Background(), "../note.txt", strings.NewReader("ok"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, d+string(os.PathSeparator)) {
		t.Fatalf("artifact escaped root: %s", p)
	}
}
func TestMCPCall(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(xiaohongshu.New())
	s := &Server{Registry: r, Logger: DefaultLogger(), Browser: plugin.NewMemoryBrowser()}
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"xiaohongshu_search_notes","arguments":{"keyword":"AI"}}}` + "\n"
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v["error"] != nil {
		t.Fatalf("unexpected error: %v", v)
	}
}
