import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { useWebSocket } from '@/composables/useWebSocket'

// Mock WebSocket 类：happy-dom 不提供 WebSocket
class MockWebSocket {
  static instances: MockWebSocket[] = []
  static LAST: MockWebSocket | null = null
  url: string
  binaryType: 'blob' | 'arraybuffer' = 'blob'
  readyState: number = 0 // CONNECTING
  onopen: ((ev: Event) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onclose: ((ev: CloseEvent) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  sent: (string | ArrayBuffer)[] = []
  closed = false

  static OPEN = 1
  static CLOSED = 3

  constructor(url: string) {
    this.url = url
    MockWebSocket.instances.push(this)
    MockWebSocket.LAST = this
  }
  send(data: string | ArrayBuffer) {
    this.sent.push(data)
  }
  close() {
    this.closed = true
    this.readyState = 3
  }

  // 测试辅助方法
  fireOpen() {
    this.readyState = 1
    this.onopen?.(new Event('open'))
  }
  fireText(data: string) {
    this.onmessage?.({ data } as MessageEvent)
  }
  fireBinary(data: ArrayBuffer) {
    this.onmessage?.({ data } as MessageEvent)
  }
  fireClose() {
    this.readyState = 3
    this.onclose?.(new CloseEvent('close'))
  }
}

// withSetup：在组件上下文中调用 composable（onUnmounted 必须在 setup 中调用）
function withSetup<T>(composable: () => T): { result: T; unmount: () => void } {
  let result!: T
  const App = defineComponent({
    setup() {
      result = composable()
      return () => h('div')
    },
  })
  const wrapper = mount(App)
  return { result, unmount: () => wrapper.unmount() }
}

describe('useWebSocket', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    MockWebSocket.instances = []
    MockWebSocket.LAST = null
    vi.stubGlobal('WebSocket', MockWebSocket)
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('connect() creates WebSocket with URL derived from path', () => {
    const { result } = withSetup(() => useWebSocket('/ws/sftp'))
    result.connect()
    expect(MockWebSocket.LAST).not.toBeNull()
    expect(MockWebSocket.LAST!.url).toMatch(/^ws:\/\/.+\/ws\/sftp$/)
  })

  it('onopen fires onOpen and sends authPayload, but connected only after markLoginSuccess', () => {
    const onOpen = vi.fn()
    const auth = { host: '1.2.3.4', port: 22 }
    const { result } = withSetup(() => useWebSocket('/ws/sftp', { authPayload: JSON.stringify(auth), onOpen }))
    result.connect()
    expect(result.connected.value).toBe(false)
    MockWebSocket.LAST!.fireOpen()
    // onopen 不再置 connected：需收到登录 ack（调用方调 markLoginSuccess）才置位
    expect(result.connected.value).toBe(false)
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(MockWebSocket.LAST!.sent[0]).toBe(JSON.stringify(auth))
    result.markLoginSuccess()
    expect(result.connected.value).toBe(true)
  })

  it('onmessage text invokes onText, binary invokes onBinary', () => {
    const onText = vi.fn()
    const onBinary = vi.fn()
    const { result } = withSetup(() => useWebSocket('/ws', { onText, onBinary }))
    result.connect()
    MockWebSocket.LAST!.fireOpen()
    MockWebSocket.LAST!.fireText('hello')
    MockWebSocket.LAST!.fireBinary(new ArrayBuffer(4))
    expect(onText).toHaveBeenCalledWith('hello')
    expect(onBinary).toHaveBeenCalledTimes(1)
    expect(onBinary.mock.calls[0][0]).toBeInstanceOf(ArrayBuffer)
  })

  it('send() returns false and skips when not OPEN, returns true when OPEN', () => {
    const { result } = withSetup(() => useWebSocket('/ws'))
    result.connect()
    // 未 fireOpen 前 readyState=0，send 返回 false 且不投递
    expect(result.send('before-open')).toBe(false)
    expect(MockWebSocket.LAST!.sent).toHaveLength(0)
    MockWebSocket.LAST!.fireOpen()
    expect(result.send('after-open')).toBe(true)
    expect(MockWebSocket.LAST!.sent).toEqual(['after-open'])
  })

  it('close() prevents reconnect and does not fire onClose', () => {
    const onClose = vi.fn()
    const { result } = withSetup(() => useWebSocket('/ws', { onClose }))
    result.connect()
    MockWebSocket.LAST!.fireOpen()
    result.close()
    expect(result.connected.value).toBe(false)
    // close() 已将 ws.onclose 置 null，模拟底层事件不再触发回调或重连
    MockWebSocket.LAST!.fireClose()
    expect(onClose).not.toHaveBeenCalled()
    expect(MockWebSocket.instances).toHaveLength(1)
  })

  it('reconnects with exponential backoff on unexpected close, stops at maxReconnect', async () => {
    // 修复 B21：Math.random 被 mock 为 0，消除 jitter 对定时的干扰
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const onClose = vi.fn()
    const { result } = withSetup(() => useWebSocket('/ws', { maxReconnect: 2, onClose }))
    result.connect()
    MockWebSocket.LAST!.fireOpen()
    // 第 1 次意外关闭：reconnectAttempts=0 → 安排 1000ms 后重连，reconnectAttempts=1
    MockWebSocket.LAST!.fireClose()
    expect(result.connected.value).toBe(false)
    await vi.advanceTimersByTimeAsync(999)
    expect(MockWebSocket.instances).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(MockWebSocket.instances).toHaveLength(2)
    // 第 2 次意外关闭：reconnectAttempts=1 → 安排 2000ms 后重连，reconnectAttempts=2
    MockWebSocket.LAST!.fireClose()
    await vi.advanceTimersByTimeAsync(1999)
    expect(MockWebSocket.instances).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(MockWebSocket.instances).toHaveLength(3)
    // 第 3 次关闭：reconnectAttempts=2 已达 maxReconnect → 调用 onClose，不再重连
    MockWebSocket.LAST!.fireClose()
    expect(onClose).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(30000)
    expect(MockWebSocket.instances).toHaveLength(3)
  })

  // 修复 B21：验证重连延迟包含 jitter（base + 0~500ms 随机抖动）
  it('reconnect delay includes random jitter (B21 fix)', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5) // jitter = 250ms
    const { result } = withSetup(() => useWebSocket('/ws', { maxReconnect: 1 }))
    result.connect()
    MockWebSocket.LAST!.fireOpen()
    MockWebSocket.LAST!.fireClose()
    // base=1000 + jitter=250 = 1250ms
    await vi.advanceTimersByTimeAsync(1249)
    expect(MockWebSocket.instances).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(MockWebSocket.instances).toHaveLength(2)
  })

