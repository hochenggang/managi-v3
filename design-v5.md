# Managi 设计目标文档（v0.5.*）

> 本文档定义 Managi 的架构目标、技术选型、模块规范与实施路线。
> 一~三章的"v2 → v3"对比是历史脉络；四章往后（模块、协议、部署、CI）按当前实现写实。

**当前状态一览（M0~M4 收敛后）**

| 维度 | 现状 |
|------|------|
| 交付形态 | 一个 `managi` 二进制两种形态：默认 HTTP 服务器；`-tags desktop` + `-tray` 为 Windows 托盘 |
| 协议 | 单一 `/ws`：控制面 JSON `{type,data,seq}`，数据面二进制 `[chan u32][flags u16][raw]` |
| HTTP 端点 | `/`（前端单页）、`/health`、`/ws`、`/api/ssh/batch`、`/api/sftp/download`；未注册的 `/api/*` 回 JSON 404 |
| 环境变量 | 8 个：`MANAGI_HOST`/`PORT`/`AUTH`/`INDEX_HTML`/`KNOWN_HOSTS`/`TLS_CERT`/`TLS_KEY`/`TRUSTED_PROXIES`，其余参数为编译期默认值 |
| 认证 | 无状态中转：凭据只来自请求，不落盘；BasicAuth 由 `MANAGI_AUTH=user:pass` 自身开关 |
| 兼容 | 前后端同仓同步迭代，**不承诺**与 v2 协议兼容 |


---

## 一、项目愿景与 v3 目标

> 本章与 1.2/1.3 是 v2 → v3 立项时的目标陈述，保留作历史；其中"② 本地客户端 = Tauri"未落地，
> 实际形态见 §2.3 与第七章，指标达成情况见 §10.1。

### 1.1 愿景

Managi v3 在保持"无代理、浏览器即终端"核心定位的前提下，演进为**统一工程结构、跨平台可分发、容器化可部署、高性能低占用**的运维管理平台。

### 1.2 五大目标矩阵

| 目标 | 现状(v2) | v3 目标 |
|------|----------|---------|
| **① 架构整合** | 前后端分离两个独立目录/仓库，无统一构建 | 统一 monorepo，单一 GitHub Actions 实现 CI/CD 全流程 |
| **② 本地客户端** | 仅 Windows，Nuitka 打包，体积 50MB+ | Tauri 跨平台(Win/macOS/Linux)，安装包 <15MB |
| **③ 部署方案** | 单 Dockerfile，构建时 git clone，无 compose | 多阶段 Docker + docker-compose + install.sh 三系跳板机一键部署 |
| **④ 后端迁移** | Python/FastAPI，线程池卸载阻塞 IO | Golang 原生并发，单静态二进制 |
| **⑤ 前端重构** | Vue3，5 项关键缺陷 | Vue3 保留 CSS 重构，5 项缺陷全修复 |

### 1.3 量化指标

| 指标 | 基线(v2 Nuitka onefile) | v3 目标 | 降幅 |
|------|------------------------|---------|------|
| 桌面客户端体积 | ~50MB | <15MB | ↓70% |
| 后端二进制体积 | ~50MB(含 Python 运行时) | <20MB | ↓60% |
| Docker 镜像体积 | ~120MB(python:3.9-alpine + 依赖) | <25MB | ↓80% |
| 运行时内存占用 | ~80MB | <60MB | ↓25% |
| 空闲 CPU 占用 | 基线值 | 较基线↓20% | ↓20% |
| 冷启动时间 | ~2s | <500ms | ↓75% |

---

## 二、技术选型

### 2.1 后端：Golang

| 维度 | 选型理由 |
|------|----------|
| **SSH 生态** | `golang.org/x/crypto/ssh` 官方扩展库 + `github.com/pkg/sftp` 成熟稳定，覆盖密码/私钥认证、Shell、SFTP 全场景 |
| **并发模型** | goroutine + channel 天然适配"WebSocket 双向流 + SSH Shell 透传"桥接模式，无需线程池卸载 |
| **二进制产物** | 单静态二进制，无运行时依赖，交叉编译一条命令（`GOOS=linux GOARCH=amd64 go build`） |
| **部署契合** | 静态二进制契合 install.sh 一键部署与 Alpine 跳板机场景；契合 Docker 多阶段构建（builder 编译 + scratch/alpine 运行） |
| **桌面集成** | Go 二进制既做服务器，也在 `-tags desktop` 时自带托盘与内嵌前端（原计划做 Tauri sidecar，见 §2.3） |
| **库选型** | Web：`net/http` + `gorilla/websocket`（不引 gin）；配置：`os.Getenv` + `Normalize()`（不引 viper/envconfig）；日志：`slog`(标准库)；SSH/SFTP：`x/crypto/ssh` + `pkg/sftp` |

> 不选 Rust 的原因：SSH 库(russh)成熟度不及 Go；编译慢影响 CI；学习曲线陡。Rust 仅用于 Tauri 客户端外壳（Tauri 原生）。

### 2.2 前端：保留 Vue3 + 重构

| 维度 | 选型理由 |
|------|----------|
| **CSS 保留** | 现有视觉设计以 `base.css`(CSS 变量主题) + `main.css`(全局样式) + 组件 scoped 样式承载，Vue3 scoped style 可字节级复用 |
| **重构范围** | 抽离 composables 层（WebSocket/SFTP/Terminal 逻辑）、新增 protocol 层（消息类型）、修复 5 缺陷，组件结构重组 |
| **构建** | 保留 `vite-plugin-singlefile` 单 HTML 产物，便于 Tauri 内嵌与后端静态服务 |
| **新增依赖** | 无重型依赖新增；可引入 `xterm-addon-fit`（已有）、`xterm-addon-search`（辅助换行） |

### 2.3 桌面客户端：Go systray（实际实现）

| 维度 | 选型理由 |
|------|----------|
| **体积** | 单 Go 二进制内嵌 HTTP 服务与前端 HTML，约 9MB（vs Tauri 需 Rust 工具链 + 系统 WebView 依赖） |
| **跨平台** | 当前仅 Windows（`//go:build windows && desktop`），macOS/Linux 可后续扩展 |
| **集成** | `getlantern/systray` 管理托盘图标与菜单，`pkg/browser` 打开默认浏览器，Go 服务监听 `127.0.0.1:18001` |
| **简化** | 省去 Tauri sidecar 通信与 Rust 构建依赖；托盘与服务器是**同一个 `cmd/managi` main**，只靠 `-tags desktop` 决定是否内嵌资源、`-tray` 决定是否进托盘 |

> 注：原设计文档规划 Tauri，实际实现选择更轻量的 Go systray 方案。Tauri 章节保留为历史参考。

