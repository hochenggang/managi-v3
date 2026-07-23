# Managi v3 小版本优化计划

## 摘要

对项目进行全面边界检查与优化，保持核心设计不变（单代码库、跳板机+本地SSH客户端、浏览器使用），以现代化编码习惯让数据流和逻辑流更简单、稳定、可靠。

## 当前状态分析

项目整体架构清晰，已修复大量缺陷（B1-B36, E2, M1-M6, T1-T4, S3, S6, H1-H8, C1-C5, P1-P3, R3-R6, A7-A12, A19, L1, L5, N1-N3, FI1 等），但仍存在以下可优化项：

### 后端问题

1. **连接池 perKeyLocks 内存泄漏**：`pool.go` 中 `keyLock()` 创建的 per-key 锁仅在连接被清理时删除。若某个 key 频繁 Get/Release 但连接长期存活，锁会累积。但更关键的是：`keyLock()` 本身使用 `perKeyLock` 全局锁保护 `perKeyLocks` map，在大量并发 Get 时可能成为瓶颈。

2. **Session Manager perKeyLocks 同理**：`live_session.go` 的 `keyLock()` 与连接池同样模式，同样的问题。

3. **SSH 命令执行缺少输入验证**：`ssh.go` 中 `testHandler` 和 `batchHandler` 未验证 cmds 长度/内容，恶意客户端可提交超大命令列表消耗服务器资源。

4. **Node 验证缺失**：API handler 未验证 Node 字段完整性（host/port/username 是否为空、port 是否合法），可能触发下游 panic 或无意义连接。

5. **SFTP 下载 HTTP 端点安全问题**：`sftpDownloadHandler` 通过 query string 传递 `node=JSON.stringify(node)`，凭据明文出现在 URL 中（浏览器历史/代理日志/Referrer 可能泄露）。

6. **错误响应不一致**：部分 handler 返回纯文本 `http.Error()`，部分返回 JSON；前端 `fetchWithRetry` 期望 JSON 响应，非 JSON 错误会导致 `resp.json()` 抛异常。

7. **uploadIdleTimeout 硬编码**：30 分钟超时值写死在 `ops.go` 中，其他超时值均通过 config 可配。

8. **sftp.Client.Close 与 StartUploadCleaner 竞态**：Close() 关闭 cleanerDone channel 后，StartUploadCleaner 的 goroutine 中 ticker select 可能尚未退出；同时 Close 持锁清理 uploads 时，cleanExpiredUploads 可能同时在等锁。

9. **连接池 maxSize/hardCap 硬编码**：`New()` 中 maxSize=20, hardCap=40，不可通过配置调整。

10. **scrollback 截断策略**：`appendScrollbackLocked` 每次超限都做 `append([]byte(nil), ls.buf[cut:]...)` 即整块拷贝，高频输出场景有 CPU 开销。

### 前端问题

11. **XtremView 中 useTerminal 返回值未使用**：`useTerminal(terminalContainer.value, node)` 的返回值 `{ term, status }` 被丢弃，组件无法访问终端实例或连接状态。

12. **CmdsView 中 copyCode 不安全**：直接调用 `navigator.clipboard.writeText()` 而无安全上下文降级，非 HTTPS 环境会静默失败。`useTerminal.ts` 已实现 `copyToClipboard` 降级但未复用。

13. **handleRenameShortcut 使用 window.prompt**：原生 prompt 与项目 UI 风格不一致，其他地方已用 useConfirm/Modal 替代。

14. **nodesStore beforeunload 监听器未清理**：`window.addEventListener('beforeunload', flushSave)` 注册后永不移除，虽然影响极小（全局单例 store），但违反 Vue 生命周期管理惯例。

15. **SFTP 大文件下载 HTTP 路径凭据暴露**：与后端第5点对应，前端通过 `new URLSearchParams({ node: JSON.stringify(node), path })` 将凭据明文放入 URL。

16. **downloadViaHTTP 缺少凭证传递**：BasicAuth 环境下 fetch 请求未设置 `credentials: 'include'`，浏览器不会自动带 Authorization 头，导致 401。

17. **前端 API 类型安全不足**：`testSSH` 和 `batchSSH` 的返回值直接 `resp.json()` 未做类型校验，后端变更可能在前端静默产生 `undefined` 字段。

18. **sessionIds Map 使用的 fallback 不可靠**：`Math.random().toString(36).slice(2) + Date.now().toString(36)` 在极端情况下可能碰撞，且不如 `crypto.randomUUID()` 安全。

19. **Finder.vue 中 deleteSelected 确认对话框换行**：`${t("finder.deleteConfire")}\n${selectedFile.value.filename}` 中 `\n` 在 HTML 中不生效，应改为模板内换行或 `<br>`。

20. **前端 settingsStore 未验证 terminalFontSize 范围**：`importSettings` 中仅检查 `typeof partial.terminalFontSize === 'number'`，未校验范围，可能写入超范围值（虽然 `setTerminalFontSize` 内部 clamp 了）。

