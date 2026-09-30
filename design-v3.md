# Managi v3 设计目标文档

> 本文档定义 Managi 第三代架构的设计目标、技术选型、模块规范与实施路线。

---

## 一、项目愿景与 v3 目标

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
| **Tauri 集成** | Go 二进制作为 Tauri sidecar 嵌入，Tauri 负责窗口/托盘，Go 负责业务，职责清晰 |
| **库选型** | Web 框架：`gin` 或 `net/http`+`gorilla/websocket`；配置：`viper` 或 `envconfig`；日志：`slog`(标准库) |

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
| **跨平台** | 当前仅 Windows（`//go:build windows`），macOS/Linux 可后续扩展 |
| **集成** | `getlantern/systray` 管理托盘图标与菜单，`pkg/browser` 打开默认浏览器，Go 服务监听 `127.0.0.1:18001` |
| **简化** | 省去 Tauri sidecar 通信与 Rust 构建依赖，单一可执行文件即完整桌面应用 |

> 注：原设计文档规划 Tauri，实际实现选择更轻量的 Go systray 方案。Tauri 章节保留为历史参考。

### 2.4 容器与部署

| 维度 | 选型理由 |
|------|----------|
| **Docker 基础镜像** | `golang:1.22-alpine` 构建 + `alpine:3.19` 运行（或 `scratch` + 静态二进制 + ca-certificates） |
| **多阶段构建** | builder 阶段编译 Go，运行阶段仅含二进制 + 前端 HTML，镜像 <25MB |
| **install.sh** | 纯 Shell，通过 `/etc/os-release` 判定 Alpine/Debian/Ubuntu，对应 apk/apt 安装依赖 |

---

## 三、统一项目架构（monorepo）

### 3.1 目录结构

```
managi-v3/
├── backend/                    # Go 后端
│   ├── cmd/managi/main.go      # 主入口
│   ├── internal/
│   │   ├── config/config.go    # 配置加载(环境变量)
│   │   ├── sshpool/pool.go     # SSH 连接池
│   │   ├── handler/            # HTTP + WebSocket handler
│   │   │   ├── ssh.go          # /api/ssh/test, /api/ssh/batch
│   │   │   ├── terminal.go     # /ws
│   │   │   └── sftp.go         # /ws/sftp
│   │   ├── sftp/ops.go         # SFTP 操作封装
│   │   ├── terminal/session.go # 终端会话
│   │   └── model/types.go      # 数据模型
│   ├── go.mod
│   └── Dockerfile
├── frontend/                   # Vue3 重构
│   ├── src/
│   │   ├── assets/             # 原样迁移 base.css/main.css
│   │   ├── components/         # 重构组件(保留 scoped CSS)
│   │   ├── composables/        # 新增:useWebSocket/useSFTP/useTerminal
│   │   ├── stores/             # Pinia
│   │   ├── views/              # CmdsView/XtremView
│   │   ├── protocol/           # 新增:WS 消息类型
│   │   └── api.ts              # HTTP 客户端(含重试)
│   ├── vite.config.ts
│   └── package.json
├── desktop/                    # Tauri 客户端
│   └── src-tauri/
│       ├── src/main.rs         # Tauri 入口(spawn sidecar)
│       ├── Cargo.toml
│       ├── tauri.conf.json     # sidecar/窗口/托盘配置
│       └── icons/
├── deploy/
│   ├── Dockerfile              # 多阶段(根级,引用 backend+frontend)
│   ├── docker-compose.yml
│   └── install.sh              # 三系跳板机部署
├── .github/workflows/
│   ├── ci.yml                  # push/PR: lint+test+build
│   └── release.yml             # tag: 多平台 Release
├── Makefile                    # dev/build/test/desktop/docker 统一入口
├── README.md
└── go.work                     # (可选)Go workspace
```

### 3.2 模块关系图

