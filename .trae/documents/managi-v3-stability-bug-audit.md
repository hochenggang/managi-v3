# 前端连接状态细分计划

## 概述

将终端（TerminalTab）的连接状态从简单的 `boolean`（已连接/未连接）细分为 6 种状态：首次连接（进行中/成功/失败）+ 重试连接（进行中/成功/失败），使用户能清晰感知当前连接阶段。

## 当前状态分析

### 现有架构

1. **`useWebSocket.ts`**：暴露 `connected: Ref<boolean>`，仅 true/false 两态。内部有 `reconnectAttempts`、`manualClose` 等状态，但未对外暴露。
   - [useWebSocket.ts#L29](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/composables/useWebSocket.ts#L29): `const connected = ref(false)`
   - [useWebSocket.ts#L60-L68](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/composables/useWebSocket.ts#L60-L68): `ws.onopen` 设 `connected = true`
   - [useWebSocket.ts#L78-L92](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/composables/useWebSocket.ts#L78-L92): `ws.onclose` 设 `connected = false`，按 `manualClose` + `reconnectAttempts` 决定重连或触发 `onReconnectFailed`

2. **`useTerminal.ts`**：透传 `connected`，在登录失败时调用 `close()` 抑制重连。
   - [useTerminal.ts#L58](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/composables/useTerminal.ts#L58): `const { connected, connect, send, close } = useWebSocket(...)`
   - [useTerminal.ts#L70-L81](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/composables/useTerminal.ts#L70-L81): `onText` 中处理 `login` 结果，失败时调 `close()`
   - [useTerminal.ts#L147](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/composables/useTerminal.ts#L147): `return { term, connected }`

3. **`TerminalTab.vue`**：用 `connected` 布尔值控制 UI。
   - [TerminalTab.vue#L5-L7](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/views/TerminalTab.vue#L5-L7): `v-if="node && !connected"` 显示 "连接断开，正在重连…" 覆盖层
   - [TerminalTab.vue#L9-L13](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/views/TerminalTab.vue#L9-L13): 工具栏显示 "已连接"(绿色) 或 "连接已断开"(红色)
   - [TerminalTab.vue#L50-L54](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/views/TerminalTab.vue#L50-L54): 值拷贝 `connected.value = wsConnected.value` + `watch`（L4 问题：首帧可能过期）

### 问题

- 布尔值无法区分"首次连接中"、"重连中"、"登录失败"、"重连耗尽"等状态
- 登录失败和网络断开显示相同提示（"连接断开，正在重连…"），用户无法判断是认证错误还是网络波动
- 覆盖层在所有非连接状态都显示"正在重连"，包括登录失败（不会重连）的情况

---

## 提议变更

### 状态模型

定义 `ConnectionStatus` 类型，6 种业务状态 + 1 个初始态：

```typescript
export type ConnectionStatus =
  | 'idle'               // 初始未连接（无节点时）
  | 'connecting'         // 首次连接进行中
  | 'connected'          // 已连接（首次成功或重连成功）
  | 'first_failed'       // 首次连接失败（登录失败等，不重试）
  | 'reconnecting'       // 重连进行中
  | 'reconnect_failed'   // 重连失败（达到最大重试次数或重连后登录失败）
  | 'disconnected'       // 主动断开（组件卸载）
```

状态转换图：
```
idle ──connect()──→ connecting ──onopen──→ connected
                       │                       │
                    onclose                onclose(非manual)
                    (非manual)                │
                       │                       ↓
                       ↓                  reconnecting
                   reconnecting          ├──onopen──→ connected
                                       ├──onclose(max)──→ reconnect_failed
                                       └──login fail──→ reconnect_failed
connecting ──login fail──→ first_failed
connected ──close()──→ disconnected
```

### 文件变更清单

#### 1. `frontend/src/composables/useWebSocket.ts` — 核心状态管理

**变更内容**：
- 新增 `ConnectionStatus` 类型导出
- 将 `connected: Ref<boolean>` 替换为 `status: Ref<ConnectionStatus>`
- 新增内部标志 `hasLoginSucceeded: boolean`（由调用方通过 `markLoginSuccess()` 设置）
- 新增 `markLoginSuccess()` 方法：标记应用层登录成功，用于区分首次连接与重连
- 新增 `markFailed()` 方法：标记连接失败（登录失败），设置 `first_failed` 或 `reconnect_failed` 并关闭 WS（不触发重连）
- 保留 `connected` 作为 computed（`status === 'connected'`），向后兼容 `useSFTP`
- 修改 `connect()`：根据 `hasLoginSucceeded` 设置 `connecting` 或 `reconnecting`
- 修改 `ws.onopen`：设 `status = 'connected'`
- 修改 `ws.onclose`：
  - `manualClose`：不改变 status（由 `close()`/`markFailed()` 设置）
  - 可重连：`status = 'reconnecting'`
  - 重连耗尽：`status = hasLoginSucceeded ? 'reconnect_failed' : 'first_failed'`
- 修改 `close()`：设 `status = 'disconnected'`
- 返回值：`{ status, connected, connect, send, close, markFailed, markLoginSuccess }`

**关键代码片段**（示意，非最终实现）：
```typescript
export type ConnectionStatus = 'idle' | 'connecting' | 'connected' | 'first_failed' | 'reconnecting' | 'reconnect_failed' | 'disconnected'

export function useWebSocket(path: string, opts: WSOptions = {}) {
  const status = ref<ConnectionStatus>('idle')
  let hasLoginSucceeded = false

  function connect(): void {
    manualClose = false
    reconnectAttempts = 0
    status.value = hasLoginSucceeded ? 'reconnecting' : 'connecting'
    doConnect()
  }

  function doConnect(): void {
    ws = new WebSocket(url)
    ws.onopen = () => {
      status.value = 'connected'
      reconnectAttempts = 0
      if (opts.authPayload !== undefined) ws?.send(opts.authPayload)
      startHeartbeat()
      opts.onOpen?.()
    }
    ws.onclose = (ev) => {
      stopHeartbeat()
      if (manualClose) return // 状态已由 close()/markFailed() 设置
      if (reconnectAttempts < (opts.maxReconnect ?? 3)) {
        status.value = 'reconnecting'
        const delay = Math.min(1000 * 2 ** reconnectAttempts, 16000)
        reconnectAttempts++
        reconnectTimer = setTimeout(doConnect, delay)
      } else {
        status.value = hasLoginSucceeded ? 'reconnect_failed' : 'first_failed'
        opts.onReconnectFailed?.()
        opts.onClose?.()
      }
    }
    // ... onerror, onmessage 不变
  }

  function markLoginSuccess(): void {
    hasLoginSucceeded = true
  }

  function markFailed(): void {
    manualClose = true
    stopHeartbeat()
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null }
    status.value = hasLoginSucceeded ? 'reconnect_failed' : 'first_failed'
    if (ws) { ws.onclose = null; ws.onerror = null; ws.close(); ws = null }
  }

  function close(): void {
    manualClose = true
    stopHeartbeat()
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null }
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    if (ws) { ws.onclose = null; ws.onerror = null; ws.close(); ws = null }
    status.value = 'disconnected'
  }

  const connected = computed(() => status.value === 'connected')
  return { status, connected, connect, send, close, markFailed, markLoginSuccess }
}
```

#### 2. `frontend/src/composables/useTerminal.ts` — 透传状态 + 登录回调

**变更内容**：
- 解构 `status` 替代 `connected`，新增 `markFailed`、`markLoginSuccess`
- `onText` 中 `login` case：
  - 成功：调 `markLoginSuccess()`（区分首次与重连）
  - 失败：调 `markFailed()` 替代 `close()`（设置正确的失败状态并关闭 WS）
- 返回值：`{ term, status }` 替代 `{ term, connected }`

**关键变更**（[useTerminal.ts#L58](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/composables/useTerminal.ts#L58)）：
```typescript
const { status, connect, send, close, markFailed, markLoginSuccess } = useWebSocket('/ws', {
  authPayload: loginMessage(node, sessionId, term.cols, term.rows),
  maxReconnect: 10,
  onText: (data) => {
    const msg = parseWSMessage(data)
    if (!msg) return
    switch (msg.type) {
      case 'msg':
        if (typeof msg.data === 'string') term.write(msg.data)
        break
      case 'login': {
        const r = msg.data as WSLoginResult
        if (r && !r.success) {
          const m = r.message ?? 'unknown'
          term.writeln(`\x1b[31m登录失败：${m}\x1b[0m`)
          handleError(`登录失败：${m}`)
          markFailed() // 替代 close()，设置 first_failed/reconnect_failed
        } else if (r && r.success) {
          markLoginSuccess()
          if (r.reattached) {
            term.writeln(`\x1b[32m[已恢复之前的会话]\x1b[0m`)
          }
        }
        break
      }
      // ... error, pong 不变
    }
  },
  // ... onBinary 不变
})
// ...
return { term, status }
```

#### 3. `frontend/src/views/TerminalTab.vue` — UI 细分展示

**变更内容**：
- 引入 `ConnectionStatus` 类型，用 `status` ref 替代 `connected` ref
- 直接引用 `useTerminal` 返回的 `status` ref（修复 L4：不再值拷贝+watch，避免首帧过期）
- **L5-L7 覆盖层**：根据 status 显示不同提示文本
- **L9-L13 工具栏**：根据 status 显示不同状态文本和颜色
- 新增 CSS 类：`.status.connecting`（黄色）、`.status.first_failed`（红色）、`.status.reconnecting`（黄色）、`.status.reconnect_failed`（红色）、`.status.disconnected`（灰色）

**模板变更**（[TerminalTab.vue#L1-L14](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/views/TerminalTab.vue#L1-L14)）：
```vue
<template>
  <div class="terminal-tab">
    <div class="terminal-wrapper">
      <div ref="terminalContainer" class="terminal-container"></div>
      <div v-if="node && showOverlay" class="terminal-overlay">
        <span class="overlay-text">{{ overlayText }}</span>
      </div>
    </div>
    <div class="terminal-toolbar">
      <span class="terminal-info">{{ node ? `${node.name} (${node.host}:${node.port})` : t('xtermPanel.idle') }}</span>
      <span :class="['status', statusClass]">{{ statusText }}</span>
    </div>
  </div>
</template>
```

**脚本变更**（[TerminalTab.vue#L17-L64](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/views/TerminalTab.vue#L17-L64)）：
```typescript
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useTerminal, getTerminalTheme } from '@/composables/useTerminal'
import type { ConnectionStatus } from '@/composables/useWebSocket'

const props = defineProps<{ node: ApiNode }>()
const terminalContainer = ref<HTMLElement | null>(null)
const status = ref<ConnectionStatus>('idle')
let standaloneTerm: Terminal | null = null
let cleanup: (() => void) | null = null

// 覆盖层仅在非 connected/idle/disconnected 状态显示
const showOverlay = computed(() =>
  ['connecting', 'first_failed', 'reconnecting', 'reconnect_failed'].includes(status.value)
)

const overlayText = computed(() => {
  const map: Record<string, string> = {
    connecting: t('xtermPanel.connecting'),
    first_failed: t('xtermPanel.firstFailed'),
    reconnecting: t('xtermPanel.reconnecting'),
    reconnect_failed: t('xtermPanel.reconnectFailed'),
  }
  return map[status.value] ?? ''
})

const statusText = computed(() => {
  const map: Record<string, string> = {
    idle: t('xtermPanel.idle'),
    connecting: t('xtermPanel.connecting'),
    connected: t('finder.connected'),
    first_failed: t('xtermPanel.firstFailed'),
    reconnecting: t('xtermPanel.reconnecting'),
    reconnect_failed: t('xtermPanel.reconnectFailed'),
    disconnected: t('finder.disconnected'),
  }
  return map[status.value] ?? ''
})

const statusClass = computed(() => {
  const map: Record<string, string> = {
    connecting: 'connecting',
    connected: 'connected',
    first_failed: 'failed',
    reconnecting: 'connecting',
    reconnect_failed: 'failed',
    disconnected: 'disconnected',
  }
  return map[status.value] ?? ''
})

onMounted(() => {
  if (!terminalContainer.value) return
  if (!props.node) {
    standaloneTerm = new Terminal({ cursorBlink: true, fontSize: 14, theme: getTerminalTheme() })
    standaloneTerm.open(terminalContainer.value)
    standaloneTerm.writeln(generateGreenText(t('xtermPanel.hello')))
    return
  }
  const { status: wsStatus } = useTerminal(terminalContainer.value, props.node)
  // 直接引用 ref，避免值拷贝导致的首帧过期（修复 L4）
  cleanup = watch(wsStatus, (val) => { status.value = val }, { immediate: true })
})
```

**CSS 变更**（[TerminalTab.vue#L66-L140](file:///c:/Users/Administrator/Documents/codes/managi/managi-v3/frontend/src/views/TerminalTab.vue#L66-L140)）：
```css
/* 新增状态样式 */
.status.connecting { color: var(--color-yellow); }
.status.connected { color: var(--color-green); }
.status.failed { color: var(--color-red); }
.status.disconnected { color: var(--color-font-3); }
/* 保留原 .status.connected 和 .status.disconnected */
```

#### 4. `frontend/src/locales/zh.json` — 中文 i18n

**变更内容**：在 `xtermPanel` 节点新增 4 个 key

```json
"xtermPanel": {
    "connecting": "正在连接…",
    "firstFailed": "连接失败",
    "reconnecting": "正在重连…",
    "reconnectFailed": "重连失败",
    "hello": "...",
    "idle": "未连接节点",
    "reattached": "已恢复之前的会话",
    "reconnecting": "连接断开，正在重连…"
}
```

注意：`reconnecting` key 已存在（值为"连接断开，正在重连…"），需更新为"正在重连…"。新增 `connecting`、`firstFailed`、`reconnectFailed` 三个 key。

#### 5. `frontend/src/locales/en.json` — 英文 i18n

```json
"xtermPanel": {
    "connecting": "Connecting…",
    "firstFailed": "Connection failed",
    "reconnecting": "Reconnecting…",
    "reconnectFailed": "Reconnect failed",
    "hello": "...",
    "idle": "No node connected",
    "reattached": "Previous session restored",
    "reconnecting": "Connection lost, reconnecting…"
}
```

同上，更新 `reconnecting` 值并新增 3 个 key。

---

## 假设与决策

1. **`useSFTP` 不改动 UI**：用户仅要求 TerminalTab 细分。`useWebSocket` 改动保持向后兼容（`connected` computed 保留），`useSFTP` 可继续使用 `connected`。后续如需 SFTP 状态细分，只需在 Finder.vue 中使用 `status` 即可。
2. **`markFailed` vs `close`**：登录失败时调 `markFailed()`（设失败状态 + 关 WS），组件卸载时调 `close()`（设 disconnected 状态）。两者都设 `manualClose = true` 阻止重连，区别在于最终状态值。
3. **`hasLoginSucceeded` 标志**：由 `useTerminal` 在收到登录成功消息时调 `markLoginSuccess()` 设置。此标志区分"首次连接"与"重连"，决定 `markFailed` 和"重连耗尽"时设 `first_failed` 还是 `reconnect_failed`。
4. **覆盖层显示策略**：仅在 `connecting`/`first_failed`/`reconnecting`/`reconnect_failed` 4 种状态显示覆盖层。`connected` 不显示，`disconnected`（组件卸载）不显示，`idle`（无节点）不显示。
5. **L4 修复**：通过 `watch(wsStatus, ..., { immediate: true })` 替代值拷贝，确保首帧即同步，修复原 L4 问题（初始状态可能过期）。原 L4 项在此计划中一并解决。
6. **`disconnected` 状态**：组件卸载时由 `close()` 设置。由于组件即将销毁，此状态实际上不会被用户看到，但保留它使状态模型完整。

---

## 验证步骤

1. **类型检查**：`cd frontend && npm run type-check` — 确保无类型错误
2. **单元测试**：`cd frontend && npm run test` — 确保现有测试通过
3. **构建**：`cd frontend && npm run build` — 确保构建成功
4. **手动验证场景**：
   - 打开终端 tab → 应显示"正在连接…"（黄色），连接成功后显示"已连接"（绿色）
   - 输入错误密码的节点 → 应显示"连接失败"（红色），覆盖层显示"连接失败"
   - 连接成功后断网 → 应显示"正在重连…"（黄色），覆盖层显示"正在重连…"
   - 重连耗尽 → 应显示"重连失败"（红色），覆盖层显示"重连失败"
   - 关闭终端 tab → 无报错，无内存泄漏
