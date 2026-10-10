# OmniMCP Community Edition

OmniMCP 是一个可自托管的网页自动化 MCP 运行时。服务端负责 MCP 协议、插件路由、账号隔离、权限确认和日志；Chrome 扩展负责在用户自己的浏览器中执行需要登录态的网页操作。

本仓库是完整的本地部署版本，不依赖官方云服务，不包含云账号、计费、遥测或平台中转。可以部署在本机、NAS、云服务器或企业内网。

## 快速开始

```bash
cp .env.example .env
set -a
source .env
set +a
go run ./cmd/omnimcp
```

控制台：`http://127.0.0.1:8090`  
MCP HTTP：`http://127.0.0.1:8090/mcp`  
健康检查：`http://127.0.0.1:8090/healthz`

Docker 部署：

```bash
OMNIMCP_API_TOKEN="change-this-token" docker compose up --build -d
```

本机使用时 `OMNIMCP_API_TOKEN` 可以留空；设置后 `/mcp` 和扩展连接需要携带 `Authorization: Bearer <OMNIMCP_API_TOKEN>`。控制台的 `/api/*` 不做鉴权，只应监听在 `127.0.0.1`。配置通过环境变量读取，上述命令显式加载 `.env`。

MCP 实现：协议版本协商（2025-06-18 / 2025-03-26 / 2024-11-05）、`ping`、批量请求；工具执行失败以 `isError` 结果返回，带 `readOnlyHint` / `destructiveHint` 注解和 `structuredContent`；同一平台账号的浏览器调用串行执行；默认超时 2 分钟（`OMNIMCP_TOOL_TIMEOUT`），发布等长流程单独声明。

默认以 HTTP 服务持续运行。需要 stdio MCP 时，使用 `go run ./cmd/omnimcp -transport stdio`（编译后使用 `bin/omnimcp -transport stdio`）。

## 浏览器扩展

1. 打开 `chrome://extensions/`。
2. 开启开发者模式。
3. 加载 `extension/` 目录。
4. 点击 OmniMCP 图标，填写服务地址、Token（本机可留空）、平台和账号。平台选「全部平台」时，一个连接服务所有平台，每个平台使用各自的标签页。
5. 点击“保存并连接”，再打开平台登录页。

扩展复用当前 Chrome 用户环境，登录态留在浏览器中。B 站接口调用需要时会读取 `bilibili.com` 的 Cookie 交给服务端。点击、输入和按键通过 `chrome.debugger` 发送真实输入事件，执行时 Chrome 会短暂显示“正在调试此浏览器”提示；升级扩展后需在 `chrome://extensions/` 重新加载以授予该权限。

## 已接入能力

- 小红书：登录检查、搜索、推荐、笔记详情和评论、用户主页、图文发布（`publish_note`，直接发布）、评论、点赞、收藏。扩展模式从页面 `__INITIAL_STATE__` 读取结构化数据，与 managed 模式返回格式一致；写操作以页面状态确认结果
- B 站：搜索、视频详情、评论、UP 主视频、发表评论
- 微信公众号：AccessToken、图片上传、图文草稿
- 知乎：作者文章目录、文章正文、搜索、问题/回答/文章详情、赞同和评论（提交后确认）、文章编辑器填写
- 抖音、快手、微博：搜索、打开、页面读取、详情、点赞、评论（评论提交后确认；抖音、快手无法读取点赞状态，返回 `verified:false`）
- YouTube：登录检查、搜索（视频/Shorts）、视频详情（完整简介、播放量、点赞数、关键词）、评论（滚动加载）、频道信息和视频列表、点赞（按钮状态确认）、评论（提交后确认）、通过 YouTube Studio 上传视频（默认私享）
- TikTok：登录检查、搜索、视频详情（精确的播放/点赞/评论/分享/收藏数、音乐、话题）、评论（滚动加载）、用户资料和作品列表、点赞、评论、通过 TikTok Studio 发布视频
- 通用浏览器：打开、快照、地址、元素查询、等待、滚动、读取页面状态、真实点击/输入/按键、上传

