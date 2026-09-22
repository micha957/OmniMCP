# OmniMCP Browser Connector

这是 OmniMCP Community Edition 的 Chrome MV3 扩展。它运行在用户自己的 Chrome 中，复用用户已经登录的平台页面；它只连接你在扩展设置里填写的自托管服务器，不依赖任何官方云服务，也不需要导出账号密码。

## 本地加载

1. 打开 `chrome://extensions/`。
2. 开启“开发者模式”。
3. 选择“加载已解压的扩展程序”，选择本目录。
4. 在扩展选项中填写自托管服务地址和 API Token。

点击浏览器工具栏中的 OmniMCP 图标，可以在弹窗内完成服务器、平台和账号绑定，查看连接状态，并直接打开平台登录页。浏览器连接相关操作优先在扩展弹窗完成。

当前扩展提供连接设置、页面快照和 MCP 调用桥接。平台插件的具体页面流程继续放在服务端插件目录中，后续通过 Browser Connector 接口接入。

服务端启用扩展浏览器模式：

```bash
OMNIMCP_BROWSER_MODE=extension
```

扩展通过 `/extension/ws?plugin_id=<id>&account_id=<id>` 建立账号隔离连接；服务端不会把普通 Chrome profile 复制到自己的 Chromium profile。