### 2.4 容器与部署

| 维度 | 选型理由 |
|------|----------|
| **Docker 基础镜像** | `node:22-alpine` 构建前端 + `golang:1.25-alpine` 构建后端 + `alpine:3.21` 运行 |
| **多阶段构建** | builder 阶段编译 Go，运行阶段仅含二进制 + 前端 HTML，镜像 <25MB |
| **install.sh** | 纯 Shell，通过 `/etc/os-release` 判定 Alpine/Debian/Ubuntu，对应 apk/apt 安装依赖 |

---

## 三、统一项目架构（monorepo）

### 3.1 目录结构

```
managi-v3/
├── backend/                    # Go 后端（单 main，两种形态）
│   ├── cmd/managi/main.go      # 入口：默认服务器，-tray 走托盘
│   ├── internal/
│   │   ├── config/             # 8 个环境变量 + Normalize 兜底 + Validate 拒启动
│   │   ├── server/             # 路由/中间件/超时的统一装配
│   │   ├── handler/            # HTTP + WS 端点
│   │   │   ├── handler.go      # 路由表
│   │   │   ├── ssh.go          # /api/ssh/batch
│   │   │   ├── download.go     # /api/sftp/download（Range 续传）
│   │   │   ├── ws.go           # /ws 单连接：控制面分发 + 数据面复用
│   │   │   ├── wsmsg.go        # 控制面消息类型
│   │   │   ├── chan_pty.go     # 终端通道
│   │   │   ├── chan_sftp.go    # SFTP 通道（ls/mkdir/rm/upload/download）
│   │   │   ├── live_session.go # 会话复用与空闲回收
│   │   │   ├── auth.go         # BasicAuth + clientIP
│   │   │   ├── accesslog.go    # 访问日志（归因 IP，可配可信代理）
│   │   │   ├── headers.go      # 安全响应头
│   │   │   ├── input_queue.go  # 每通道有界输入队列（读循环只入队）
│   │   │   └── stream.go       # 数据面分片收发组合子
│   │   ├── wire/               # 协议编解码：envelope + 二进制帧头
│   │   ├── sshpool/            # SSH 连接池(复用/保活/淘汰)
│   │   ├── sftp/ops.go         # SFTP 操作封装
│   │   ├── terminal/           # 终端会话(Shell 透传/resize/背压)
│   │   ├── ring/               # 终端输出回放的环形缓冲
│   │   ├── keylock/            # per-key 互斥（连接创建串行化）
│   │   ├── model/              # 数据模型
│   │   ├── desktop/            # 托盘形态（-tags desktop，内嵌 HTML/图标）
│   │   └── testutil/           # 测试桩（mock SSH/SFTP）
│   └── go.mod
├── frontend/                   # Vue3 单页（vite-plugin-singlefile）
│   ├── src/
│   │   ├── assets/             # 原样迁移 base.css/main.css
│   │   ├── components/         # 组件(保留 scoped CSS)
│   │   ├── composables/        # useWSHub/useSFTP/useTerminal/useRetry/useConfirm
│   │   ├── protocol/           # types.ts / ws.ts / frames.ts（与控制面对齐）
│   │   ├── stores/             # Pinia
│   │   ├── views/              # CmdsView/XtremView
│   │   ├── locales/            # i18n 文案
│   │   └── api.ts              # HTTP 客户端(含重试)
│   ├── vite.config.ts
│   └── package.json
├── desktop/                    # Windows 托盘：icon.ico + README（产物不入库）
├── deploy/
│   ├── Dockerfile              # 多阶段(引用 backend+frontend)
│   ├── docker-compose.yml
│   ├── .env.example
│   └── install.sh              # 三系跳板机部署
├── .github/workflows/
│   ├── ci.yml                  # push/PR: lint+test+build
│   └── release.yml             # tag: 多平台 Release
├── Makefile                    # dev/build/test/desktop/docker 统一入口
├── README.md
└── design-v5.md                # 本文档
```

### 3.2 模块关系图

```
┌─────────────────────────────────────────────────────────────┐
│  浏览器（Web 模式直连远端服务 / 桌面模式连本机 127.0.0.1）    │
│  frontend/dist/index.html：HTTP + 单条 /ws                    │
└──────────────────────────┬──────────────────────────────────┘
                           │
┌──────────────────────────▼──────────────────────────────────┐
│  managi 二进制（cmd/managi 同一份 main）                      │
│  ├─ 服务器形态：server.New → handler → sshpool → 远程节点     │
│  └─ 托盘形态（-tags desktop + -tray）：                      │
│     internal/desktop 内嵌 index.html/icon.ico，systray 菜单   │
│     仍复用 server.New，只强制 Host=127.0.0.1 与端口顺延       │
└──────────────────────────┬──────────────────────────────────┘
                           │ Docker/容器 / install.sh+systemd
                           ▼
┌─────────────────────────────────────────────────────────────┐
│  部署层 (deploy/)                                            │
│  Dockerfile ─► docker-compose  /  install.sh ─► systemd/OpenRC│
└─────────────────────────────────────────────────────────────┘
```

### 3.3 运行形态

| 形态 | 组合 | 启动方式 |
|------|------|----------|
| **Web 服务** | Go 后端(服务前端 HTML) | `./managi -port 18001` 或 Docker |
| **桌面客户端** | 同一二进制 + 内嵌前端 + systray | 双击 `managi-desktop-windows-amd64.exe`（默认 `-tray`） |
| **跳板机** | Go 二进制 + systemd | `install.sh` 一键部署 |

---

## 四、后端 Golang 迁移设计

### 4.1 模块划分

| 模块 | 职责 | 对应 v2 |
|------|------|---------|
| `cmd/managi/main.go` | 解析 flag、加载配置、按形态分派（服务器 / 托盘） | `app.py` |
| `internal/config` | 8 个环境变量加载 + `Normalize()` 兜底非法值 + `Validate()` 拒绝启动级错误 | `setting.py` |
| `internal/server` | 路由与中间件装配、优雅关闭（服务器与托盘共用） | `app.py` 装配部分 |
| `internal/sshpool` | SSH 连接池(复用/保活/淘汰) | `ssh_pool.py` |
| `internal/handler` | HTTP + WebSocket 端点 | `routers.py` |
| `internal/wire` | 协议编解码：控制面 envelope + 数据面帧头，无业务状态 | — |
| `internal/sftp` | SFTP 操作(上传/下载/列目录/删除/mkdir) | `sftp_client.py` |
| `internal/terminal` | 终端会话(Shell 透传/resize/输入背压) | `routers.py` WS 部分 |
| `internal/ring` | 会话复用时的输出回放缓冲 | — |
| `internal/keylock` | per-key 互斥，串行化同键连接创建 | — |
| `internal/model` | 数据结构(Node/FileItem 等) | `models.py` |
| `internal/desktop` | 托盘形态：内嵌前端与图标、端口顺延、托盘报错可见 | Nuitka 打包 |

