# Managi v3

轻量级的 Web 端 SSH 管理工具，支持终端会话、SFTP 文件传输、批量命令执行。

## 预览

![终端会话](doc/preview/preview1.webp)
![SFTP 文件管理](doc/preview/preview2.webp)
![批量命令执行](doc/preview/preview3.webp)

## 特性

- **SSH 终端**：基于 xterm.js 的 Web 终端，支持多会话、窗口大小调整、断线重连恢复会话
- **SFTP 文件管理**：浏览、上传（断点续传）、下载（Range 请求）、重命名、删除
- **批量命令**：跨多节点并行执行命令，实时查看输出
- **多平台**：后端 Go 静态二进制（Linux/macOS/Windows, glibc/musl），前端单 HTML 文件，桌面端为 Go 托盘启动器

## 快速开始

### 跳板机方式（部署到服务器）

```bash
curl -fsSL -o ./install.sh https://github.com/hochenggang/managi-v3/releases/latest/download/install.sh
chmod +x ./install.sh
sudo ./install.sh
```

根据交互式提示进行安装，安装完成后，访问 `http://<服务器IP>:18001`。

支持的系统：Alpine、Debian、Ubuntu（amd64/arm64）。

安装脚本会按 Release 附带的 `<asset>.sha256` 自动校验下载产物；也可以自己指定预期指纹（注意 `sudo` 需要 `-E` 才保留环境变量）：

```bash
MANAGI_SHA256=<二进制文件的 sha256> sudo -E ./install.sh
```

### 客户端方式（本地桌面应用）

[下载 Windows 客户端](https://github.com/hochenggang/managi-v3/releases/latest/download/windows-app.exe)（约 9MB，内嵌服务、前端与托盘）


## 配置

配置文件位于 `/etc/managi/config.env`，首次安装时自动生成：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `MANAGI_HOST` | `0.0.0.0` | 监听地址 |
| `MANAGI_PORT` | `18001` | 监听端口 |
| `MANAGI_INDEX_HTML` | `index.html` | 前端单页文件路径 |
| `MANAGI_BASICAUTH_ENABLED` | `false` | 是否启用 Basic Auth |
| `MANAGI_BASICAUTH_USERNAME` | `admin` | Basic Auth 用户名 |
| `MANAGI_BASICAUTH_PASSWORD` | 空 | Basic Auth 密码。启用而未配置时服务端生成随机口令并打印到启动日志（每次重启都变），故 docker 部署改为强制从 `.env` 注入（见 `deploy/.env.example`） |
| `MANAGI_TRUST_PROXY` | `false` | 置于反向代理之后时设为 `true`，才按 `X-Forwarded-For` 识别客户端 IP（用于登录失败限流与访问日志）。直连部署保持关闭：任何人伪造该头即可绕开限流 |
| `MANAGI_SSH_TIMEOUT` | `15` | SSH 连接超时（秒） |
| `MANAGI_KEEPALIVE` | `30` | SSH 保活间隔（秒） |
| `MANAGI_SSH_IDLE_TIMEOUT` | `120` | SSH 连接池空闲清理时间（秒） |
| `MANAGI_SSH_POOL_SIZE` | `20` | SSH 连接池常驻连接数上限，超出则驱逐空闲连接 |
| `MANAGI_KNOWN_HOSTS` | 空 | 指向 OpenSSH `known_hosts` 即启用严格主机密钥校验；留空沿用首次信任（TOFU）。文件无法解析时拒绝所有连接，不会退回 TOFU |
| `MANAGI_WS_READ_DEADLINE` | `90` | WebSocket 读超时（秒） |
| `MANAGI_WS_PING_INTERVAL` | `30` | WebSocket Ping 间隔（秒） |
| `MANAGI_SESSION_IDLE_TIMEOUT` | `60` | 终端会话空闲保留时间（秒），前端断开后保留 shell 的时长 |
| `MANAGI_SFTP_CHUNK_SIZE` | `1048576` | SFTP 上传分片大小（字节，上限 8MB）。`upload_init` 响应下发给前端，前端按它切片；WS 单帧读取上限取 2× 该值 |
| `MANAGI_SFTP_DOWNLOAD_CHUNK` | `65536` | WS 下载每帧字节数（上限 1MB）。大文件请走 HTTP Range 流式下载，与此无关 |

## 行为说明

### 终端会话复用

后端维护到目标服务器的 shell 会话，前端断开后保留 `MANAGI_SESSION_IDLE_TIMEOUT`（默认 60 秒）。期间前端重连可复用同一会话（保留工作目录、运行中进程、scrollback）。超时后会话关闭。

### SFTP 下载路径

小文件（≤100MB）通过 WebSocket 下载，大文件自动切换为 HTTP Range 流式下载（`POST /api/sftp/download`），避免浏览器内存溢出。断点续传通过 Range 请求头实现。

### 主机密钥校验（TOFU）

首次连接某主机时记录其公钥（Trust On First Use），后续连接比对公钥，不匹配则拒绝（防中间人攻击）。主机密钥仅进程内有效，重启后重新信任。这是简约设计取舍：持久化主机密钥需引入额外存储与用户交互，当前场景（内网跳板机）可接受。

### 凭据存储

节点配置（含 SSH 密码/私钥）明文存储于浏览器 localStorage。这是工具定位（本机/内网管理端）的取舍：若需更高安全性，建议使用 SSH 密钥认证而非密码。

## 服务管理

```bash
# Debian/Ubuntu (systemd)
systemctl status managi
systemctl restart managi

# Alpine (OpenRC)
rc-service managi status
rc-service managi restart
```


## 技术栈

- **后端**：Go 1.25 + gorilla/websocket + golang.org/x/crypto/ssh
- **前端**：Vue 3 + TypeScript + Vite + xterm.js
- **桌面端**：Go system tray（单二进制内嵌服务与前端）
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