  // 修复 B25：onerror 在已登录状态下更新为 reconnecting
  it('onerror updates status from connected to reconnecting (B25 fix)', () => {
    const { result } = withSetup(() => useWebSocket('/ws'))
    result.connect()
    MockWebSocket.LAST!.fireOpen()
    // 先标记登录成功（onopen 不再置 connected）
    result.markLoginSuccess()
    expect(result.status.value).toBe('connected')
    // 模拟 onerror（onclose 之前触发）
    MockWebSocket.LAST!.onerror!(new Event('error'))
    expect(result.status.value).toBe('reconnecting')
  })

  it('close() cancels pending reconnect timer (A9 fix)', async () => {
    // N4：验证 close() 在已调度重连定时器后调用会 clearTimeout，避免连接"复活"
    const { result } = withSetup(() => useWebSocket('/ws', { maxReconnect: 5 }))
    result.connect()
    MockWebSocket.LAST!.fireOpen()
    // 意外关闭 → 调度 1000ms 后重连
    MockWebSocket.LAST!.fireClose()
    expect(MockWebSocket.instances).toHaveLength(1)
    // 在重连触发前调用 close()，应取消定时器
    result.close()
    // 推进足够长时间，验证无新 WebSocket 创建
    await vi.advanceTimersByTimeAsync(60000)
    expect(MockWebSocket.instances).toHaveLength(1)
  })

  it('onUnmounted triggers close() on component unmount', () => {
    const { result, unmount } = withSetup(() => useWebSocket('/ws'))
    result.connect()
    MockWebSocket.LAST!.fireOpen()
    expect(MockWebSocket.LAST!.closed).toBe(false)
    unmount()
    expect(MockWebSocket.LAST!.closed).toBe(true)
    expect(result.connected.value).toBe(false)
  })

  it('fires onReconnectFailed before onClose when reconnects exhausted (T3 fix)', async () => {
    // 修复 B21：Math.random 被 mock 为 0，消除 jitter 对定时的干扰
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const onReconnectFailed = vi.fn()
    const onClose = vi.fn()
    const { result } = withSetup(() => useWebSocket('/ws', { maxReconnect: 1, onReconnectFailed, onClose }))
    result.connect()
    MockWebSocket.LAST!.fireOpen()
    // 第 1 次关闭：reconnectAttempts=0 → 安排 1000ms 后重连，reconnectAttempts=1
    MockWebSocket.LAST!.fireClose()
    await vi.advanceTimersByTimeAsync(1000)
    expect(MockWebSocket.instances).toHaveLength(2)
    // 第 2 次关闭：reconnectAttempts=1 已达 maxReconnect → 先调 onReconnectFailed 再调 onClose
    MockWebSocket.LAST!.fireClose()
    expect(onReconnectFailed).toHaveBeenCalledTimes(1)
    expect(onClose).toHaveBeenCalledTimes(1)
    // 验证调用顺序：onReconnectFailed 在 onClose 之前
    expect(onReconnectFailed.mock.invocationCallOrder[0]).toBeLessThan(onClose.mock.invocationCallOrder[0])
    // 不再重连
    await vi.advanceTimersByTimeAsync(30000)
    expect(MockWebSocket.instances).toHaveLength(2)
  })

  // 响应超时：发出数据（登录帧/输入/ping）后 10s 内未收到任何消息则判定连接已死并重连
  it('response timeout triggers reconnect when no message received after send', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const { result } = withSetup(() => useWebSocket('/ws', { authPayload: '{"host":"x"}', maxReconnect: 1 }))
    result.connect()
    MockWebSocket.LAST!.fireOpen() // 发送 authPayload，启动 10s 响应超时
    expect(MockWebSocket.instances).toHaveLength(1)
    // 10s 内无任何消息 → 超时触发重连调度（1s 退避后新建 WS）
    await vi.advanceTimersByTimeAsync(11000)
    expect(MockWebSocket.instances).toHaveLength(2)
    expect(result.status.value).toBe('reconnecting')
  })

  it('receiving any message clears response timeout, no reconnect', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0)
    const onText = vi.fn()
    const { result } = withSetup(() => useWebSocket('/ws', { authPayload: '{"host":"x"}', onText }))
    result.connect()
    MockWebSocket.LAST!.fireOpen() // 启动 10s 响应超时
    // 9s 时收到消息 → 清除响应超时
    await vi.advanceTimersByTimeAsync(9000)
    MockWebSocket.LAST!.fireText('{"type":"pong"}')
    expect(onText).toHaveBeenCalledTimes(1)
    // 继续推进超过 10s，不应触发重连（无活跃计时器，心跳 30s 未到）
    await vi.advanceTimersByTimeAsync(11000)
    expect(MockWebSocket.instances).toHaveLength(1)
  })
})