### 4.2 SSH 连接池设计（修正 v2 缺陷）

**v2 问题**：`release_connection` 实为关闭，命令执行路径不复用连接，仅 SFTP 会话复用。

**v3 修正**：

```
连接池(key = host:port:username:凭据指纹)
├── Get(node)
│   ├── 先校验 node 字段(host/port/username/认证材料齐全)，再取键
│   ├── 存在且 transport 活跃 → 复用,引用计数+1
│   └── 不存在/失效 → 新建,入池,引用计数=1
├── Release(conn)
│   ├── 按连接对象身份归还(避免死连接的迟到 Release 误减新连接的引用计数)
│   └── 引用计数=0 → 不立即关闭,标记空闲时间戳
└── 后台 cleaner(定时)
    ├── 清理空闲超时(固定 120s)的连接
    └── 池满(固定 20 条)时淘汰最旧空闲连接
```

- **键含凭据指纹**：同一 `host:port:username` 换口令/换认证方式后不会复用旧连接，
  也不会把凭据本身写进键（指纹为 sha256 前 4 字节）。
- **命令执行也复用**：Get → 执行 → Release（减引用，不关闭），下次同节点命令直接复用。
- **保活**：每 30s 发送 keepalive packet；探测失败即从池中剔除，不滞留到 cleanIdle。
- **并发安全**：per-key `sync.Mutex` 串行化连接创建，连接本身 goroutine 安全。
- **主机密钥**：默认文件化 TOFU（首次记录公钥，之后不符即拒），信任库为
  `~/.managi/known_hosts`（OpenSSH known_hosts 格式），跨进程重启有效；库文件损坏即拒绝所有连接（坏行会让信任锚静默消失，
  下次连接被当成「首次」而信任任意密钥），库不可写（如容器只读根文件系统）才降级为进程内记录并告警。
  设 `MANAGI_KNOWN_HOSTS` 走 OpenSSH known_hosts 严格校验，文件无法解析时拒绝所有连接而不退回 TOFU。

### 4.3 并发模型

| 场景 | 模型 |
|------|------|
| 批量命令 | 入口闸：节点数 ≤100、总时限 15min；`errgroup.Group` 并发执行（SetLimit 10）、单路输出 4MB 截断；单节点失败不取消其它节点 |
| 终端输出 | 一路会话一个 `outputLoop` goroutine：读 shell stdout → 追加 scrollback → 转发给当前挂载的 outSink |
| 终端输入 | 每路通道一个输入协程消费有界队列后写 PTY stdin：`/ws` 读协程只做入队与记账，远端不读时绝不堵住整条连接；队列溢出丢帧并以补偿 ack + 提示保通道存活 |
| 会话复用 | `sessionManager` 按 session_id 索引；标签/连接断开后保留 60s，重开同一 id 复用同一 shell（CWD/进程/scrollback 都在）。命中已有会话时核对节点连接键：id 与节点身份失配即拒绝（会话不跨节点复用） |
| SFTP 上传 | 每路通道一个输入协程按序把帧写入 `.part`（一通道一写入者，无并发写冲突）；积压溢出中止整条通道（error + 补偿 ack），已落盘字节留在 `.part` 供续传 |
| SFTP 下载 | `pump` goroutine 逐块读 + 单帧写出，`ctx` 取消时关掉读侧解阻塞；EOF 补 `FlagEnd`，读错**不补**（前端据此判失败） |
| WS 心跳 | 服务端 `time.Ticker` 定时 Ping（30s），`SetReadDeadline`（90s）在 Pong 回调里重置 |
| 写并发 | 一条连接一把写锁（`wsConn.mu`）保护所有写，含控制帧；每次写设 30s 写截止（页面冻结/半断网时 `WriteMessage` 会一直堵在内核发送缓冲区上，没有写超时会拖住整条会话）；锁顺序固定 `wc.mu → ls.mu`，锁内不做网络 I/O |

### 4.4 WebSocket 协议与 HTTP 端点

一条 `/ws` 连接服务整个页面：**每个终端标签占一路 PTY 通道，每个文件管理标签占一路 SFTP 通道**，
通道号由服务端分配（从 1 起，0 保留给"与具体通道无关"的控制帧）。

| 端点 | 方法 | 协议 | 说明 |
|------|------|------|------|
| `/api/ssh/batch` | POST | `{nodes, cmds}` | 批量命令执行（errgroup 并发），回 `[]CmdsTestResult`；入口闸：节点 ≤100、总时限 15min、单路输出 4MB 截断 |
| `/api/sftp/download` | POST | `{node, path}` + `Range` 头 | HTTP Range 下载（断点续传），凭据走 body 不落 URL |
| `/ws` | WS | 文本帧=控制面，二进制帧=数据面 | 终端 + SFTP 全部走这一条连接 |
| `/health` | GET | — | 探活（不经鉴权，供桌面端开浏览器与容器 HEALTHCHECK） |
| `/` | GET | — | 前端单页 |
| `/api/*` | 任意 | — | 未注册路径回 JSON 404（不回落首页 HTML） |

**`/api/*` 写端点的跨站闸**：统一经 `apiPost` 包装——仅 POST；`Sec-Fetch-Site` 非 `same-origin`/`none`
（即 cross-site/same-site，空值=非浏览器客户端）回 403；请求体强制 `application/json`（跨站表单发不出该
类型）。浏览器自动携带 BasicAuth 的场景下，跨站页面无法再借道发起写操作（CSRF）。

**控制面**（文本帧，`{type, data, seq}`）：

```
open      {kind:"pty"|"sftp", node, session_id?, cols?, rows?}
          → {kind, chan, home?, reattached?, chunk_size?, window?}
          // home：该账号的初始目录（SFTP）；reattached：命中 60s 会话复用
          // chunk_size：PTY 的入站单帧上限（SFTP 的切片大小随 upload 响应下发）
          // window：输入窗口（字节），客户端发送方向的节流依据
close     {chan}                         → 关一路通道
resize    {chan, cols, rows}             // seq=0，不等回复
ping / pong                              → 心跳，服务端据此重置读超时
error     {chan, message}                // chan=0 表示与通道无关
ack       {chan, bytes}                  // seq=0，已消费或已作废的累计输入字节
ls / mkdir / rm / upload / download  {chan, path, filename, size, offset}
upload_end {chan, size}                  // 服务端主动推，seq=0
```

