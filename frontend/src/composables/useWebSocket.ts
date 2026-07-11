// useWebSocket：WebSocket 连接管理 composable。
// v3 协议：统一 {type, data} envelope。心跳发 {type:"ping"}，重连上限默认 3 次。
// 设计见 ../../../design-v3.md §6.2 §6.3。

import { ref, computed, onUnmounted } from 'vue'
import { wsMessage } from '@/protocol/ws'
import { getApiBase } from '@/api'

/** 连接状态：细分首次连接与重连各阶段，供 UI 精确展示。 */
export type ConnectionStatus =
  | 'idle'               // 初始未连接（无节点时）
  | 'connecting'         // 首次连接进行中
  | 'connected'          // 已连接（首次成功或重连成功）
  | 'first_failed'       // 首次连接失败（登录失败等，不重试）
  | 'reconnecting'       // 重连进行中
  | 'reconnect_failed'   // 重连失败（达到最大重试次数或重连后登录失败）
  | 'disconnected'       // 主动断开（组件卸载）

export interface WSOptions {
  /** 首包认证消息（已序列化字符串，建立后立即发送）。 */
  authPayload?: string
  /** 心跳间隔毫秒，默认 30000。 */
  heartbeatInterval?: number
  /** 最大重连次数，默认 3。登录失败由调用方在 onText 中调用 markFailed() 抑制重连。 */
  maxReconnect?: number
  /** 收到文本消息回调。 */
  onText?: (data: string) => void
  /** 收到二进制回调。 */
  onBinary?: (data: ArrayBuffer) => void
  /** 连接就绪回调。 */
  onOpen?: () => void
  /** 连接关闭回调（重连耗尽后触发）。 */
  onClose?: () => void
  /** 重连耗尽回调（修复 T3：供调用方做可视化提示，避免用户体感「卡住」）。 */
  onReconnectFailed?: () => void
}

export function useWebSocket(path: string, opts: WSOptions = {}) {
  const status = ref<ConnectionStatus>('idle')
  let ws: WebSocket | null = null
  let heartbeatTimer: ReturnType<typeof setInterval> | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectAttempts = 0
  let manualClose = false
  let pageHidden = false
  // 应用层登录是否曾成功：区分首次连接与重连，决定失败时设 first_failed 还是 reconnect_failed
  let hasLoginSucceeded = false

  const wsHost = getWsHost()
  const url = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${wsHost}${path}`

  function handleVisibilityChange(): void {
    pageHidden = document.hidden
    if (!pageHidden && ws?.readyState === WebSocket.OPEN) {
      // 切回前台立即补发一次 ping，并重置心跳定时器
      ws.send(wsMessage('ping'))
      startHeartbeat()
    }
  }
  document.addEventListener('visibilitychange', handleVisibilityChange)

  function connect(): void {
    manualClose = false
    reconnectAttempts = 0
    status.value = hasLoginSucceeded ? 'reconnecting' : 'connecting'
    doConnect()
  }

  function doConnect(): void {
    ws = new WebSocket(url)
    ws.binaryType = 'arraybuffer'

    ws.onopen = () => {
      status.value = 'connected'
      reconnectAttempts = 0
      if (opts.authPayload !== undefined) {
        ws?.send(opts.authPayload)
      }
      startHeartbeat()
      opts.onOpen?.()
    }

    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        opts.onText?.(ev.data)
      } else {
        opts.onBinary?.(ev.data as ArrayBuffer)
      }
    }

    ws.onclose = (ev: CloseEvent) => {
      stopHeartbeat()
      // 日志记录关闭原因，便于排查后台重连问题
      console.warn('[useWebSocket] closed', ev.code, ev.reason)
      if (manualClose) return // 状态已由 close()/markFailed() 设置
      if (reconnectAttempts < (opts.maxReconnect ?? 3)) {
        status.value = 'reconnecting'
        const delay = Math.min(1000 * 2 ** reconnectAttempts, 16000)
        reconnectAttempts++
        reconnectTimer = setTimeout(doConnect, delay)
      } else {
        // 修复 T3：重连耗尽时通知调用方做可视化提示，避免用户体感「卡住」
        status.value = hasLoginSucceeded ? 'reconnect_failed' : 'first_failed'
        opts.onReconnectFailed?.()
        opts.onClose?.()
      }
    }

    ws.onerror = () => {
      stopHeartbeat()
    }
  }

  // v3 心跳：定时发 {type:"ping"} 文本帧，服务端回 {type:"pong"}。
  // 浏览器无法主动发 WS 协议级 Ping 控制帧，改用业务层 ping/pong（design-v3.md §6.3）。
  function startHeartbeat(): void {
    const interval = opts.heartbeatInterval ?? 30000
    stopHeartbeat()
    heartbeatTimer = setInterval(() => {
      // 标签页在后台时 JS 定时器会被节流，跳过发送；依赖服务端控制帧 Ping 保活
      if (pageHidden) return
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(wsMessage('ping'))
      }
    }, interval)
  }

  function stopHeartbeat(): void {
    if (heartbeatTimer) {
      clearInterval(heartbeatTimer)
      heartbeatTimer = null
    }
  }

  /** 发送数据，仅在 OPEN 时发送。返回是否成功投递。 */
  function send(data: string | ArrayBuffer): boolean {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(data)
      return true
    }
    return false
  }

  /** markLoginSuccess 标记应用层登录成功，用于区分首次连接与重连。
   *  调用方在收到登录成功消息时调用。
   */
  function markLoginSuccess(): void {
    hasLoginSucceeded = true
  }

  /** markFailed 标记连接失败（如登录失败），设置 first_failed 或 reconnect_failed 并关闭 WS。
   *  与 close() 的区别：close 设 disconnected（组件卸载），markFailed 设失败状态（用户可见）。
   */
  function markFailed(): void {
    manualClose = true
    stopHeartbeat()
    if (reconnectTimer) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    status.value = hasLoginSucceeded ? 'reconnect_failed' : 'first_failed'
    if (ws) {
      ws.onclose = null
      ws.onerror = null
      ws.close()
      ws = null
    }
  }

  function close(): void {
    manualClose = true
    stopHeartbeat()
    // 清理待执行的重连定时器，避免 close 后连接"复活"（修复 A9）
    if (reconnectTimer) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    if (ws) {
      ws.onclose = null
      ws.onerror = null
      ws.close()
      ws = null
    }
    status.value = 'disconnected'
  }

  // 向后兼容：useSFTP 等仍可使用 connected 布尔值
  const connected = computed(() => status.value === 'connected')

  onUnmounted(close)

  return { status, connected, connect, send, close, markFailed, markLoginSuccess }
}

/** 获取 WS host，https 保留非默认端口（修复 A8：wss 部署在 8443 丢端口）。
 *  修复 R5：复用 api.ts 的 getApiBase，去除重复 host/port 推导逻辑。
 */
function getWsHost(): string {
  return getApiBase().replace(/^https?:\/\//, '')
}