工具由插件自动注册，客户端只需要连接 OmniMCP 一次。调用时可以传入 `_account_id` 选择账号：

```json
{"_account_id":"main","keyword":"AI"}
```

YouTube 和 TikTok 需要浏览器能直接访问对应站点。视频上传按 4MB 分片传给扩展，不受 Chrome 64MB 消息上限限制。

当前插件采用编译期注册；独立安装和热加载尚未实现。网页流程依赖平台页面结构及登录状态，选择器随平台改版可能失效，失败时工具返回明确错误而不是假成功。小红书扩展模式的发布暂不支持话题、定时、原创声明等选项（managed 模式支持话题），详见 [docs/SELF_HOSTED.md](docs/SELF_HOSTED.md)。

扩展复用所在 Chrome Profile 的登录态。平台和账号 ID 用于连接路由；同一平台的多个真实账号需要使用不同 Chrome Profile。`managed` 模式由服务端为平台和账号创建独立 Chromium Profile。

## 发布与草稿

所有发布类工具都有必填参数 `mode`：`publish` 直接发布，`draft` 存草稿。结果以页面跳转或平台的成功提示确认，不会只因为点了按钮就报成功；存草稿时绝不会点击发布按钮或发布确认弹窗。

| 平台 | 工具 | 内容 | 草稿 |
| --- | --- | --- | --- |
| 小红书 | `xiaohongshu_publish_note` / `xiaohongshu_publish_video` | 图文 / 视频 | 「暂存离开」（仅扩展模式） |
| 抖音 | `douyin_publish_video` | 视频，标题 ≤30 字 | 「暂存离开」 |
| 快手 | `kuaishou_publish_video` | 视频 | 「存草稿」 |
| B 站 | `bilibili_publish_video` | 视频，支持标签 | 「存草稿」 |
| YouTube | `youtube_upload_video` | 视频，可选私享/不公开/公开 | 关闭上传向导后保留为草稿 |
| TikTok | `tiktok_publish_video` | 视频 | 「Save draft」 |
| 知乎 | `zhihu_publish_article` | 专栏文章 | 编辑器自动保存 |
| 微信公众号 | `wechat_create_draft` → `wechat_publish_draft` | 图文（官方 API） | 官方草稿箱 |

视频在提交按钮可用（转码完成）之前会持续等待，最长约 15–25 分钟。

## 配置

| 变量 | 作用 |
| --- | --- |
| `OMNIMCP_WEB_ADDR` | 控制台和 HTTP MCP 监听地址 |
| `OMNIMCP_API_TOKEN` | HTTP MCP、扩展连接认证 |
| `OMNIMCP_BROWSER_MODE` | `extension` 使用 Chrome 扩展；`managed` 使用独立 Chromium |
| `OMNIMCP_HEADLESS` | 独立 Chromium 是否无头运行 |
| `OMNIMCP_DATA_DIR` | 账号配置和浏览器数据目录 |
| `OMNIMCP_ARTIFACT_DIR` | 临时素材目录 |

微信公众号默认读取 `WECHAT_APP_ID` / `WECHAT_APP_SECRET`，也支持按账号配置。B 站兼容 `BILIBILI_COOKIE`，网页模式下优先读取扩展浏览器 Cookie。

## 开发插件

插件实现 `plugin.Plugin`：

```go
type Plugin interface {
    Manifest() Manifest
    Tools() []Tool
    Execute(context.Context, *Context, string, json.RawMessage) (any, error)
}
```

底座通过 `plugin.Context` 提供浏览器、认证、账号、素材、日志和确认能力。开发约定见 [CONTRIBUTING.md](CONTRIBUTING.md)，自托管边界见 [docs/SELF_HOSTED.md](docs/SELF_HOSTED.md)。

## 验证

```bash
go test ./...
go vet ./...
node --test extension/*.test.*
go build -o bin/omnimcp ./cmd/omnimcp
```