21. **useSFTP 中 close() 未使用 onUnmounted 自动清理**：依赖 Finder.vue 的 `onBeforeUnmount(() => close())`，若其他组件使用 useSFTP 忘记手动 close，会泄漏 WebSocket。

### 跨层/架构问题

22. **前后端协议类型定义分散**：后端 `wsmsg.go` 的消息类型常量与前端 `ws.ts` 的 `WSMessageType` 各自维护，无自动同步机制，协议变更可能导致不一致。

23. **Node 凭据传输安全**：Node 的 auth_value（密码/私钥）在前端 localStorage 明文存储，且通过 HTTP/WS 传输到后端。跳板机场景下，多用户共享浏览器时存在风险。

24. **后端无请求日志**：HTTP handler 未记录请求方法/路径/耗时/状态码，生产环境排障困难。

25. **Go 版本声明 1.25.0**：go.mod 中 Go 版本可能超前于实际可用版本，CI 可能失败。

26. **Tauri 残留引用**：代码注释和 .gitignore 中仍有 Tauri 相关引用，但项目已改用 Go 托盘客户端（`backend/cmd/windows-app`），应清理所有 Tauri 残留。

---

## 提议变更

### 前置 - 移除 Tauri 残留

#### 0. 清理所有 Tauri 引用
- **文件与变更**:
  - `frontend/vite.config.ts` L9: `便于 Tauri 内嵌与后端静态服务` → `便于桌面端内嵌与后端静态服务`
  - `backend/cmd/managi/main.go` L56: `供 Tauri sidecar 与 Docker healthcheck 使用` → `供 Windows 桌面端与 Docker healthcheck 使用`
  - `frontend/src/api.ts` L109: `建议启用 BasicAuth 或使用 Tauri 桌面端` → `建议启用 BasicAuth 或使用 Windows 桌面端`
  - `backend/internal/handler/auth.go` L116: `Docker healthcheck / Tauri sidecar 探活` → `Docker healthcheck / Windows 桌面端探活`
  - `backend/internal/handler/auth.go` L158: `非浏览器客户端如 Tauri sidecar / 测试工具` → `非浏览器客户端如 Windows 桌面端 / 测试工具`
  - `.gitignore`: 移除 `# Rust / Tauri` 注释及其下 `target/`、`binaries/` 条目
- **原因**: 项目已采用 Go 托盘客户端，Tauri 引用过时

### P0 - 必修（安全/稳定性）

#### 1. API 统一错误响应格式
- **文件**: `backend/internal/handler/ssh.go`
- **变更**: 将 `http.Error(w, err.Error(), http.StatusBadRequest)` 改为返回 JSON `{"error": "message"}`
- **原因**: 前端 `fetchWithRetry` 期望 JSON，非 JSON 响应在 `resp.json()` 时抛异常，错误信息丢失

#### 2. SFTP HTTP 下载凭据安全
- **文件**: `backend/internal/handler/sftp.go`, `frontend/src/api.ts`
- **变更**: 将 node 信息从 query string 移到 POST body（前端改为 POST 请求，后端从 body 读取 node），在 header 中传递 Range
- **原因**: 凭据明文出现在 URL 中会被浏览器历史/代理日志/Referrer 泄露

#### 3. BasicAuth 下 fetch 凭证传递
- **文件**: `frontend/src/api.ts`
- **变更**: `downloadWithRange` 的 fetch 调用添加 `credentials: 'include'`
- **原因**: BasicAuth 环境下浏览器不自动带 Authorization 头，导致 401 失败

#### 4. Node 字段验证
- **文件**: `backend/internal/handler/ssh.go`
- **变更**: 在 testHandler/batchHandler 中验证 Node.Host 非空、Node.Port 在 1-65535、Node.Username 非空
- **原因**: 无效 Node 会触发下游无效 SSH 连接，浪费资源

#### 5. Cmds 长度限制
- **文件**: `backend/internal/handler/ssh.go`
- **变更**: 验证 cmds 数组长度上限（如 100 条）和单条命令长度上限（如 4096 字符）
- **原因**: 防止恶意客户端提交超大命令列表消耗服务器资源

### P1 - 推荐（可靠性/健壮性）

#### 6. XtremView useTerminal 返回值使用
- **文件**: `frontend/src/views/XtremView.vue`
- **变更**: 存储 useTerminal 返回的 `{ term, status }`，用 status 展示连接状态
- **原因**: 当前丢弃返回值，组件无法感知连接状态

#### 7. copyCode 降级处理
- **文件**: `frontend/src/views/CmdsView.vue`
- **变更**: 将 `navigator.clipboard.writeText` 替换为从 `useTerminal.ts` 提取的 `copyToClipboard` 工具函数（提取到 `helper.ts`）
- **原因**: 非 HTTPS 环境静默失败，useTerminal 已有降级方案