**数据面**（二进制帧，`internal/wire`）：`[chan:4 BE][flags:2 BE][原始字节]`，`HeaderLen=6`。
PTY 输出、粘贴输入、文件分片都在此处，不经 `string→JSON`；`FlagEnd` 表示该通道这路流到此结束。

**输入流控**（发送方向背压）：服务端在 open 响应里给出 `window`，客户端"已发 − 已确认"不超过它才继续
发送（`useWSHub.waitAck`）；服务端每消费完一帧即回 `ack`（累计值，含丢弃补偿），重连换通道即重置账本。
服务端输入队列预算 = 窗口 + 一片余量：合规客户端永远填不满，溢出只是防线的防线——PTY 溢出丢帧并以补偿
ack 催齐账本（通道存活），SFTP 溢出中止整条通道（error + 补偿 ack，`.part` 保留可续传）。

**seq 关联**：请求 `seq>0` ⇒ 同 type、同 seq 回且只回一次；服务端主动推的消息 `seq=0`（序列化时省略）。
前端按 seq 匹配当前请求，seq 不符或迟到的帧一律丢弃。协议换代后**不再兼容 v2**：
v2 的 `/ws/ssh` + `/ws/sftp` 首包 `{type:"login",…}` 已删除，前后端同仓同步发布。

### 4.5 性能对比基线

| 指标 | v2(Python) | v3(Go) 预期 |
|------|------------|-------------|
| 100 节点并发命令 | ~8s(线程池瓶颈) | <3s(goroutine) |
| 单连接内存 | ~2MB(Paramiko) | ~200KB(ssh 库) |
| 二进制体积 | ~50MB | <20MB |
| 冷启动 | ~2s | <500ms |

---

## 五、前端重构设计

### 5.1 CSS 保留策略

| 文件 | 策略 |
|------|------|
| `assets/base.css` | **字节级复制**（CSS 变量主题 + 全局 reset） |
| `assets/main.css` | **字节级复制**（全局元素样式 + 工具类） |
| 各组件 `<style scoped>` | 随组件迁移，**样式规则原样保留**，仅调整组件结构与逻辑 |
| xterm 主题色 | 保留 solarized dark 配色（`#002b36` bg / `#cce4f5` fg） |

### 5.2 新增架构层

#### 5.2.1 `protocol/` 消息类型层

集中定义所有 WS 消息类型，取代 v2 散落在组件内的隐式约定：

```
protocol/
├── types.ts    # Node / ApiNode / OldApiNode / BatchCmdRequest / CmdsTestResult / FileItem
├── ws.ts       # 控制面：{type, data, seq} 与每个动词的负载（对齐 handler/wsmsg.go）
├── frames.ts   # 数据面：[chan:u32][flags:u16][raw]，HEADER_LEN=6（对齐 internal/wire）
└── sftp.ts     # SFTP 控制帧 data 负载（对齐 handler/chan_sftp.go 的请求/响应）
```

#### 5.2.2 `composables/` 逻辑复用层

| composable | 职责 | 修复缺陷 |
|------------|------|----------|
| `useWSHub` | 全站**唯一**一条 `/ws`：连接、重连、心跳、按 chan 分发到各标签 | 心跳失效、网络重试、多标签各建一条连接的浪费 |
| `useTerminal` | xterm 实例 + 尺寸同步 + 输入输出（经数据面） | 终端换行 |
| `useSFTP` | 一路文件管理：列目录/上传/下载/续传（控制面 + 数据面） | 上传/下载中断 |
| `useRetry` | 通用指数退避重试（HTTP 层用） | 网络响应丢失 |
| `useConfirm` / `useSidebar` / `flyToTab` | 交互辅助（确认框、侧栏、标签跳转动画） | — |

### 5.3 五大缺陷修复方案

> 详见第六章。

---

## 六、五大缺陷修复方案

### 6.1 SSH 命令过长导致终端换行错乱

**问题分析**：
- v2 后端 `invoke_shell` 固定 `width=80, height=24`，前端窗口实际宽度未同步。
- v2 resize 通过解析 `\x1b[8;<rows>;<cols>t` 字符串 split，格式不匹配时静默忽略。
- 长命令超出 cols 时，Shell 不自动折行，导致光标位置错乱。

**修复方案**：
1. **前端**：`FitAddon.fit()` 后立即通过结构化消息（非转义序列）发送 `{type:"resize", cols, rows}`。
2. **后端**：`/ws` 收到 resize 消息调 `channel.WindowChange(cols, rows)`（Go ssh 库原生支持）。
3. **初始化**：连接建立后前端首次 fit 即发送 resize，避免 80×24 默认值。
4. **长命令**：依赖 Shell 自身折行（cols 正确后自然折行），无需额外处理。

**验收**：窗口缩放后长命令正确折行，光标位置与输出对齐。

### 6.2 网络响应丢失（重试机制）

**问题分析**：
- v2 HTTP 请求失败直接抛异常，无重试。
- v2 WS 断线仅简单重连 3 次，固定 2s 间隔。

**实现方案**：
1. **HTTP 层**（`api.ts` + `useRetry.ts`）：`fetchWithRetry` 指数退避重试 3 次（1s/2s/4s，上限 30s）。
   只对 **fetch 抛错**（网络不可达、`AbortController` 超时）重试；HTTP 错误码在重试包装**之外**才抛，
   因此 4xx/5xx 不重试——响应已经到达，重试 `/api/ssh/batch` 等于把命令再执行一遍。
2. **超时**：每个请求 `AbortController` 30s 超时，且 signal 在每次重试的回调内新建（首次 abort 后复用会让后续重试立刻失败）。
3. **WS 层**（`useWSHub`）：断线指数退避重连，上限 10 次（1s 起翻倍、封顶 16s，加随机抖动避免同时断线的标签页一起重连），超限置 `failed` 状态并通知用户；重连成功后重建各路通道。
4. **幂等性**：后端是无状态中转，不做 `request_id` 去重；写语义由"只重试网络层失败"保证。

**验收**：模拟网络抖动，请求自动重试成功；断线在退避上限内自动恢复。

### 6.3 心跳连接失效

**问题分析**：
- v2 前端每 30s 发 `\x00`，但 `\x00` 作为终端输入会进入 Shell，可能引发异常。
- v2 后端无心跳超时检测，连接假死不被发现。
- 中间网络设备空闲超时断开 WS，v2 无感知。

**实现方案**：
1. **服务端主动 Ping**：`startPingLoop` 每 30s 发原生 WS Ping 控制帧。浏览器不能自己发 Ping 帧
   （JS API 没有这个口子），所以心跳必须由服务端发起、浏览器内核自动回 Pong——
   用业务 `{type:"ping"}` 也能做，但后台标签页的 JS 定时器会被节流到分钟级，心跳就断了。
