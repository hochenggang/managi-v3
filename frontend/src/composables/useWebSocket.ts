// useWebSocket：WebSocket 连接管理 composable。
// 修复 v2 缺陷：心跳失效（改用原生 Ping/Pong）+ 网络重试（指数退避重连）。
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
        setTimeout(doConnect, delay)
      } else {
        opts.onClose?.()
      }
    }

    ws.onerror = () => {
      stopHeartbeat()
    }
  }

  // v3 心跳：使用原生 WebSocket Ping/Pong（不污染业务数据，修正 v2 的 \x00 心跳）。
  // 浏览器无法主动发 Ping 帧，改用定时发送空 Pong 帧或依赖服务端 Ping。
  // 实际实现依赖服务端 SetPongHandler + SetReadDeadline（见后端 handler/terminal.go）。
  function startHeartbeat(): void {
    const interval = opts.heartbeatInterval ?? 30000
    stopHeartbeat()
    // TODO(P0): 浏览器无 ws.ping() API；通过定时发心跳文本帧由服务端识别，
    //   或依赖服务端主动 Ping（推荐）。当前占位。
    heartbeatTimer = setInterval(() => {
      if (ws?.readyState === WebSocket.OPEN) {
        // 服务端 Ping 模式下此处可空操作
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

/** 获取 WS host，与 v2 getWsUrl 一致。 */
function getWsHost(): string {
  const stored = localStorage.getItem('managi-api-host')
  if (stored) return stored
  if (location.protocol === 'https:') return location.hostname
  return location.hostname + ':' + location.port
}
