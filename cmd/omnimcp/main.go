package main

import (
	"context"
	"flag"
	"github.com/heybox/omnimcp/internal/core"
	"github.com/heybox/omnimcp/plugin"
	"github.com/heybox/omnimcp/plugins/bilibili"
	"github.com/heybox/omnimcp/plugins/browser"
	"github.com/heybox/omnimcp/plugins/domestic"
	"github.com/heybox/omnimcp/plugins/tiktok"
	"github.com/heybox/omnimcp/plugins/wechat"
	"github.com/heybox/omnimcp/plugins/xiaohongshu"
	"github.com/heybox/omnimcp/plugins/youtube"
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
	if err := r.Register(youtube.New()); err != nil {
		panic(err)
	}
	if err := r.Register(tiktok.New()); err != nil {
		panic(err)
	}
	for _, p := range []*domestic.Plugin{
		domestic.New(domestic.Config{ID: "douyin", Name: "抖音", Host: "www.douyin.com",
			CreatorAccount: true,
			SearchTemplate: "https://www.douyin.com/search/%s", DetailTemplate: "https://www.douyin.com/video/%s",
			LikeSelectors: []string{"[data-e2e='video-player-digg']"},
			// Douyin submits on Enter, which PostComment falls back to.
			Comment: plugin.CommentFlow{Inputs: []string{"[data-e2e='comment-input'] [contenteditable='true']", ".comment-input-inner-container [contenteditable='true']", "div[contenteditable='true']"}},
			Creator: &plugin.CreatorFlow{
				URL: "https://creator.douyin.com/creator-micro/content/upload", LoginHint: "/login",
				FileInput: `input[type="file"]`,
				Ready:     []string{`input[placeholder*="标题"]`, `.zone-container[contenteditable="true"]`},
				Title:     []string{`input[placeholder*="标题"]`}, TitleMax: 30,
				Body:    []string{`.zone-container[contenteditable="true"]`, `div[contenteditable="true"][data-placeholder*="简介"]`, `div[contenteditable="true"]`},
				BodyMax: 1000,
				Publish: []plugin.ClickTarget{{Selector: "button", Text: "发布", Exact: true}},
				Draft:   []plugin.ClickTarget{{Selector: "button", Text: "暂存离开", Exact: true}},
				// Douyin leaves the form for the content manager on success.
				RedirectOnSuccess: true,
				PublishedTexts:    []string{"发布成功"},
				DraftTexts:        []string{"暂存成功", "已暂存"},
			}}),
		domestic.New(domestic.Config{ID: "kuaishou", Name: "快手", Host: "www.kuaishou.com",
			SearchTemplate: "https://www.kuaishou.com/search/video?searchKey=%s", DetailTemplate: "https://www.kuaishou.com/short-video/%s",
			LikeSelectors: []string{".like-icon", "[data-testid*='like']"},
			Comment: plugin.CommentFlow{
				Inputs:  []string{"textarea[placeholder*='评论']", "input[placeholder*='评论']", "[contenteditable='true']"},
				Submits: []plugin.ClickTarget{{Selector: "button", Text: "发送"}, {Selector: "button", Text: "发布"}}},
			Creator: &plugin.CreatorFlow{
				URL: "https://cp.kuaishou.com/article/publish/video", LoginHint: "passport.kuaishou.com",
				FileInput:         `input[type="file"]`,
				Ready:             []string{`#work-description-edit`, `div[contenteditable="true"]`},
				Body:              []string{`#work-description-edit`, `div[contenteditable="true"]`},
				BodyMax:           500,
				Publish:           []plugin.ClickTarget{{Selector: "button, [role='button'], div[class*='button']", Text: "发布", Exact: true}},
				Draft:             []plugin.ClickTarget{{Selector: "button, [role='button'], div[class*='button']", Text: "存草稿", Exact: true}, {Selector: "button, [role='button'], div[class*='button']", Text: "保存草稿", Exact: true}},
				Confirm:           []plugin.ClickTarget{{Selector: "[role='dialog'] button, .ant-modal button", Text: "确定", Exact: true}},
				RedirectOnSuccess: true,
				PublishedTexts:    []string{"发布成功"},
				DraftTexts:        []string{"草稿保存成功", "保存成功"},
			}}),
		domestic.New(domestic.Config{ID: "weibo", Name: "微博", Host: "weibo.com",
			SearchTemplate: "https://s.weibo.com/weibo?q=%s", DetailTemplate: "https://weibo.com/%s", DetailPath: true,
			LikeSelectors: []string{"button.woo-like-main", ".woo-like-main"}, LikedMarker: "woo-like-liked",
			Comment: plugin.CommentFlow{
				Inputs:  []string{"textarea[placeholder*='评论']", "textarea"},
				Submits: []plugin.ClickTarget{{Selector: "button", Text: "评论"}}}}),
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
	approvals, err := core.NewApprovalStore(dataDir)
	if err != nil {
		panic(err)
	}
	if err := r.Register(core.NewApprovalsPlugin(approvals)); err != nil {
		panic(err)
	}
	auto, err := core.ParseAutoPolicy(os.Getenv("OMNIMCP_AUTO_APPROVE"))
	if err != nil {
		log.Fatal(err)
	}
	s := &core.Server{Registry: r, Logger: core.DefaultLogger(), Sessions: sessions, Extensions: core.NewExtensionManager(), Artifacts: artifacts, Approvals: approvals, Auto: auto}
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