```
┌─────────────────────────────────────────────────────────────┐
│  Tauri 桌面外壳 (Rust)                                       │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  WebView (Vue3 前端)  ◄── HTTP/WS(localhost) ──►  Go   │  │
│  │  (frontend/)            sidecar (backend/)             │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
          │ Web 模式(无 Tauri): 浏览器直接访问 Go 服务
          ▼
┌─────────────────────────────────────────────────────────────┐
│  Go 后端 (backend/)                                          │
│  handler ─► sshpool ─► 远程 SSH/SFTP ─► 远程节点            │
└─────────────────────────────────────────────────────────────┘
          │ Docker/容器模式
          ▼
┌─────────────────────────────────────────────────────────────┐
│  部署层 (deploy/)                                            │
│  Dockerfile ─► docker-compose  /  install.sh ─► systemd     │
└─────────────────────────────────────────────────────────────┘
```

### 3.3 三种运行形态

| 形态 | 组合 | 启动方式 |
|------|------|----------|
| **Web 服务** | Go 后端(服务前端 HTML) | `./managi -port 18001` 或 Docker |
| **桌面客户端** | Tauri 外壳 + Go sidecar + Vue3 WebView | 双击安装包 |
| **跳板机** | Go 二进制 + systemd | `install.sh` 一键部署 |

---

## 四、后端 Golang 迁移设计

### 4.1 模块划分

| 模块 | 职责 | 对应 v2 |
|------|------|---------|
| `cmd/managi/main.go` | 启动 HTTP 服务、加载配置、注册路由 | `app.py` |
| `internal/config` | 环境变量加载、默认值 | `setting.py` |
| `internal/sshpool` | SSH 连接池(复用/保活/淘汰) | `ssh_pool.py` |
| `internal/handler` | HTTP + WebSocket 端点 | `routers.py` |
| `internal/sftp` | SFTP 操作(上传/下载/列目录/删除/mkdir/rename) | `sftp_client.py` |
| `internal/terminal` | 终端会话(Shell 透传/resize) | `routers.py` WS 部分 |
| `internal/model` | 数据结构(Node/CmdsTestResult/FileItem 等) | `models.py` |

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
    ├── 清理空闲超时(默认 120s,`MANAGI_SSH_IDLE_TIMEOUT`)的连接
    └── 池满(默认 20 条,`MANAGI_SSH_POOL_SIZE`)时淘汰最旧空闲连接
```

- **键含凭据指纹**：同一 `host:port:username` 换口令/换认证方式后不会复用旧连接，
  也不会把凭据本身写进键（指纹为 sha256 前 4 字节）。
- **命令执行也复用**：Get → 执行 → Release（减引用，不关闭），下次同节点命令直接复用。
- **保活**：每 30s 发送 keepalive packet；探测失败即从池中剔除，不滞留到 cleanIdle。
- **并发安全**：per-key `sync.Mutex` 串行化连接创建，连接本身 goroutine 安全。
- **主机密钥**：默认进程内 TOFU（首次记录公钥，之后不符即拒）；设 `MANAGI_KNOWN_HOSTS`
  走 OpenSSH known_hosts 严格校验，文件无法解析时拒绝所有连接而不退回 TOFU。

### 4.3 并发模型

| 场景 | 模型 |
|------|------|
| 批量命令 | `errgroup.Group` 并发执行多节点，`SetLimit` 控制并发数 |
| 终端会话 | 两个 goroutine：`forwardOutput`(SSH→WS)、`forwardInput`(WS→SSH)，`context.Context` 控制生命周期 |
| SFTP 下载流 | goroutine 逐块读取 + `select` 推送至 WS writer，`time.After` 让出调度 |
| WS 心跳 | `time.Ticker` 定时 Ping，`SetReadDeadline` 检测超时 |

### 4.4 WebSocket 协议与 HTTP 端点

v3 后端保持 v2 首包认证模式，前端可平滑对接：

| 端点 | 方法 | 协议 | 说明 |
|------|------|------|------|
| `/api/ssh/test` | POST | `{node, cmds}` | 单节点命令执行 |
| `/api/ssh/batch` | POST | `{nodes, cmds}` | 批量命令执行（errgroup 并发） |
| `/api/sftp/download` | POST | `{node, path}` + `Range` 头 | HTTP Range 下载（断点续传），凭据走 body 不落 URL |
| `/ws/ssh` | WS | 首包 `{type:"login", data:{node, session_id, cols, rows}}` | 终端会话，支持会话复用（60s 空闲保留） |
| `/ws/sftp` | WS | 首包 login + JSON 指令 + 二进制分片 | SFTP 操作，所有请求-响应带 `seq` 字段用于关联 |

**seq 关联**：SFTP WS 协议的 envelope 增加可选 `seq` 字段。前端发请求时分配递增 seq，后端响应回填同一 seq。前端按 seq 匹配响应，丢弃迟到的旧响应（如超时后到达的原请求响应）。向后兼容：旧前端不发 seq → 后端回填 0 → 行为不变；新前端遇旧后端（无 seq 响应）→ 回退按到达顺序。

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
├── types.ts        # Node / CmdsTestResult / FileItem / SFTPRequest / SFTPResponse
├── terminal.ts     # 终端消息(含 resize 序列)
└── sftp.ts         # SFTP 操作枚举 + 断点续传扩展协议
```

