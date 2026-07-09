// useWebSocket：WebSocket 连接管理 composable。
// v3 协议：统一 {type, data} envelope。心跳发 {type:"ping"}，重连上限默认 3 次。
// 设计见 ../../../design-v3.md §6.2 §6.3。

import { ref, onUnmounted } from 'vue'
import { wsMessage } from '@/protocol/ws'
import { getApiBase } from '@/api'

export interface WSOptions {
  /** 首包认证消息（已序列化字符串，建立后立即发送）。 */
  authPayload?: string
  /** 心跳间隔毫秒，默认 30000。 */
  heartbeatInterval?: number
  /** 最大重连次数，默认 3。登录失败由调用方在 onText 中调用 close() 抑制重连。 */
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
  const connected = ref(false)
  let ws: WebSocket | null = null
  let heartbeatTimer: ReturnType<typeof setInterval> | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectAttempts = 0
  let manualClose = false
  let pageHidden = false

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
    doConnect()
  }

  function doConnect(): void {
    ws = new WebSocket(url)
    ws.binaryType = 'arraybuffer'

    ws.onopen = () => {
      connected.value = true
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
      connected.value = false
      stopHeartbeat()
      // 日志记录关闭原因，便于排查后台重连问题
      console.warn('[useWebSocket] closed', ev.code, ev.reason)
      if (!manualClose && reconnectAttempts < (opts.maxReconnect ?? 3)) {
        const delay = Math.min(1000 * 2 ** reconnectAttempts, 16000)
        reconnectAttempts++
        reconnectTimer = setTimeout(doConnect, delay)
      } else {
        // 修复 T3：重连耗尽时通知调用方做可视化提示，避免用户体感「卡住」
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
    connected.value = false
  }

  onUnmounted(close)

  return { connected, connect, send, close }
}

/** 获取 WS host，https 保留非默认端口（修复 A8：wss 部署在 8443 丢端口）。
 *  修复 R5：复用 api.ts 的 getApiBase，去除重复 host/port 推导逻辑。
 */
function getWsHost(): string {
  return getApiBase().replace(/^https?:\/\//, '')
}
