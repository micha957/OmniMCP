# 编写 OmniMCP 插件

插件是普通 Go 包，不需要实现 MCP 协议。插件只声明平台元信息、工具 Schema，并在 `Execute` 中调用底座提供的 `Context`。

```go
type MyPlugin struct{}

func (MyPlugin) Manifest() plugin.Manifest {
    return plugin.Manifest{ID: "myapp", Name: "我的 App", Version: "0.1.0", APIVersion: "v1"}
}

func (MyPlugin) Tools() []plugin.Tool {
    return []plugin.Tool{{
        Name: "search", Description: "搜索内容",
        InputSchema: plugin.ToolSchema(`{"keyword":{"type":"string"}}`, []string{"keyword"}),
    }}
}
```

将插件导入 `cmd/omnimcp/main.go`，调用一次 `registry.Register(myapp.New())`，它的工具就会自动出现在 `tools/list` 中，并以 `myapp_search` 的形式对 MCP 客户端可见。

后续可以把插件独立成 Git 仓库或 Go module。当前版本采用编译期注册，避免 Go 动态插件在 macOS、Windows 和 Linux 上的 ABI 限制；插件安装器可以在此基础上增加版本下载、校验和配置生成。


## 页面驱动的插件

需要操作网页的插件用 `plugin.NewPage(c.Browser)` 取得完整的连接器能力（查询、等待、读取页面状态、真实点击/输入、上传、列表抽取），扩展和 managed Chromium 都实现了这些能力。

- 优先读页面自带的结构化数据：`ReadGlobal("ytInitialData")`、`ReadScriptJSON("#__UNIVERSAL_DATA_FOR_REHYDRATION__")`。
- 列表用 `ExtractList` / `ExtractUntil` 声明选择器抽取，不需要在插件里写页面 JavaScript。
- 写操作在执行前读取状态、执行后确认结果；评论可直接用 `plugin.PostComment`。
- 测试用 `plugin/plugintest.Page` 模拟页面。