#### 5.2.2 `composables/` 逻辑复用层

| composable | 职责 | 修复缺陷 |
|------------|------|----------|
| `useWebSocket` | WS 连接管理(建立/重试/心跳/关闭) | 心跳失效、网络重试 |
| `useTerminal` | xterm 实例 + 尺寸同步 + 输入输出 | 终端换行 |
| `useSFTP` | SFTP 会话(列目录/上传/下载/续传) | 上传/下载中断 |
| `useRetry` | 通用指数退避重试 | 网络响应丢失 |

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

**修复方案**：
1. **HTTP 层**（`api.ts`）：封装 `fetchWithRetry`，指数退避重试 3 次（1s/2s/4s），仅对 5xx 与网络错误重试，4xx 不重试。
2. **超时**：每个请求 `AbortController` 30s 超时。
3. **WS 层**（`useWebSocket`）：断线指数退避重连，上限 5 次（1s/2s/4s/8s/16s），重连失败通知用户。
4. **幂等性**：批量命令请求带 `request_id`，重试时同 id 服务端去重。

**验收**：模拟网络抖动，请求自动重试成功；断线 5 次内自动恢复。

### 6.3 心跳连接失效

**问题分析**：
- v2 前端每 30s 发 `\x00`，但 `\x00` 作为终端输入会进入 Shell，可能引发异常。
- v2 后端无心跳超时检测，连接假死不被发现。
- 中间网络设备空闲超时断开 WS，v2 无感知。

**修复方案**：
1. **WS Ping/Pong**：使用 WebSocket 原生 Ping/Pong 帧（非业务数据），前端每 30s `ws.ping()`，后端 `SetPongHandler` 重置读超时。
2. **服务端超时**：`SetReadDeadline(60s)`，未收到 Pong 主动关闭连接触发前端重连。
3. **保活分离**：SSH Shell 保活由后端 keepalive packet 处理，与 WS 心跳解耦。
4. **终端输入不再用 `\x00`**：移除 v2 的空字节心跳。

**验收**：空闲 5 分钟连接不断；拔网线 60s 内服务端检测并清理。

### 6.4 文件上传中断（断点续传）

**问题分析**：
- v2 上传为单帧整体发送，文件大或网络抖动即失败，无续传。
- v2 上传无分片、无上传 ID、无进度保存。

**修复方案**：

**协议扩展**（`/ws/sftp` 新增指令，统一 envelope `{type,data,seq}`）：
```
上传初始化:  {type:"upload_init", data:{remote_path, filename, total_size, chunk_size}, seq}
             ← {type:"upload_init", data:{upload_id, offset, chunk_size}, seq}
                 // offset = 已有 .part 大小（续传起点）
                 // chunk_size 由服务端下发（MANAGI_SFTP_CHUNK_SIZE），前端按它切片；
                 // 请求里带的 chunk_size 仅作观测，服务端不采用。
分片上传:    二进制帧 [8B seq][4B upload_id_len][upload_id][4B chunk_index][8B offset][8B data_len][data]
             ← {type:"chunk_ack", data:{chunk_index}, seq}
上传完成:    {type:"upload_complete", data:{upload_id}, seq}
             ← {type:"ok", seq}
```