2. **服务端读超时**：`SetReadDeadline(90s)`，Pong 回调里重置；超时未收到任何入站帧即关闭连接，触发前端重连。
3. **业务 ping/pong**：保留控制面 `{type:"ping"} → {type:"pong"}`（`seq=0` 不等回复的语义），用于前端主动探测。
4. **保活分离**：SSH Shell 保活由后端 keepalive packet（30s）处理，与 WS 心跳解耦。
5. **终端输入不再用 `\x00`**：移除 v2 的空字节心跳。

**验收**：空闲 10 分钟连接不断；拔网线 90s 内服务端检测并清理。

### 6.4 文件上传中断（断点续传）

**问题分析**：
- v2 上传为单帧整体发送，文件大或网络抖动即失败，无续传。
- v2 上传无分片、无进度保存。

**实现方案**（控制面开路径，字节走数据面）：

```
开上传:  {type:"upload", data:{chan, path, filename, size, mtime}, seq}
         ← {type:"upload", data:{chan, offset, chunk_size}, seq}
             // offset = 同身份残片 <filename>.<size>-<mtime>.part 的大小（续传起点）
             // path 是目标目录，空即报错回帧
             // chunk_size 由服务端下发（config 里的固定值），客户端按它切帧
分片:    二进制帧 [chan:4][flags:2][payload]      // 无 seq/无 index：TCP 有序 + 一通道一写入者
落定:    二进制帧 [chan][FlagEnd]                 // 载荷可为空，只是"结束了"这个信号
         ← {type:"upload_end", data:{chan, size}}  // 服务端主动推，seq=0
出错:    {type:"error", data:{chan, message}}
流控:    每片落盘后 ← {type:"ack", data:{chan, bytes}}（累计值）；客户端"已发 − 已确认 ≤ window"才继续
```

**服务端**：
- `upload` 在远端建/续 `<filename>.<size>-<mtime>.part`：身份（大小 + 修改时间）写进残片文件名，
  只有身份吻合的残片才作为续传起点；失配残片与旧版无身份的 `.part` 一并清掉重传——
  同名换内容后若续到旧残片上，新旧字节会拼成静默损坏的文件。
  身份段两侧必须都是纯数字才认（用户自建的同前缀文件不碰）；负 mtime 归零以保持可解析。
- 该通道后续的数据帧按序直接写入 `.part`；带 `FlagEnd` 的帧触发 `rename` 为目标文件并推 `upload_end`。
- 同通道收到第二个 `upload` 视为前一路已被客户端放弃：中止旧写入者（已落盘字节留在 `.part`）并接管，
  而不是拒绝——否则一次中断会让这条连接余生都传不了文件。
- 写入失败即 `Abort` 并回 error：`.part` 保留，重开通道即可续传。
- 输入队列溢出（只会由不合规客户端触发）中止整条通道：error + 补偿 ack，已落盘字节留在 `.part`。
- 进度与 `.part` 状态为会话级内存，不落盘（无状态中转的定位）。

**前端**：
- 请求携带 `size` 与 `mtime`（取 `file.lastModified`）作为续传身份；服务端据此决定从旧残片续传还是重传。
- 分片大小取 `upload` 响应的 `chunk_size`，`File.slice(pos, pos+chunk)`；该值非正即中止（步长为 0 会让循环永不结束）。
- 每片发送前 `await channel.waitAck(bytes)` 按输入窗口节流；等待期间通道被判死（error 已到）即以该错误收尾，不对死通道续发。
- 响应按 `seq` 严格匹配当前请求；`seq=0` 的帧（如 `upload_end`）是服务端主动推，不参与匹配。
- 进度：`min(pos + 已写字节, file.size) / file.size`，封顶 100%。

**验收**：上传中断网络，恢复后从中断点继续；刷新页面后可恢复（依赖 localStorage + 服务端 `.part`）。

### 6.5 文件下载中断（断点续传）

**问题分析**：
- v2 下载为 WS 分块流，前端全量累积到内存，中断即丢失全部进度。
- 无 Range 支持，无法续传。

**修复方案**：

**改为 HTTP Range 下载**（SFTP 下载经后端中转）：
```
POST /api/sftp/download          body: {node, path}   // 凭据走 body，不落 URL/访问日志
Header: Range: bytes=<offset>-   // 续传偏移（闭区间/后缀区间同样支持）
← 206 Partial Content
   Content-Range: bytes <start>-<end>/<total>
   Content-Length / Last-Modified
   Body: 流式分块
← 416 Requested Range Not Satisfiable   // 起点越过文件末尾，Content-Range: bytes */<total>
← 200 OK（整文件）                       // 无 Range 或 Range 写法无法解析
```

**服务端**：
- 后端打开 SFTP 文件句柄，Range 语义**整份交给 `http.ServeContent`**：闭区间、开区间、后缀区间、
  416 + `Content-Range: bytes */total`、`Content-Length`、`Last-Modified` 一次到位。
  自己解析只会写不全，然后各处客户端踩坑（起点越界发 200/206 空体，就会让续传端停在错误偏移再也不敢传）。
- `ServeContent` 在 POST 上同样按 Range 处理，故凭据可以留在请求体而不必退回 GET。
- 流式写出，不载入内存。

**前端**：
- 使用 `fetch` + `ReadableStream` 读取，边读边写入 `FileSystemWritableFileStream`（File System Access API）或累积到 Blob。
- 断点记录：`localStorage` 存 `{path, offset, total}`，中断后下次从 offset 续传。
- 进度：`received / total` 实时更新。

**降级**：不支持 File System Access API 的浏览器，回退到内存累积 + Blob 下载（同 v2）。

**验收**：下载中断后恢复从断点继续；大文件(>1GB)下载不 OOM。

---

## 七、桌面客户端设计

> **注**：实现选择更轻量的 Go systray 方案（见 §2.3），Tauri 从未落地；本章末尾保留其设计要点作为历史参考。

### 7.1 实际实现：同一二进制 + 托盘形态

```
┌──────────────────────────────────────────────┐
│  managi-desktop-windows-amd64.exe            │
│  = cmd/managi 同一份 main，编译带 -tags desktop │
│  ├─ -tray（该构建默认为 true）                │
│  │   ├─ internal/desktop：systray 图标与菜单  │
│  │   ├─ go:embed index.html / icon.ico        │
│  │   ├─ server.New(...) 强制 Host=127.0.0.1   │
│  │   ├─ 端口顺延 18001→18005，就绪后开浏览器  │
│  │   └─ 启动/运行期失败写进托盘 tooltip 与菜单 │
│  └─ -tray=false：退回普通服务器形态           │
└──────────────────────────────────────────────┘
```

