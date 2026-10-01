# Windows 桌面客户端

`managi-desktop-windows-amd64.exe`：与服务器形态**同一个二进制**（`backend/cmd/managi`），
只是编译时带 `-tags desktop`（内嵌前端单页 + 托盘图标）并默认以 `-tray` 启动。

## 构建

```bash
make build-desktop
```

产物：`desktop/managi-desktop-windows-amd64.exe`

构建前会把 `frontend/dist/index.html` 与 `desktop/icon.ico` 复制进
`backend/internal/desktop/` 供 `go:embed` 使用，这两个文件与生成的 `.syso` 都不入库。

## 运行

双击 exe：

- 不显示控制台窗口（`-H=windowsgui` 链接）。
- 自动进入 Windows 系统托盘。
- 服务就绪后自动用系统默认浏览器打开 `http://127.0.0.1:18001`。
- 18001 被占用时依次尝试 18002…18005，并按实际端口打开页面；五个端口都不可用、
  或启动其它错误时，失败原因显示在托盘图标提示与右键菜单里（不会一闪就退出）。
- 桌面形态**只监听 127.0.0.1**，不接受外部连接。

右键托盘图标：

- **打开 Managi**：再次用默认浏览器打开当前服务地址。
- **退出**：关闭 HTTP 服务与全部 SSH 连接后退出程序。

## 命令行

同一份 exe 也能当服务器用（调试时有用，注意 windowsgui 链接后没有控制台输出）：

```bash
managi-desktop-windows-amd64.exe -tray=false -host 0.0.0.0 -port 18001
```

配置项与服务器形态完全一致，仍只认 5 个环境变量（`MANAGI_HOST` / `MANAGI_PORT` /
`MANAGI_AUTH` / `MANAGI_INDEX_HTML` / `MANAGI_KNOWN_HOSTS`）；桌面版用不到
`MANAGI_INDEX_HTML`，首页始终来自内嵌资源。