**服务端**：
- `upload_init` 生成 `upload_id`，在远端创建 `.part` 临时文件。
- 查询已有 `.part` 文件大小返回 `offset`（断点续传依据）；比本次 total_size 还大的脏 `.part` 直接丢弃重传。
- 分片按 offset 顺序写入 `.part`（offset 与服务端记录不一致即拒绝，超过 total_size 也拒绝），完成后 rename 为目标文件。
- `upload_id` 与进度保存在内存 map（会话级），可选落盘防重启丢失。

**前端**：
- 分片大小取 `upload_init` 响应的 `chunk_size`（旧后端不带该字段时回退本地 1MB），`File.slice(pos, pos+chunk)`。
- 上传前先 `upload_init` 查询 offset，从 offset 开始上传。
- 进度：`min(pos + 已写字节, file.size) / file.size`，封顶 100%。

**验收**：上传中断网络，恢复后从中断点继续；刷新页面后可恢复（依赖 localStorage + 服务端 .part）。

### 6.5 文件下载中断（断点续传）

**问题分析**：
- v2 下载为 WS 分块流，前端全量累积到内存，中断即丢失全部进度。
- 无 Range 支持，无法续传。

**修复方案**：

**改为 HTTP Range 下载**（SFTP 下载经后端中转）：
```
POST /api/sftp/download          body: {node, path}   // 凭据走 body，不落 URL/访问日志
Header: Range: bytes=<offset>-   // 续传偏移，只支持开区间
← 206 Partial Content
   Content-Range: bytes <offset>-<total-1>/<total>
   Body: 流式分块
← 416 Requested Range Not Satisfiable   // offset ≥ total，Content-Range: bytes */<total>
← 200 OK（整文件）                       // 无 Range，或写法不支持：带结束位、后缀区间、非数字
```

**服务端**：
- 后端打开 SFTP 文件，`Seek(offset)` 后流式 copy 到 HTTP ResponseWriter。
- 支持 `Range` 请求头，返回 206 + `Content-Range`。
- 起点越过文件末尾必须回 416：发 200/206 空体会让续传客户端以为尾部已取完，停在错误偏移再也不敢传。
- 只认 `bytes=<起始>-`；其余写法按 RFC 7233「忽略不理解的 Range」退回 200 整文件，
  而不是半支持（按起始位截断会多发数据，Content-Range 与请求不符）。
- 流式写出，不载入内存。

**前端**：
- 使用 `fetch` + `ReadableStream` 读取，边读边写入 `FileSystemWritableFileStream`（File System Access API）或累积到 Blob。
- 断点记录：`localStorage` 存 `{path, offset, total}`，中断后下次从 offset 续传。
- 进度：`received / total` 实时更新。

**降级**：不支持 File System Access API 的浏览器，回退到内存累积 + Blob 下载（同 v2）。

**验收**：下载中断后恢复从断点继续；大文件(>1GB)下载不 OOM。

---

## 七、桌面客户端设计

> **注**：实际实现选择更轻量的 Go systray 方案（见 §2.3），而非 Tauri。本节保留 Tauri 设计为历史参考。

### 7.1 实际实现：Go systray

```
┌─────────────────────────────────────┐
│  windows-app.exe (Go)               │
│  ├─ systray 托盘图标与菜单          │
│  ├─ 内嵌 HTTP 服务 (127.0.0.1:18001)│
│  ├─ 内嵌前端 index.html             │
│  └─ 自动打开默认浏览器              │
└─────────────────────────────────────┘
```

单二进制约 9MB，无需 Rust 工具链或 sidecar 通信。

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

### 7.2 配置要点（`tauri.conf.json`）

