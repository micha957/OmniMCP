# Self-hosted Community Edition

OmniMCP 的开源仓库只负责可自托管的运行时：MCP Server、Dashboard、插件 SDK、平台插件、持久化账号 profile 和 Chrome 扩展。运行时不会主动连接官方云端，也不会把 Cookie、浏览器内容或账号凭据上传到第三方服务。

## 部署边界

- `go run ./cmd/omnimcp`：本机开发和单机部署。
- `docker compose up --build -d`：自有机器部署。
- `/mcp`：部署实例自己的 MCP HTTP 入口。
- `extension/`：连接部署实例的 Chrome 扩展。

需要让支持扩展连接的平台优先使用用户 Chrome 时，可设置 `OMNIMCP_BROWSER_MODE=extension`。插件 Manifest 的 `browser_mode` 决定是否使用扩展；目前除微信公众号（官方 API）外的平台都走扩展连接；小红书发布在扩展模式下支持标题、正文和图片，话题等高级选项只在 managed 模式可用。

托管服务属于独立产品，可以实现自己的用户、计费、配额和中转层；开源运行时不包含这些实现。托管服务应通过标准 MCP JSON-RPC 调用工具，并使用扩展设置中的服务器地址和 Token。

## 数据边界

账号配置写入部署实例的数据目录，浏览器登录态写入平台和账号隔离的 Chromium profile。扩展只向配置的服务器地址发请求，默认地址是 `http://127.0.0.1:8090`。
