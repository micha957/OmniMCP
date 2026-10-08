package main

import (
	"context"
	"flag"
	"github.com/heybox/omnimcp/internal/core"
	"github.com/heybox/omnimcp/plugins/bilibili"
	"github.com/heybox/omnimcp/plugins/browser"
	"github.com/heybox/omnimcp/plugins/domestic"
	"github.com/heybox/omnimcp/plugins/wechat"
	"github.com/heybox/omnimcp/plugins/xiaohongshu"
	"github.com/heybox/omnimcp/plugins/zhihu"
	"log"
	"net/http"
	"os"
)

func main() {
	transport := flag.String("transport", "http", "MCP transport: http or stdio")
	flag.Parse()
	if *transport != "http" && *transport != "stdio" {
		log.Fatal("transport must be http or stdio")
	}
	dataDir := os.Getenv("OMNIMCP_DATA_DIR")
	accounts, err := core.NewAccountStore(dataDir)
	if err != nil {
		panic(err)
	}
	r := core.NewRegistry()
	if err := r.Register(xiaohongshu.New()); err != nil {
		panic(err)
	}
	if err := r.Register(wechat.New()); err != nil {
		panic(err)
	}
	if err := r.Register(bilibili.New()); err != nil {
		panic(err)
	}
	if err := r.Register(browser.New()); err != nil {
		panic(err)
	}
	if err := r.Register(zhihu.New()); err != nil {
		panic(err)
	}
	for _, p := range []*domestic.Plugin{
		domestic.New("douyin", "抖音", "www.douyin.com", "https://www.douyin.com/search/%s", "https://www.douyin.com/video/%s", []string{"button[aria-label*='赞']", "[data-e2e='video-player-digg']"}, []string{"textarea[placeholder*='评论']", "input[placeholder*='评论']"}),
		domestic.New("kuaishou", "快手", "www.kuaishou.com", "https://www.kuaishou.com/search/video?searchKey=%s", "https://www.kuaishou.com/short-video/%s", []string{"button[aria-label*='赞']", "[data-testid*='like']"}, []string{"textarea[placeholder*='评论']", "input[placeholder*='评论']"}),
		domestic.New("weibo", "微博", "weibo.com", "https://s.weibo.com/weibo?q=%s", "https://weibo.com/%s", []string{"button[aria-label*='赞']", ".woo-like-main"}, []string{"textarea[placeholder*='评论']", "textarea"}),
	} {
		if err := r.Register(p); err != nil {
			panic(err)
		}
	}
	headless := os.Getenv("OMNIMCP_HEADLESS") != "false"
	sessions := core.NewSessionManager(os.Getenv("OMNIMCP_DATA_DIR"), headless)
	defer sessions.Close()
	artifacts, err := core.NewLocalArtifacts(os.Getenv("OMNIMCP_ARTIFACT_DIR"))
	if err != nil {
		panic(err)
	}
	s := &core.Server{Registry: r, Logger: core.DefaultLogger(), Sessions: sessions, Extensions: core.NewExtensionManager(), Artifacts: artifacts}
	addr := os.Getenv("OMNIMCP_WEB_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8090"
	}
	serveHTTP := func() {
		log.Printf("OmniMCP dashboard: http://%s", addr)
		if err := http.ListenAndServe(addr, (&core.Dashboard{Server: s, Accounts: accounts}).Handler()); err != nil {
			log.Fatalf("dashboard stopped: %v", err)
		}
	}
	if *transport == "http" {
		serveHTTP()
		return
	}
	go serveHTTP()
	if err := s.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		panic(err)
	}
}