| 配置项 | 值 |
|--------|-----|
| `bundle.externalBin` | `binaries/managi`（Go 编译产物，按平台三后缀） |
| `app.windows[0]` | 标题 Managi、固定尺寸、图标 |
| `app.trayIcon` | icon.ico/png，菜单：打开主窗口、退出 |
| `build.beforeBuildCommand` | `cd frontend && npm run build` |
| `build.beforeDevCommand` | `cd frontend && npm run dev` |
| `build.devUrl` | `http://localhost:5173` |
| `build.frontendDist` | `../frontend/dist` |

### 7.3 Sidecar 生命周期

1. Tauri 启动 → spawn Go 二进制（参数 `-port 0` 自动选端口或固定 18001）。
2. Rust 端轮询 `http://127.0.0.1:<port>/health` 直至就绪。
3. 就绪后 WebView 加载 `http://127.0.0.1:<port>`。
4. 退出时 Rust 端发送 SIGTERM 给 sidecar，等待 5s 后强杀。

### 7.4 跨平台构建

| 平台 | Go 编译目标 | Tauri 产物 | 安装包格式 |
|------|-------------|-----------|-----------|
| Windows | `GOOS=windows GOARCH=amd64` | `managi.exe` | `.msi` / `.exe`(NSIS) |
| macOS | `GOOS=darwin GOARCH=amd64/arm64` | `managi` | `.dmg` |
| Linux | `GOOS=linux GOARCH=amd64` | `managi` | `.deb` / `.AppImage` |

### 7.5 客户端-服务端通信

- WebView 直连 `http://127.0.0.1:<port>`，HTTP + WebSocket。
- 与 Web 模式完全一致的协议，前端代码零改动。
- 端口冲突时 sidecar 探测可用端口（18001-18100），写入临时文件供 WebView 读取。

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
- `MANAGI_BASICAUTH_PASSWORD` 由宿主环境或同目录 `.env` 注入（样板见 `deploy/.env.example`），
  compose 用 `${VAR:?}` 强校验：留空不是「无密码」，而是每次启动换随机口令并打印进日志，
  配合 `restart: unless-stopped` 等于口令天天变、历史明文留在 `docker logs`。
- 端口只由 `MANAGI_PORT` 决定。镜像的 `CMD` 不写 `-port`：命令行 flag 优先级高于环境变量，
  写死会让用户改的 `MANAGI_PORT` 静默失效。需要追加参数时用 compose 的 `command:` 覆盖。

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

---

## 九、GitHub Actions 设计

### 9.1 `ci.yml`（push/PR 触发）

| Job | 触发 | 步骤 |
|-----|------|------|
| `lint-test-go` | push/PR | `go vet`、`golangci-lint`(v2.6)、`go test -race ./...` |
| `build-go` | push/PR | `CGO_ENABLED=0 GOOS=linux go build ./cmd/managi` 并检查产物体积 |
| `build-frontend` | push/PR | `npm ci`、`npm run type-check`、`npm run test`、`npm run build`、校验 dist/index.html 生成 |
| `build-windows-app` | push(main) | 构建前端 → 拷贝 dist 与图标 → rsrc 生成资源 → `go build` windows-app.exe |
| `deploy-scripts` | push/PR | `sh -n` 语法检查 + 运行 `deploy/install_test.sh`（verify_checksum 的 sidecar/环境变量两条路径） |
| `security-scan` | push/PR | Trivy 文件系统扫描（CRITICAL/HIGH，`exit-code: 1`，忽略上游无补丁项） |

注：Go/Node 版本以 `backend/go.mod` 与 workflow 内 `node-version` 为准；CI 无独立 docker/tauri job，
Dockerfile 的构建验证由本地或发布流水线承担。

### 9.2 `release.yml`（tag `v*` 触发）

| Job | 产物 |
|-----|------|
| `build-go-binaries` | matrix(linux amd64/arm64 各含 glibc 与 musl、darwin amd64/arm64、windows amd64) → 裸二进制资产 |
| `build-frontend` | dist/index.html（vite-single 单文件） |
| `build-windows-app` | windows-app.exe（内嵌前端与图标，需先取 build-frontend 产物） |
| `release` | 汇总上述产物 + `deploy/install.sh` 上传 GitHub Release（`generate_release_notes`） |

