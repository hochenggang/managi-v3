# Managi v3

轻量级 Web 端 SSH 管理工具：**终端会话、SFTP 文件传输、批量命令执行**。
后端是一个 Go 静态二进制，前端是一个 HTML 文件。

> 架构、协议与各项设计取舍见 **[设计文档 design-v5.md](design-v5.md)**。

## 预览

![终端会话](doc/preview/preview1.webp)
![SFTP 文件管理](doc/preview/preview2.webp)
![批量命令执行](doc/preview/preview3.webp)

## 快速开始

### 方式一：部署到服务器（跳板机）

```bash
curl -fsSL -o ./install.sh https://github.com/hochenggang/managi-v3/releases/latest/download/install.sh
chmod +x ./install.sh
sudo ./install.sh
```

按提示完成安装（会要求设置访问密码），之后访问 `http://<服务器IP>:18001`。
支持 Alpine / Debian / Ubuntu（amd64 / arm64）。

安装脚本按 Release 附带的 `.sha256` 自动校验产物，也可自行指定指纹（`sudo -E` 才保留环境变量）：

```bash
MANAGI_SHA256=<二进制文件的 sha256> sudo -E ./install.sh
```

### 方式二：Windows 桌面客户端

[下载 managi-desktop-windows-amd64.exe](https://github.com/hochenggang/managi-v3/releases/latest/download/managi-desktop-windows-amd64.exe)（约 9MB，内嵌服务、前端与托盘）。

双击即用：自动以 `http://127.0.0.1:18001` 启动并打开浏览器；端口被占用时自动顺延，失败原因显示在托盘图标上。
它与服务器形态是同一个二进制，桌面版只是带 `-tray` 启动。

## 特性

- **SSH 终端**：xterm.js，多会话、窗口自适应；断线重连恢复会话（工作目录与运行中进程保留）
- **SFTP 文件管理**：浏览、上传（断点续传）、下载（大文件自动走 HTTP Range）、删除
- **批量命令**：多节点并发执行，输出实时汇总
- **安全**：可选 Basic Auth、TLS、主机密钥校验（TOFU 落盘 / 严格 known_hosts）、登录失败限流
- **多平台**：Go 静态二进制（Linux / macOS / Windows，glibc / musl）

## 配置

只开放 8 个环境变量（配置文件 `/etc/managi/config.env`，安装时自动生成）：

| 变量 | 默认 | 说明 |
|------|------|------|
| `MANAGI_HOST` | `0.0.0.0` | 监听地址 |
| `MANAGI_PORT` | `18001` | 监听端口 |
| `MANAGI_AUTH` | 空 | Basic Auth 凭据 `user:pass`，**非空即启用鉴权**；留空不鉴权（非回环监听时启动告警）；空口令（`user:`）拒绝启动 |
| `MANAGI_INDEX_HTML` | `index.html` | 前端单页路径 |
| `MANAGI_KNOWN_HOSTS` | 空 | 指向 OpenSSH `known_hosts` 即启用严格主机密钥校验；留空 = TOFU，信任落盘 `~/.managi/known_hosts` |
| `MANAGI_TLS_CERT` / `MANAGI_TLS_KEY` | 空 | 证书与私钥路径（PEM），**成对**设置启用 HTTPS（`wss://`）；只配一半拒绝启动 |
| `MANAGI_TRUSTED_PROXIES` | 空 | 可信反向代理网段（CIDR 或裸 IP），仅这些来源采信 `X-Forwarded-For` |

另有 `-host` / `-port` 命令行参数供手工调试。超时、心跳、连接池容量与分片大小等调优参数不开放配置——取舍依据见 [design-v5.md §8.4](design-v5.md)；反代与 TLS 部署细节见 §8.2。

## 说明

- **会话复用**：终端标签关闭或断线后，后端 shell 保留 60 秒；期间重连可回到同一会话。
- **主机密钥**：TOFU 信任库落盘 `~/.managi/known_hosts`，跨重启有效；路径不可写时降级为进程内记录并告警，文件损坏时拒绝所有连接。完全掌控信任列表请用 `MANAGI_KNOWN_HOSTS`。
- **凭据存储**：节点配置（含密码 / 私钥）明文存于浏览器 localStorage——本工具的定位是本机 / 内网管理端；更高安全性请使用 SSH 密钥认证。

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
- **CI/CD**：GitHub Actions（构建、测试、发布）

## 开发

```bash
cd backend && go test ./...            # 后端测试
cd frontend && npm ci && npm run dev   # 前端开发
make build-desktop                     # Windows 桌面端（交叉编译）
```

## License

MIT