- 产物约 9MB，无需 Rust 工具链或 sidecar 通信；服务装配与服务器形态**共用 `server.New`**，
  路由/鉴权/安全头/超时不可能在两处漂移。
- 只监听回环：桌面端是本机管理台，不对外提供服务。
- `-H=windowsgui` 链接后没有控制台，所以失败必须可见（托盘提示 + 置灰菜单项），而不是 `os.Exit` 一闪就没。
- 构建：`make build-desktop`（先把前端产物与图标复制进 `backend/internal/desktop/`，rsrc 生成 `.syso`）。

### 7.2 原 Tauri 设计（历史参考）

```
┌─────────────────────────────────────┐
│  Tauri 应用 (Rust)                  │
│  ┌───────────────────────────────┐  │
│  │ main.rs                       │  │
│  │  ├─ spawn sidecar: managi     │  │  ← Go 二进制,监听 127.0.0.1:<port>
│  │  ├─ 等待 sidecar 健康检查通过 │  │
│  │  ├─ 打开 WebView → localhost  │  │
│  │  └─ 系统托盘(打开/退出)       │  │
│  └───────────────────────────────┘  │
│  ┌───────────────────────────────┐  │
│  │ WebView (Vue3 frontend)       │  │  ← 加载内嵌 HTML 或 localhost
│  └───────────────────────────────┘  │
└─────────────────────────────────────┘
```

### 7.3 原 Tauri 设计要点（历史参考，未实现）

| 项 | 原计划 | 实际落地 |
|----|--------|----------|
| 外壳 | Tauri(Rust) + WebView，`bundle.externalBin` 指 Go sidecar | Go systray，前端 HTML 直接 `go:embed` |
| 生命周期 | Rust spawn sidecar → 轮询 `/health` → 打开 WebView → SIGTERM | 同进程起 `server.New` → 轮询 `/health` → `pkg/browser` |
| 端口 | sidecar 探测 18001-18100 并写临时文件 | 18001 起顺延 5 个，实际端口用于开浏览器 |
| 跨平台 | Win(.msi/.exe)/macOS(.dmg)/Linux(.deb/.AppImage) | 仅 Windows exe；macOS/Linux 用户走 Docker 或 install.sh |
| 协议 | WebView 直连 `127.0.0.1:<port>` | 同——与 Web 模式一套协议，前端零改动 |

---

## 八、部署方案设计

### 8.1 Dockerfile（多阶段）

```dockerfile
# 阶段1: 前端构建（Node 22，与 CI 的 node-version 对齐）
FROM node:22-alpine AS frontend
WORKDIR /fe
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# 阶段2: 后端构建（Go 版本与 backend/go.mod 一致）
FROM golang:1.25-alpine AS backend
WORKDIR /be
COPY backend/go.mod backend/go.sum* ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /managi ./cmd/managi

# 阶段3: 运行
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=backend /managi /app/managi
COPY --from=frontend /fe/dist/index.html /app/index.html
WORKDIR /app
RUN chown -R nobody:nobody /app
EXPOSE 18001
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://localhost:18001/health || exit 1
USER nobody
ENTRYPOINT ["/app/managi"]
```

**目标镜像体积**：<25MB（Go 静态二进制 ~15MB + Alpine ~7MB + HTML）。

### 8.2 docker-compose.yml

| 服务 | 配置 |
|------|------|
| `managi` | 镜像 managi:v3、`network_mode: host` 或端口映射、环境变量(BasicAuth/端口)、`restart: unless-stopped`、healthcheck、volume 挂载（可选配置） |

**凭据与端口约定**：
- `MANAGI_AUTH=user:pass` 非空即启用 BasicAuth（**凭据本身就是开关**，没有第二个布尔变量），
  由宿主环境或同目录 `.env` 注入（样板见 `deploy/.env.example`）。
  值按**第一个冒号**切分，所以密码可以含冒号；只给用户名不给密码（空口令）是非法配置：
  配置层 `Validate` 直接拒绝启动（空口令绝不构成有效凭据），compose 的 `${MANAGI_AUTH:?…}` 更早一步拦住。
  未设置 = 明确不鉴权，监听非回环地址时启动打 WARN 提醒，但不拒绝启动（反代终结鉴权的部署合法）。
- 端口只由 `MANAGI_PORT` 决定。镜像的 `CMD` 不写 `-port`：命令行 flag 优先级高于环境变量，
  写死会让用户改的 `MANAGI_PORT` 静默失效。需要追加参数时用 compose 的 `command:` 覆盖。
- 容器硬化：`read_only: true` + `tmpfs /tmp` + `no-new-privileges` + `cap_drop: ALL`。
- TOFU 信任库默认在 `$HOME/.managi/known_hosts`。容器以 nobody 运行且根文件系统只读：
  HOME 未设或路径不可写时降级为「进程内信任、重启即忘」（启动日志告警），
  要跨重启保留首次信任就设 `HOME=/data` 并挂一个可写卷（compose 里有注释样板）。

### 8.3 install.sh（三系跳板机部署）

**目标 OS**：Alpine、Debian、Ubuntu。

**流程**：
```
1. 交互菜单：安装 / 卸载 / 升级（脚本要求 TTY 与 root）
2. 检测 OS → cat /etc/os-release → ID=alpine/debian/ubuntu（其余报错退出）
3. 架构检测 → uname -m → amd64/arm64
4. 依赖检查与安装（缺失才装）：
   - Alpine: apk add --no-cache ca-certificates tzdata wget curl
   - Debian/Ubuntu: apt-get update && apt-get install -y ca-certificates tzdata wget curl
5. 下载对应平台 Go 二进制与前端 index.html（GitHub Release latest/download）
   - 可选 MANAGI_SHA256 校验；Release 附带 <file>.sha256 时自动比对
   - 落盘采用「同目录临时文件 + mv 原子 rename」，覆盖运行中的二进制不再 ETXTBSY
6. 安装前检测已有安装/配置，询问是否沿用旧配置（沿用则读 /etc/managi/config.env）
7. 交互式询问是否启用 BasicAuth；启用时必须给出口令，脚本不再写弱默认口令
8. 写入 /etc/managi/config.env（chmod 600）
9. 创建非特权服务用户 managi
10. 写服务定义：systemd /etc/systemd/system/managi.service；Alpine /etc/init.d/managi(OpenRC)
11. enable + restart，随后轮询 /health 探测就绪并输出访问地址
```

**特性**：
- 幂等：重复执行即覆盖升级；`config.env` 与用户手工改过的 service 文件不会被覆盖（升级仅在缺失时补写 unit）。
- 原子替换：先写临时文件，下载/校验通过才 rename，失败不破坏线上文件。
- 卸载：菜单项 2 停服 + 删 unit + 删二进制与前端，保留配置目录。
- 升级：菜单项 3 仅替换二进制与前端 + 重启服务，不动配置。

