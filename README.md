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

HTTP MCP 请求需要携带 `Authorization: Bearer <OMNIMCP_API_TOKEN>`。配置通过环境变量读取，上述命令显式加载 `.env`。

默认以 HTTP 服务持续运行。需要 stdio MCP 时，使用 `go run ./cmd/omnimcp -transport stdio`（编译后使用 `bin/omnimcp -transport stdio`）。

## 浏览器扩展

1. 打开 `chrome://extensions/`。
2. 开启开发者模式。
3. 加载 `extension/` 目录。
4. 点击 OmniMCP 图标，填写服务地址、Token、平台和账号。
5. 点击“保存并连接”，再打开平台登录页。

扩展复用当前 Chrome 用户环境，登录态和 Cookie 不需要复制到服务端。平台账号通过“平台 ID + 账号 ID”隔离。

## 已接入能力

- 小红书：登录检查、搜索、推荐、详情、发布、评论、点赞、收藏
- B 站：搜索、视频详情、评论、UP 主视频、发表评论
- 微信公众号：AccessToken、图片上传、图文草稿
- 抖音、快手、知乎、微博：搜索、打开、页面读取、详情、点赞、评论
- 通用浏览器：打开、快照、点击、输入、上传

工具由插件自动注册，客户端只需要连接 OmniMCP 一次。调用时可以传入 `_account_id` 选择账号：

```json
{"_account_id":"main","keyword":"AI"}
```

当前插件采用编译期注册；独立安装和热加载尚未实现。上述工具的网页流程依赖平台页面及登录状态，各平台的发布能力并非全部完成。小红书扩展模式的复杂发布流程仍在迁移，详见 [docs/SELF_HOSTED.md](docs/SELF_HOSTED.md)。

扩展复用所在 Chrome Profile 的登录态。平台和账号 ID 用于连接路由；同一平台的多个真实账号需要使用不同 Chrome Profile。`managed` 模式由服务端为平台和账号创建独立 Chromium Profile。

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
