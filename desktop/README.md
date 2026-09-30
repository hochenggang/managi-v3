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
- 服务就绪后自动用系统默认浏览器打开 `http://127.0.0.1:18001`。
- 18001 被占用时依次尝试 18002…18005，并按实际端口打开页面；五个端口都不可用、
  或启动其它错误时，失败原因显示在托盘图标提示与右键菜单里（不会一闪就退出）。

右键托盘图标：

- **打开 Managi**：再次用默认浏览器打开当前服务地址。
- **退出**：关闭 HTTP 服务与全部 SSH 连接后退出程序。