### 8.4 配置面：8 个环境变量，其余参数固定

M4 把配置面收敛到只剩"部署者必须回答的问题"，其余都是协议调优参数而非用户选项；
R1 审查后补回 TLS 与可信代理 3 项（安全边界上不可省略的部署问题）：

| 环境变量 | 默认 | 语义 |
|----------|------|------|
| `MANAGI_HOST` | `0.0.0.0` | 监听地址（托盘形态强制 `127.0.0.1`） |
| `MANAGI_PORT` | `18001` | 监听端口（托盘形态被占用时顺延 5 个） |
| `MANAGI_AUTH` | 空=不启用 | BasicAuth 凭据 `user:pass`，**非空即启用**；按第一个冒号切分，缺冒号则整串当密码、用户名回退 `admin` 并告警；空口令（`user:`）拒绝启动，未启用且非回环监听时启动告警 |
| `MANAGI_INDEX_HTML` | `index.html` | 前端单页路径（相对路径会被改成绝对路径；托盘形态用内嵌内容，不读它） |
| `MANAGI_KNOWN_HOSTS` | 空=TOFU（信任库落盘 `~/.managi/known_hosts`） | 指向 OpenSSH known_hosts 即启用严格主机密钥校验；文件解析失败即拒绝所有连接，不退回 TOFU |
| `MANAGI_TLS_CERT` / `MANAGI_TLS_KEY` | 空=明文 | 证书与私钥路径，**成对**设置即启用 HTTPS（`wss://` 随之可用）；只配一半拒绝启动（静默退明文=安全降级） |
| `MANAGI_TRUSTED_PROXIES` | 空=不信任 XFF | 可信反向代理网段（逗号分隔 CIDR 或裸 IP）。仅来自这些地址的连接才采信 `X-Forwarded-For`，取最右侧非代理地址；用于反代之后的登录限流与日志归因 |

命令行 `-host` / `-port` 优先级高于环境变量（用 `flag.Visit` 判断是否显式传入，而非比较值）。

**已移除的 12 个环境变量**（原有 17 个，保留 4 个 + 新增 `MANAGI_AUTH`；后补回 TLS/代理 3 项，
`MANAGI_TRUST_PROXY` 以 `MANAGI_TRUSTED_PROXIES` 的白名单语义回归）：
`MANAGI_SSH_TIMEOUT`、`MANAGI_KEEPALIVE`、`MANAGI_SSH_IDLE_TIMEOUT`、`MANAGI_SSH_POOL_SIZE`、
`MANAGI_WS_READ_DEADLINE`、`MANAGI_WS_PING_INTERVAL`、`MANAGI_SESSION_IDLE_TIMEOUT`、
`MANAGI_SFTP_CHUNK_SIZE`、`MANAGI_SFTP_DOWNLOAD_CHUNK`、`MANAGI_BASICAUTH_ENABLED`、
`MANAGI_BASICAUTH_USERNAME`、`MANAGI_BASICAUTH_PASSWORD`。
（install.sh 仍会把沿用旧配置时读到的 `MANAGI_BASICAUTH_*` 迁移成 `MANAGI_AUTH`。）
调优值改为 `config` 包内的 `Default*` 常量（SSH 15s / 保活 30s / 空闲 120s / 池 20 / 读超时 90s /
心跳 30s / 会话保留 60s / 上行 1MiB / WS 下行 64KiB），字段仍可被装配与测试覆写，
非法值由 `Normalize()` 按字段名校正回默认并 `slog.Warn` 出声。

**为什么收**：改错的代价（周期性掉线、单帧放大到几百 MB 打爆内存、鉴权静默失效）远大于收益，
而 5 个变量已覆盖全部真实部署差异。

---

## 九、GitHub Actions 设计

### 9.1 `ci.yml`（push/PR 触发）

| Job | 触发 | 步骤 |
|-----|------|------|
| `lint-test-go` | push/PR | `go vet`、`golangci-lint`(v2.6)、`go test -race ./...` |
| `build-go` | push/PR | `CGO_ENABLED=0 GOOS=linux go build ./cmd/managi` 并检查产物体积 |
| `build-frontend` | push/PR | `npm ci`、`npm run type-check`、`npm run test`、`npm run build`、校验 dist/index.html 生成 |
| `build-desktop` | push(main) | 构建前端 → 拷贝 dist 与图标进 `backend/internal/desktop` → rsrc 生成资源 → `go build -tags desktop ./cmd/managi` |
| `deploy-scripts` | push/PR | `sh -n` 语法检查 + 运行 `deploy/install_test.sh`（verify_checksum 的 sidecar/环境变量两条路径） |
| `security-scan` | push/PR | Trivy 文件系统扫描（CRITICAL/HIGH，`exit-code: 1`，忽略上游无补丁项） |

注：Go/Node 版本以 `backend/go.mod` 与 workflow 内 `node-version` 为准；CI 无独立 docker job，
Dockerfile 的构建验证由本地或发布流水线承担。桌面构建跑在 ubuntu runner 上交叉编译，不需要 Windows runner。

### 9.2 `release.yml`（tag `v*` 触发）

| Job | 产物 |
|-----|------|
| `build-go-binaries` | matrix(linux amd64/arm64 各含 glibc 与 musl、darwin amd64/arm64、windows amd64) → 裸二进制资产 |
| `build-frontend` | dist/index.html（vite-single 单文件） |
| `build-desktop` | `managi-desktop-windows-amd64.exe`（内嵌前端与图标，需先取 build-frontend 产物） |
| `release` | 汇总上述产物 + `deploy/install.sh` 上传 GitHub Release（`generate_release_notes`），并为每个资产附 `.sha256` sidecar |

说明：桌面端走「Go + 系统托盘 + 内嵌 index.html」，不再使用 Tauri；镜像由 `deploy/Dockerfile`
与 `deploy/docker-compose.yml` 本地构建，发布流水线不推 GHCR。
install.sh 的校验顺序是 `MANAGI_SHA256` → Release 里的 `<asset>.sha256` → 跳过，不发布 sidecar 就等于永远不校验。

### 9.3 CI/CD 流程图

```
push/PR ─► ci.yml ─► lint + test + build + Trivy 验证
                          │ (main 通过)
                          ▼
tag v* ─► release.yml ─► Go 多平台二进制
                        ├─ 前端单页
                        ├─ Windows 桌面 exe（-tags desktop）
                        ├─ SHA256 sidecar
                        └─ GitHub Release 资产上传（install.sh 从这里取二进制）
```

