# Windows 桌面客户端

单一 `windows-app.exe`，内嵌后端 HTTP/WebSocket 服务、前端单页与托盘图标。

## 构建

```bash
make build-windows-app
```

产物：`desktop/windows-app.exe`

## 运行

双击 `windows-app.exe`：

- 不显示控制台窗口。
- 自动进入 Windows 系统托盘。
- 自动通过系统默认浏览器打开 `http://127.0.0.1:18001`。

右键托盘图标：

- **打开 Managi**：再次用默认浏览器打开页面。
- **退出**：关闭 HTTP 服务并退出程序。