#### 8. handleRenameShortcut 改用 Modal
- **文件**: `frontend/src/views/CmdsView.vue`
- **变更**: 将 `window.prompt` 替换为 Modal 对话框（与项目其他地方一致）
- **原因**: 原生 prompt 与项目 UI 风格不一致

#### 9. 后端请求日志
- **文件**: `backend/internal/handler/handler.go`（新增中间件）
- **变更**: 添加轻量 HTTP 请求日志中间件，记录 method/path/status/latency
- **原因**: 生产环境排障无日志可查

#### 10. 连接池/Session Manager keyLock cleanup
- **文件**: `backend/internal/sshpool/pool.go`, `backend/internal/handler/live_session.go`
- **变更**: 在 `cleanIdle` 和 `close` 中清理不再使用的 perKeyLocks 条目（当前已有部分清理，但 keyLock() 创建的锁在 Get 失败场景下不会被清理）
- **原因**: 长期运行可能导致 perKeyLocks map 累积无用锁

#### 11. useSFTP 自动清理
- **文件**: `frontend/src/composables/useSFTP.ts`
- **变更**: 在 useSFTP 内部注册 `onUnmounted(() => close())`，而非依赖调用方手动调用
- **原因**: 当前依赖 Finder.vue 的 onBeforeUnmount，若其他组件使用 useSFTP 忘记 close 会泄漏

#### 12. uploadIdleTimeout 可配化
- **文件**: `backend/internal/config/config.go`, `backend/internal/sftp/ops.go`
- **变更**: 将 `uploadIdleTimeout` 改为从 config 读取，环境变量 `MANAGI_SFTP_UPLOAD_TIMEOUT`
- **原因**: 与其他超时值保持一致的可配性

#### 13. 连接池容量可配
- **文件**: `backend/internal/config/config.go`, `backend/internal/sshpool/pool.go`
- **变更**: 新增 `MANAGI_SSH_POOL_SIZE` 环境变量控制 maxSize，hardCap 自动为 2x
- **原因**: 不同规模部署需要不同池大小

#### 14. 前端删除确认对话框换行修复
- **文件**: `frontend/src/components/Finder.vue`
- **变更**: 确认对话框中 `\n` 改为模板内换行展示
- **原因**: `\n` 在 HTML 中不换行，用户看到的确认信息格式错误

### P2 - 可选（代码质量/现代化）

#### 15. 提取 copyToClipboard/readFromClipboard 到 helper.ts
- **文件**: `frontend/src/helper.ts`, `frontend/src/composables/useTerminal.ts`
- **变更**: 将剪贴板工具函数从 useTerminal.ts 移到 helper.ts 并导出，useTerminal 和 CmdsView 共用
- **原因**: 消除重复代码，统一降级策略

#### 16. sessionIds fallback 移除
- **文件**: `frontend/src/composables/useTerminal.ts`
- **变更**: 移除 `Math.random().toString(36)...` fallback，所有现代浏览器都支持 `crypto.randomUUID()`
- **原因**: fallback 不够安全且代码已过时，Managi 目标浏览器均支持 crypto.randomUUID

#### 17. scrollback 截断优化
- **文件**: `backend/internal/handler/live_session.go`
- **变更**: 使用 ring buffer 或预分配 + 双指针替代当前每次超限整块拷贝的方式
- **原因**: 高频输出时减少内存分配和拷贝

#### 18. settingsStore importSettings 范围校验
- **文件**: `frontend/src/stores/settingsStore.ts`
- **变更**: `importSettings` 中对 terminalFontSize 做范围校验（与 setTerminalFontSize 一致）
- **原因**: 防止导入配置时写入超范围值

#### 19. fetchWithRetry 统一错误处理
- **文件**: `frontend/src/api.ts`
- **变更**: `testSSH`/`batchSSH` 中 `resp.json()` 加 try/catch，解析失败时抛出更有意义的错误
- **原因**: 后端返回非 JSON 时前端静默崩溃

---

## 假设与决策

1. **核心设计不变**：单代码库、跳板机+本地SSH客户端、浏览器使用
2. **优先级排序原则**：安全 > 稳定性 > 代码质量
3. **向后兼容**：API 变更需要兼容旧客户端（SFTP 下载改为 POST 是 breaking change，需同步更新前后端）
4. **简约优先**：不引入新依赖，不过度设计
5. **P2 项视时间/复杂度取舍**：ring buffer 等重构可能增加复杂度，需权衡收益

## 验证步骤

1. 所有变更完成后运行 `go test ./...` 确保后端测试通过
2. 所有变更完成后运行前端测试（如有）确保无回归
3. 手动验证：SFTP 下载在 BasicAuth 开启时正常工作
4. 手动验证：API 错误返回 JSON 格式
5. 手动验证：Node 字段验证拒绝无效输入
6. 手动验证：XtremView 终端连接状态可见
7. 手动验证：Finder 删除确认对话框换行正确显示
