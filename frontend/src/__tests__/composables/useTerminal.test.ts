import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import type { ApiNode } from '@/protocol/types'

// vi.hoisted 确保 mock 在 vi.mock 工厂执行前已初始化
const { mockHandleError } = vi.hoisted(() => ({
  mockHandleError: vi.fn(),
}))

vi.mock('@/helper', () => ({
  handleError: mockHandleError,
  handleMsg: vi.fn(),
}))

// 捕获 Terminal 实例回调
let onDataCb: ((data: string) => void) | null = null
let onSelectionChangeCb: (() => void) | null = null
const mockTerminal = {
  loadAddon: vi.fn(),
  open: vi.fn(),
  write: vi.fn(),
  writeln: vi.fn(),
  onData: vi.fn((cb: (data: string) => void) => {
    onDataCb = cb
    return { dispose: vi.fn() }
  }),
  onSelectionChange: vi.fn((cb: () => void) => {
    onSelectionChangeCb = cb
    return { dispose: vi.fn() }
  }),
  dispose: vi.fn(),
  focus: vi.fn(),
  getSelection: vi.fn(() => ''),
  cols: 80,
  rows: 24,
}

// 注意：构造函数必须用普通 function（箭头函数不能 new），且返回对象时 new 会用该返回值。
vi.mock('@xterm/xterm', () => ({
  Terminal: function MockTerminal() {
    return mockTerminal
  },
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: function MockFitAddon() {
    return { fit: vi.fn() }
  },
}))

// 捕获 useWebSocket 回调与返回值（与 useSFTP.test.ts 同模式）
let onTextCb: ((data: string) => void) | null = null
let onBinaryCb: ((data: ArrayBuffer) => void) | null = null
const mockSend = vi.fn()
const mockConnect = vi.fn()
const mockClose = vi.fn()

vi.mock('@/composables/useWebSocket', () => ({
  useWebSocket: (_path: string, opts: any) => {
    onTextCb = opts.onText
    onBinaryCb = opts.onBinary
    return {
      connected: { value: false },
      connect: mockConnect,
      send: mockSend,
      close: mockClose,
    }
  },
}))

import { useTerminal } from '@/composables/useTerminal'
import { wsMessage } from '@/protocol/ws'
import { inputMessage } from '@/protocol/terminal'

const node: ApiNode = {
  name: 'n1',
  host: '1.2.3.4',
  port: 22,
  username: 'root',
  auth_type: 'password',
  auth_value: 'pwd',
}

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

describe('useTerminal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    onDataCb = null
    onSelectionChangeCb = null
    onTextCb = null
    onBinaryCb = null
  })

  it('mount: creates Terminal, opens in container, loads FitAddon, calls connect', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    expect(mockTerminal.open).toHaveBeenCalledWith(container)
    expect(mockTerminal.loadAddon).toHaveBeenCalledTimes(1)
    expect(mockTerminal.focus).toHaveBeenCalledTimes(1)
    expect(mockConnect).toHaveBeenCalledTimes(1)
  })

  it('onText msg type writes data to terminal', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    onTextCb!(wsMessage('msg', 'hello world'))
    expect(mockTerminal.write).toHaveBeenCalledWith('hello world')
  })

  it('onText non-msg types (resize/ping/pong) do NOT write to terminal', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    mockTerminal.write.mockClear()
    onTextCb!(wsMessage('resize', { cols: 120, rows: 40 }))
    onTextCb!(wsMessage('ping'))
    onTextCb!(wsMessage('pong'))
    expect(mockTerminal.write).not.toHaveBeenCalled()
  })

  it('onText login failure writes formatted error, calls handleError and close', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    mockTerminal.writeln.mockClear()
    onTextCb!(wsMessage('login', { success: false, message: 'auth failed' }))
    expect(mockTerminal.writeln).toHaveBeenCalledWith(expect.stringContaining('登录失败：auth failed'))
    expect(mockHandleError).toHaveBeenCalledWith('登录失败：auth failed')
    expect(mockClose).toHaveBeenCalledTimes(1)
  })

  it('onText error type writes formatted error to terminal', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    mockTerminal.writeln.mockClear()
    onTextCb!(wsMessage('error', { message: 'boom' }))
    expect(mockTerminal.writeln).toHaveBeenCalledWith(expect.stringContaining('错误：boom'))
  })

  it('onText non-JSON data is ignored', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    mockTerminal.write.mockClear()
    onTextCb!('not json')
    expect(mockTerminal.write).not.toHaveBeenCalled()
  })

  it('onBinary writes UTF-8 decoded data to terminal', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    const buf = new TextEncoder().encode('binary text')
    onBinaryCb!(buf.buffer)
    expect(mockTerminal.write).toHaveBeenCalledWith('binary text')
  })

  it('term.onData forwards input as inputMessage envelope', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    onDataCb!('ls -la\n')
    expect(mockSend).toHaveBeenCalledWith(inputMessage('ls -la\n'))
  })

  it('onUnmounted: calls close() and term.dispose()', () => {
    const container = document.createElement('div')
    const { unmount } = withSetup(() => useTerminal(container, node))
    expect(mockClose).not.toHaveBeenCalled()
    expect(mockTerminal.dispose).not.toHaveBeenCalled()
    unmount()
    expect(mockClose).toHaveBeenCalledTimes(1)
    expect(mockTerminal.dispose).toHaveBeenCalledTimes(1)
  })
})
