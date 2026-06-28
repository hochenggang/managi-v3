// useWebSocket：WebSocket 连接管理 composable。
// 修复 v2 缺陷：心跳失效（文本 ping/pong）+ 网络重试（指数退避重连）。
// 设计见 ../../../design-v3.md §6.2 §6.3。

import { ref, onUnmounted } from 'vue'

export interface WSOptions {
  /** 首包认证消息（建立后立即发送，如节点 JSON）。 */
  authPayload?: unknown
  /** 心跳间隔毫秒，默认 30000。 */
  heartbeatInterval?: number
  /** 最大重连次数，默认 5。 */
  maxReconnect?: number
  /** 收到文本消息回调。 */
  onText?: (data: string) => void
  /** 收到二进制回调。 */
  onBinary?: (data: ArrayBuffer) => void
  /** 连接就绪回调。 */
  onOpen?: () => void
  /** 连接关闭回调。 */
  onClose?: () => void
}

export function useWebSocket(path: string, opts: WSOptions = {}) {
  const connected = ref(false)
  let ws: WebSocket | null = null
  let heartbeatTimer: ReturnType<typeof setInterval> | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectAttempts = 0
  let manualClose = false

  const wsHost = getWsHost()
  const url = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${wsHost}${path}`

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
        ws?.send(JSON.stringify(opts.authPayload))
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

    ws.onclose = () => {
      connected.value = false
      stopHeartbeat()
      if (!manualClose && reconnectAttempts < (opts.maxReconnect ?? 5)) {
        const delay = Math.min(1000 * 2 ** reconnectAttempts, 16000)
        reconnectAttempts++
        reconnectTimer = setTimeout(doConnect, delay)
      } else {
        opts.onClose?.()
      }
    }

    ws.onerror = () => {
      stopHeartbeat()
    }
  }

  // v3 心跳：定时发文本 ping 帧，服务端回 pong 刷新连接活跃状态。
  // 浏览器无法主动发 Ping 控制帧，改用业务层 ping/pong（design-v3.md §6.3）。
  function startHeartbeat(): void {
    const interval = opts.heartbeatInterval ?? 30000
    stopHeartbeat()
    heartbeatTimer = setInterval(() => {
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'ping' }))
      }
    }, interval)
  }

  function stopHeartbeat(): void {
    if (heartbeatTimer) {
      clearInterval(heartbeatTimer)
      heartbeatTimer = null
    }
  }

  function send(data: string | ArrayBuffer): void {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(data)
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

/** 获取 WS host，https 保留非默认端口（修复 A8：wss 部署在 8443 丢端口）。 */
function getWsHost(): string {
  const stored = localStorage.getItem('managi-api-host')
  if (stored) return stored
  const port = location.port
  if (location.protocol === 'https:') {
    return port && port !== '443' ? `${location.hostname}:${port}` : location.hostname
  }
  return port ? `${location.hostname}:${port}` : location.hostname
}
