package agentops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PlatformInfo holds what the workbench must know about a platform. Platforms
// without Inbox or Account tools can publish but are not synced automatically.
type PlatformInfo struct {
	Name     string
	TitleMax int
	BodyMax  int
	Inbox    bool
	Account  bool
	Login    bool
}

var platforms = map[string]PlatformInfo{
	"xiaohongshu": {Name: "小红书", TitleMax: 20, BodyMax: 1000, Inbox: true, Account: true, Login: true},
	"bilibili":    {Name: "哔哩哔哩", TitleMax: 80, BodyMax: 2000, Inbox: true, Account: true, Login: true},
	"zhihu":       {Name: "知乎", TitleMax: 100, BodyMax: 20000, Inbox: true, Account: true, Login: true},
	"douyin":      {Name: "抖音", TitleMax: 30, BodyMax: 1000, Account: true, Login: true},
	"kuaishou":    {Name: "快手", TitleMax: 30, BodyMax: 500},
	"weibo":       {Name: "微博", TitleMax: 30, BodyMax: 2000},
	"youtube":     {Name: "YouTube", TitleMax: 100, BodyMax: 5000, Login: true},
	"tiktok":      {Name: "TikTok", TitleMax: 100, BodyMax: 2200, Login: true},
}

// writeTools lists every platform write this program may propose. Model output
// only supplies text: it never chooses a tool, account or target.
var writeTools = map[string]bool{
	"xiaohongshu_publish_note": true, "xiaohongshu_publish_video": true, "xiaohongshu_post_comment": true, "xiaohongshu_reply_comment": true,
	"bilibili_publish_video": true, "bilibili_post_comment": true, "bilibili_reply_comment": true,
	"zhihu_publish_article": true, "zhihu_comment": true,
	"douyin_publish_video": true, "douyin_comment": true,
	"kuaishou_publish_video": true, "kuaishou_comment": true,
	"weibo_comment":        true,
	"youtube_upload_video": true, "youtube_post_comment": true,
	"tiktok_publish_video": true, "tiktok_post_comment": true,
}

func isVideo(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".mov", ".webm", ".mkv", ".avi", ".flv":
		return true
	}
	return false
}

// BuildAction maps a reviewed draft to one platform tool call. It checks files
// here so the reviewer is not asked to confirm something that cannot run.
func BuildAction(d Draft, mode string) (string, map[string]any, error) {
	if mode != "publish" && mode != "draft" {
		return "", nil, fmt.Errorf("提交方式须为 publish 或 draft")
	}
	platform := d.platform()
	if _, ok := platforms[platform]; !ok {
		return "", nil, fmt.Errorf("不支持的平台 %s", platform)
	}
	for _, f := range d.AssetPaths {
		if !filepath.IsAbs(f) {
			return "", nil, fmt.Errorf("素材须为本机绝对路径: %s", f)
		}
		if info, err := os.Stat(f); err != nil || info.IsDir() {
			return "", nil, fmt.Errorf("素材文件不存在: %s", f)
		}
	}
	switch d.Kind {
	case "note":
		return buildPost(d, platform, mode)
	case "comment", "reply":
		return buildComment(d, platform)
	}
	return "", nil, fmt.Errorf("未知草稿类型 %s", d.Kind)
}

func buildPost(d Draft, platform, mode string) (string, map[string]any, error) {
	video := ""
	images := []string{}
	for _, f := range d.AssetPaths {
		if isVideo(f) {
			video = f
		} else {
			images = append(images, f)
		}
	}
	needVideo := func() error {
		if video == "" {
			return fmt.Errorf("%s发布需要一个视频文件，请先在草稿里添加素材", platforms[platform].Name)
		}
		return nil
	}
	switch platform {
	case "xiaohongshu":
		if video != "" {
			return "xiaohongshu_publish_video", map[string]any{"title": d.Title, "content": d.Body, "video": video, "declare_ai": true, "mode": mode}, nil
		}
		if len(images) == 0 {
			return "", nil, fmt.Errorf("小红书笔记至少需要一张真实图片，请先在草稿里添加素材")
		}
		return "xiaohongshu_publish_note", map[string]any{"title": d.Title, "content": d.Body, "images": images, "declare_ai": true, "mode": mode}, nil
	case "zhihu":
		return "zhihu_publish_article", map[string]any{"title": d.Title, "content": d.Body, "mode": mode}, nil
	case "bilibili":
		if err := needVideo(); err != nil {
			return "", nil, err
		}
		return "bilibili_publish_video", map[string]any{"file_path": video, "title": d.Title, "content": d.Body, "mode": mode}, nil
	case "douyin":
		if err := needVideo(); err != nil {
			return "", nil, err
		}
		return "douyin_publish_video", map[string]any{"file_path": video, "title": d.Title, "content": d.Body, "mode": mode}, nil
	case "kuaishou":
		if err := needVideo(); err != nil {
			return "", nil, err
		}
		return "kuaishou_publish_video", map[string]any{"file_path": video, "content": d.Body, "mode": mode}, nil
	case "youtube":
		if err := needVideo(); err != nil {
			return "", nil, err
		}
		return "youtube_upload_video", map[string]any{"file_path": video, "title": d.Title, "description": d.Body, "mode": mode}, nil
	case "tiktok":
		if err := needVideo(); err != nil {
			return "", nil, err
		}
		return "tiktok_publish_video", map[string]any{"file_path": video, "caption": d.Body, "mode": mode}, nil
	}
	return "", nil, fmt.Errorf("%s暂不支持发布", platforms[platform].Name)
}

func buildComment(d Draft, platform string) (string, map[string]any, error) {
	reply := d.Kind == "reply"
	missing := func(what string) (string, map[string]any, error) {
		return "", nil, fmt.Errorf("缺少%s，无法定位发送目标；请确认这条内容来自平台读取", what)
	}
	if d.FeedID == "" {
		return missing("内容 ID")
	}
	if reply && d.CommentID == "" {
		return missing("目标评论 ID")
	}
	switch platform {
	case "xiaohongshu":
		if d.Token == "" {
			return missing("xsec_token")
		}
		args := map[string]any{"feed_id": d.FeedID, "xsec_token": d.Token, "content": d.Body}
		if reply {
			args["comment_id"] = d.CommentID
			return "xiaohongshu_reply_comment", args, nil
		}
		return "xiaohongshu_post_comment", args, nil
	case "bilibili":
		if reply {
			root := d.RootID
			if root == "" {
				root = d.CommentID
			}
			return "bilibili_reply_comment", map[string]any{"aid": d.FeedID, "root": root, "parent": d.CommentID, "content": d.Body}, nil
		}
		return "bilibili_post_comment", map[string]any{"aid": d.FeedID, "content": d.Body}, nil
	}
	if reply {
		return "", nil, fmt.Errorf("%s暂不支持回复指定评论", platforms[platform].Name)
	}
	switch platform {
	case "zhihu":
		return "zhihu_comment", map[string]any{"url": d.SourceURL, "content": d.Body}, nil
	case "douyin", "kuaishou", "weibo":
		return platform + "_comment", map[string]any{"id": d.FeedID, "content": d.Body}, nil
	case "youtube":
		return "youtube_post_comment", map[string]any{"video": d.FeedID, "content": d.Body}, nil
	case "tiktok":
		return "tiktok_post_comment", map[string]any{"url": d.SourceURL, "content": d.Body}, nil
	}
	return "", nil, fmt.Errorf("%s暂不支持评论", platforms[platform].Name)
}