说明：桌面端走「Go + 系统托盘 + 内嵌 index.html」，不再使用 Tauri；镜像由 `deploy/Dockerfile`
与 `deploy/docker-compose.yml` 本地构建，发布流水线不推 GHCR。

### 9.3 CI/CD 流程图

```
push/PR ─► ci.yml ─► lint+test+build 验证
                          │ (main 通过)
                          ▼
tag v* ─► release.yml ─► Go 多平台编译
                        ├─ 前端构建
                        ├─ Tauri 多平台打包
                        ├─ Docker 镜像推送 GHCR
                        └─ GitHub Release 资产上传
```

---

## 十、性能指标与验收标准

### 10.1 量化验收表

| 指标 | 验收方法 | 目标 |
|------|----------|------|
| Go 二进制体积 | `ls -lh managi` | <20MB |
| Docker 镜像体积 | `docker images managi` | <25MB |
| Tauri 安装包体积 | 构建产物 | <15MB |
| 运行时内存(空闲) | `docker stats` 或 `ps aux` | <60MB |
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
| WS 协议 | v3 后端兼容 v2 前端（除断点续传扩展外） |
| 浏览器 | Chrome/Edge/Firefox 最近两版 |
| OS | Windows 10+/macOS 11+/Ubuntu 20.04+/Alpine 3.19+ |
| 跳板机 | install.sh 在 Alpine/Debian/Ubuntu 干净环境成功 |

---

## 十一、实施优先级

### 11.1 四阶段路线图

```
P0 (核心) ──────────────────────────────────────────► P1 (部署) ──► P2 (客户端) ──► P3 (CI/CD)
│ 后端 Go 迁移 + 前端 5 缺陷修复                       │ Docker     │ Tauri        │ GitHub
│ - Go 后端实现 4 端点                                │ install.sh │ 跨平台打包   │ Actions
│ - SSH 连接池(复用修正)                              │ compose    │ 托盘/自启    │ ci.yml
│ - 前端 composables/protocol 层                      │            │              │ release.yml
│ - 5 缺陷修复                                        │            │              │
└────────────────────────────────────────────────────┘└────────────┘└──────────────┘└──────────┘
```

### 11.2 阶段依赖

| 阶段 | 前置 | 可并行 |
|------|------|--------|
| P0 | 无 | 前端缺陷修复与 Go 后端可并行 |
| P1 | P0(Go 二进制) | docker-compose 与 install.sh 并行 |
| P2 | P0(前后端) + P1(Dockerfile 参考) | 三平台打包并行 |
| P3 | P0/P1/P2 全部产物 | ci.yml 可在 P0 后即开始 |

### 11.3 当前脚手架范围

本 design-v3.md 配套的 `./managi-v3` 脚手架已建立完整目录结构与骨架文件（含 TODO 指向本文档章节），具体业务实现按上述优先级推进。

---

## 十二、设计总结

Managi v3 通过四项关键演进实现从"可用"到"可分发、可部署、高性能"的跃迁：

1. **统一工程结构**：monorepo 整合前后端与桌面端，单一 CI/CD 流水线覆盖全生命周期。
2. **Golang 后端**：原生并发 + 单静态二进制，彻底解决 v2 线程池瓶颈与连接复用缺陷，体积/内存/启动全面优化。
3. **Tauri 跨端**：以系统 WebView 替代 Chromium，安装包从 50MB 降至 <15MB，实现 Win/macOS/Linux 统一分发。
4. **协议增强**：在保持 v2 协议兼容前提下，扩展断点续传、结构化 resize、原生心跳，修复 5 项关键缺陷。

整体设计以"性能、可靠、可分发"为三角目标，在保持核心易用性的同时补齐工程化短板。

---

*文档版本: 3.0*
*生成日期: 2026-06-28*
