# Managi v3

轻量级的 Web 端 SSH 管理工具，支持终端会话、SFTP 文件传输、批量命令执行。

## 特性

- **SSH 终端**：基于 xterm.js 的 Web 终端，支持多会话、窗口大小调整
- **SFTP 文件管理**：浏览、上传（断点续传）、下载（Range 请求）、重命名、删除
- **批量命令**：跨多节点并行执行命令，实时查看输出
- **多平台**：后端 Go 静态二进制（Linux/macOS/Windows, glibc/musl），前端单 HTML 文件，桌面端为托盘网页启动器

## 快速开始

### 跳板机方式（部署到服务器）

```bash
curl -fsSL https://github.com/hochenggang/managi-v3/releases/latest/download/install.sh | bash
```

安装完成后访问 `http://<服务器IP>:18001`。

支持的系统：Alpine、Debian、Ubuntu（amd64/arm64）。

### 客户端方式（本地桌面应用）

[下载 Windows 客户端](https://github.com/hochenggang/managi-v3/releases/latest/download/windows-app.exe)（约 9MB，内嵌服务、前端与托盘）

### 手动运行

```bash
# 下载二进制和前端单页
mkdir -p /opt/managi
wget -O /opt/managi/managi https://github.com/hochenggang/managi-v3/releases/latest/download/managi-linux-amd64
wget -O /opt/managi/index.html https://github.com/hochenggang/managi-v3/releases/latest/download/index.html
chmod +x /opt/managi/managi

# 运行
/opt/managi/managi -port 18001
```

## 配置

配置文件位于 `/etc/managi/config.env`，首次安装时自动生成：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `MANAGI_HOST` | `0.0.0.0` | 监听地址 |
| `MANAGI_PORT` | `18001` | 监听端口 |
| `MANAGI_INDEX_HTML` | `index.html` | 前端单页文件路径 |
| `MANAGI_BASICAUTH_ENABLED` | `false` | 是否启用 Basic Auth |
| `MANAGI_BASICAUTH_USERNAME` | `admin` | Basic Auth 用户名 |
| `MANAGI_BASICAUTH_PASSWORD` | 随机 | Basic Auth 密码 |
| `MANAGI_SSH_TIMEOUT` | `15` | SSH 连接超时（秒） |
| `MANAGI_KEEPALIVE` | `30` | SSH 保活间隔（秒） |

## 服务管理

```bash
# Debian/Ubuntu (systemd)
systemctl status managi
systemctl restart managi

# Alpine (OpenRC)
rc-service managi status
rc-service managi restart
```

## 卸载

```bash
curl -fsSL https://github.com/hochenggang/managi-v3/releases/latest/download/install.sh | bash -s uninstall
```

## 技术栈

- **后端**：Go 1.22 + gorilla/websocket + golang.org/x/crypto/ssh
- **前端**：Vue 3 + TypeScript + Vite + xterm.js
- **桌面端**：go system tray
- **CI/CD**：GitHub Actions（自动构建、测试、发布）

## 开发

```bash
# 后端
cd backend && go test ./...

# 前端
cd frontend && npm ci && npm run dev

# Windows 桌面端（交叉编译）
make build-windows-app
```

## License

MIT
