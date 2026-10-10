package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/heybox/omnimcp/plugin"
)

// dialFakeExtension connects like the Chrome extension and answers commands with reply.
func dialFakeExtension(t *testing.T, m *ExtensionManager, pluginID string, reply func(extensionMessage) extensionMessage) *websocket.Conn {
	t.Helper()
	t.Setenv("OMNIMCP_API_TOKEN", "")
	srv := httptest.NewServer(http.HandlerFunc(m.HandleWS))
	t.Cleanup(srv.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"?plugin_id="+pluginID+"&account_id=a", nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			var msg extensionMessage
			if ws.ReadJSON(&msg) != nil {
				return
			}
			if reply != nil {
				_ = ws.WriteJSON(reply(msg))
			}
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !m.Connected(plugin.Account{PluginID: "p", AccountID: "a"}) {
		if time.Now().After(deadline) {
			t.Fatal("extension never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ws
}

func TestExtensionQueryAndReadGlobal(t *testing.T) {
	m := NewExtensionManager()
	dialFakeExtension(t, m, "p", func(msg extensionMessage) extensionMessage {
		switch msg.Method {
		case "query":
			return extensionMessage{ID: msg.ID, Result: json.RawMessage(`{"ok":true,"exists":true,"count":2,"text":"hi","attrs":{"class":"a b"}}`)}
		case "read_global":
			return extensionMessage{ID: msg.ID, Result: json.RawMessage(`{"json":"{\"x\":1}"}`)}
		}
		return extensionMessage{ID: msg.ID, Error: "unexpected " + msg.Method}
	})
	b := m.Browser(plugin.Account{PluginID: "p", AccountID: "a"})
	info, err := b.(plugin.Inspector).Query(context.Background(), ".x")
	if err != nil || !info.Exists || info.Count != 2 || info.Attrs["class"] != "a b" {
		t.Fatalf("query: %+v %v", info, err)
	}
	raw, err := b.(plugin.StateReader).ReadGlobal(context.Background(), "s")
	if err != nil || string(raw) != `{"x":1}` {
		t.Fatalf("read_global: %s %v", raw, err)
	}
}

func TestExtensionDisconnectFailsPendingCalls(t *testing.T) {
	m := NewExtensionManager()
	ws := dialFakeExtension(t, m, "p", nil)
	done := make(chan error, 1)
	go func() {
		_, err := m.Browser(plugin.Account{PluginID: "p", AccountID: "a"}).Snapshot(context.Background())
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	_ = ws.Close()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "disconnected") {
			t.Fatalf("want disconnect error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending call hung after extension disconnected")
	}
}

func TestAllPlatformsConnectionCarriesScope(t *testing.T) {
	m := NewExtensionManager()
	scopes := make(chan string, 1)
	dialFakeExtension(t, m, AllPlugins, func(msg extensionMessage) extensionMessage {
		scopes <- msg.Scope
		return extensionMessage{ID: msg.ID, Result: json.RawMessage(`{"url":"https://x.test/"}`)}
	})
	u, err := m.Browser(plugin.Account{PluginID: "p", AccountID: "a"}).(plugin.Inspector).CurrentURL(context.Background())
	if err != nil || u != "https://x.test/" {
		t.Fatalf("wildcard connection not used: %q %v", u, err)
	}
	if got := <-scopes; got != "p" {
		t.Fatalf("command scope = %q, want plugin id", got)
	}
	if m.Connected(plugin.Account{PluginID: "p", AccountID: "other"}) {
		t.Fatal("wildcard must stay scoped to its account")
	}
}

func TestUploadIsChunked(t *testing.T) {
	m := NewExtensionManager()
	methods := make(chan string, 16)
	dialFakeExtension(t, m, "p", func(msg extensionMessage) extensionMessage {
		methods <- msg.Method
		return extensionMessage{ID: msg.ID, Result: json.RawMessage(`{"ok":true}`)}
	})
	path := filepath.Join(t.TempDir(), "v.mp4")
	if err := os.WriteFile(path, make([]byte, 2*uploadChunkSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Browser(plugin.Account{PluginID: "p", AccountID: "a"}).(plugin.Uploader).Upload(context.Background(), "input", path); err != nil {
		t.Fatal(err)
	}
	close(methods)
	var got []string
	for m := range methods {
		got = append(got, m)
	}
	if strings.Join(got, ",") != "upload_begin,upload_chunk,upload_chunk,upload_chunk,upload_commit" {
		t.Fatalf("unexpected command sequence: %v", got)
	}
}
