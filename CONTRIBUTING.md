# Contributing

OmniMCP 是一个可自托管的网页自动化 MCP 运行时。平台插件负责业务流程，运行底座负责 MCP、账号隔离、浏览器连接、确认和日志。

新增平台插件：

1. 创建 `plugins/<platform>/`。
2. 实现 `plugin.Plugin` 并声明平台 Manifest。
3. 通过 `plugin.Context.Browser` 和 `plugin.Context.Auth` 使用浏览器能力。
4. 在 `cmd/omnimcp/main.go` 注册插件。
5. 为参数校验和失败路径添加测试。

提交前运行：

```bash
go test ./...
go vet ./...
```
