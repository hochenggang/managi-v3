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

  it('onopen flips connected, fires onOpen, and sends authPayload JSON', () => {
    const onOpen = vi.fn()
    const auth = { host: '1.2.3.4', port: 22 }
    const { result } = withSetup(() => useWebSocket('/ws/sftp', { authPayload: auth, onOpen }))
    result.connect()
    expect(result.connected.value).toBe(false)
    MockWebSocket.LAST!.fireOpen()
    expect(result.connected.value).toBe(true)
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(MockWebSocket.LAST!.sent[0]).toBe(JSON.stringify(auth))
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

  it('send() forwards to ws.send only when OPEN', () => {
    const { result } = withSetup(() => useWebSocket('/ws'))
    result.connect()
    // 未 fireOpen 前 readyState=0，send 应静默跳过
    result.send('before-open')
    expect(MockWebSocket.LAST!.sent).toHaveLength(0)
    MockWebSocket.LAST!.fireOpen()
    result.send('after-open')
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
})