---

## 十、性能指标与验收标准

### 10.1 量化验收表

| 指标 | 验收方法 | 目标 |
|------|----------|------|
| Go 二进制体积 | `ls -lh managi` | <20MB |
| Docker 镜像体积 | `docker images managi` | <25MB |
| 桌面 exe 体积 | 构建产物（含内嵌前端） | <15MB（实测约 9MB） || 运行时内存(空闲) | `docker stats` 或 `ps aux` | <60MB |
| 运行时内存(100 连接) | 压测脚本 | <200MB |
| 冷启动时间 | `time ./managi` 至健康检查通过 | <500ms |
| 100 节点并发命令耗时 | 批量执行 `uptime` | <3s |
| 文件上传断点续传 | 中断后恢复 | 从断点继续 |
| 文件下载断点续传 | 中断后恢复 | 从断点继续 |
| 终端换行 | 缩放窗口 + 长命令 | 正确折行 |
| 心跳保活 | 空闲 10 分钟 | 连接不断 |

### 10.2 兼容性验收

| 项 | 要求 |
|----|------|
| WS 协议 | 前后端**同仓同版本发布**，不承诺与 v2 首包认证协议兼容（`/ws/ssh`、`/ws/sftp` 已删除） |
| 浏览器 | Chrome/Edge/Firefox 最近两版（下载落盘需 File System Access API，不支持则回退 Blob） |
| OS | Windows 10+/macOS 11+/Ubuntu 20.04+/Alpine 3.19+ |
| 跳板机 | install.sh 在 Alpine/Debian/Ubuntu 干净环境成功 |
| 凭据 | 后端全程无状态：凭据只来自请求，不落盘、不写日志、不进 URL |

---

## 十一、实施路线

### 11.1 已完成：v2 → v3（上述一~十章的原始范围）

Go 后端迁移、SSH 连接池复用修正、前端 composables/protocol 分层、5 项缺陷修复、
Docker + compose + install.sh 三系部署、CI/CD 双流水线。

### 11.2 已完成：v0.4 → v0.5 收敛（M0~M4）

| 里程碑 | 目标 | 关键动作 |
|--------|------|----------|
| **M0 纯减法** | 去掉从未被调用的代码 | 删死组件/兼容分支/未使用变量与导出 |
| **M1 协议换代** | 一条连接、两个平面 | `/ws/ssh`+`/ws/sftp` 合成单 `/ws`；控制面 JSON `{type,data,seq}`，数据面二进制 `[chan][flags][raw]`（`internal/wire`） |
| **M2 组合子化** | 每个函数只做一件事 | 拆 `handler` 为 chan_pty/chan_sftp/stream/wsmsg；`server.New` 统一装配；连接池/会话按职责归位 |
| **M3 稳定性** | 通道状态机与传输语义 | 帧大小单一来源、`FlagEnd` 收尾、写锁与背压、`.part` 接管续传、下载截断必须出声 |
| **M4 收敛** | 配置面与交付形态 | 删 `/api/ssh/test` 与 `mv`；环境变量 17→5；删 `MANAGI_TRUST_PROXY`（不再无条件信任 XFF；后续以 `MANAGI_TRUSTED_PROXIES` 白名单语义补回）；桌面托盘并入 `cmd/managi`（`-tags desktop` + `-tray`） |

### 11.3 已完成：v0.5.3 稳定 + 安全加固

后端稳定+安全审阅的 10 项全量修复（按严重度排序执行；各项的协议与部署细节已写入 §4.3/§4.4/§6.4/§8.2/§8.4）：

| 级别 | 项 | 落点 |
|------|----|------|
| P0 | 批量命令三道闸 | 节点 ≤100、总时限 15min、单路输出 4MB 截断（`handler/ssh.go`、`sshpool/exec.go`） |
| P0 | `/api/*` 跨站闸 | 仅 POST + `Sec-Fetch-Site` 校验 + 强制 `application/json` 且拒尾随字节（`handler/handler.go`） |
| P1 | 存活探测超时 | `SendRequest` 上限 5s，超时按已死处理并关闭连接（`sshpool/dial.go`） |
| P1 | 输入方向流控 | 每通道输入队列 + 工作协程；open 下发 `window`，`ack` 累计值滑动窗口（`handler/input_queue.go`、`stream.go`） |
| P1 | TLS 与可信代理 | `MANAGI_TLS_CERT/KEY` 成对生效；`MANAGI_TRUSTED_PROXIES` 控制 XFF 采信（`handler/auth.go`、`accesslog.go`） |
| P2 | 上传续传身份 | `.part` 命名带 `<size>-<mtime>`，失配重传并清理陈旧残片（`sftp/ops.go`） |
| P2 | TOFU 落盘 | 信任库 `~/.managi/known_hosts`；损坏拒连、不可写降级进程内（`sshpool/tofu.go`） |
| P2 | 部署安全默认值 | 空口令（`user:`）拒绝启动（废除随机口令机制）；免鉴权 + 非回环监听启动告警 |
| P3 | reattach 身份 | 会话命中时核对节点连接键，失配拒绝——会话不跨节点复用（`handler/live_session.go`） |

### 11.4 下一步

推进到 `0.5.*`：协议与配置面已定形，后续只做"改错的代价大于收益就不开放"这一原则下的收口。
动调优参数前先回到 §8.4 的取舍依据。

---

## 十二、设计总结

Managi 从"可用"走到"可分发、可部署、高性能"，再在 v0.4/v0.5 把自己收敛成一个小而稳的中转：

1. **统一工程结构**：monorepo 整合前后端与桌面端，单一 CI/CD 流水线覆盖全生命周期。
2. **Golang 后端**：原生并发 + 单静态二进制，彻底解决 v2 线程池瓶颈与连接复用缺陷。
3. **单二进制两形态**：同一份 `cmd/managi`，默认是服务器，`-tags desktop` + `-tray` 是 Windows 托盘；
   桌面端从 50MB（Nuitka）降到约 9MB，且两形态共用 `server.New`，装配不会漂移。
4. **协议一次到位**：一条 `/ws`、控制面 JSON + 数据面二进制，断点续传、结构化 resize、原生心跳。
5. **配置面收敛**：无状态中转（凭据只来自请求、不落盘），用户可调项只剩 8 个环境变量，
   调优参数一律编译期定死——可观测的失败比可配置的参数更有价值。

整体设计以"性能、可靠、可分发"为三角目标，并以"更少moving parts"为第四约束。

---

*文档版本: 5.1（原 design-v3.md，随 v0.5.* 架构收敛更名并对齐实现；v0.5.3 稳定+安全加固已同步）*
*更新日期: 2026-10-06*
