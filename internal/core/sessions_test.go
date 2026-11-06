package core

import (
	"strings"
	"testing"

	"github.com/heybox/omnimcp/plugin"
)

func TestOnePlatformBindsOneAccount(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, true)
	bind := func(platform, account string) error {
		return m.bindAccount(plugin.Account{PluginID: platform, AccountID: account})
	}
	if err := bind("xiaohongshu", "default"); err != nil {
		t.Fatal(err)
	}
	if m.state.Accounts["xiaohongshu"] != "" {
		t.Fatal("default must not claim the platform")
	}
	if err := bind("xiaohongshu", "main"); err != nil {
		t.Fatal(err)
	}
	if err := bind("xiaohongshu", "default"); err != nil {
		t.Fatalf("default follows the bound account: %v", err)
	}
	if err := bind("xiaohongshu", "other"); err == nil || !strings.Contains(err.Error(), "main") {
		t.Fatalf("another account must be refused, not act as main: %v", err)
	}
	if err := bind("bilibili", "other"); err != nil {
		t.Fatalf("platforms are independent: %v", err)
	}
	if err := bind("browser", "a"); err != nil {
		t.Fatal(err)
	}
	if err := bind("browser", "b"); err != nil {
		t.Fatalf("the generic browser has no account: %v", err)
	}
	reloaded := NewSessionManager(dir, true)
	if err := reloaded.bindAccount(plugin.Account{PluginID: "xiaohongshu", AccountID: "other"}); err == nil {
		t.Fatal("binding must survive a restart")
	}
}

func TestTabIDsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	m := NewSessionManager(dir, true)
	m.state.Tabs["zhihu"] = "target-1"
	m.saveState()
	if got := NewSessionManager(dir, true).state.Tabs["zhihu"]; got != "target-1" {
		t.Fatalf("got %q", got)
	}
}
