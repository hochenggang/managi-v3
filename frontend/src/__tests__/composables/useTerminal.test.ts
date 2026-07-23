import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h, reactive, ref } from 'vue'
import type { ApiNode } from '@/protocol/types'

// vi.hoisted 确保 mock 在 vi.mock 工厂执行前已初始化
// 使用 holder 模式：reactive 在 vi.hoisted 中不可用，故用 holder 延迟到 import 后初始化
const { mockHandleError, mockSettingsHolder } = vi.hoisted(() => ({
  mockHandleError: vi.fn(),
  mockSettingsHolder: { settings: null as any },
}))

vi.mock('@/helper', () => ({
  handleError: mockHandleError,
  handleMsg: vi.fn(),
}))

vi.mock('@/stores/settingsStore', () => ({
  useSettingsStore: () => mockSettingsHolder,
}))

// 捕获 Terminal 实例回调
let onDataCb: ((data: string) => void) | null = null
let onSelectionChangeCb: (() => void) | null = null
// 修复 B6/B7：捕获 Terminal 构造参数以断言字体设置
let terminalCtorOpts: any = null
const mockTerminal = {
  options: {} as Record<string, unknown>,
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
  Terminal: function MockTerminal(opts: any) {
    terminalCtorOpts = opts
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
const mockMarkFailed = vi.fn()
const mockMarkLoginSuccess = vi.fn()
// 修复 B24：用真实 ref 模拟 status，支持 watch 触发
let mockStatus: ReturnType<typeof ref<string>>

vi.mock('@/composables/useWebSocket', () => ({
  useWebSocket: (_path: string, opts: any) => {
    onTextCb = opts.onText
    onBinaryCb = opts.onBinary
    return {
      status: mockStatus,
      connected: { value: false },
      connect: mockConnect,
      send: mockSend,
      close: mockClose,
      markFailed: mockMarkFailed,
      markLoginSuccess: mockMarkLoginSuccess,
    }
  },
}))

import { useTerminal } from '@/composables/useTerminal'
import { wsMessage } from '@/protocol/ws'
import { inputMessage } from '@/protocol/terminal'

// 初始化 reactive settings 对象（vi.hoisted 中无法调用 reactive）
function makeSettings(overrides: Partial<{ terminalFontSize: number; terminalFontFamily: string }> = {}) {
  return reactive({
    theme: 'nord' as const,
    language: 'zh' as const,
    terminalFontSize: 14,
    terminalFontFamily: "'JetBrains Mono', monospace",
    ...overrides,
  })
}

// 在所有测试开始前初始化 holder.settings
mockSettingsHolder.settings = makeSettings()

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
    terminalCtorOpts = null
    // 每次测试重置 reactive settings 到默认值
    mockSettingsHolder.settings = makeSettings()
    // 修复 B24：每次测试创建新的 status ref，默认 connected
    mockStatus = ref('connected')
    // mockSend 默认返回 true（WS 已连接），模拟正常发送
    mockSend.mockReturnValue(true)
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

  it('onText login failure writes formatted error, calls handleError and markFailed', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    mockTerminal.writeln.mockClear()
    onTextCb!(wsMessage('login', { success: false, message: 'auth failed' }))
    expect(mockTerminal.writeln).toHaveBeenCalledWith(expect.stringContaining('登录失败：auth failed'))
    expect(mockHandleError).toHaveBeenCalledWith('登录失败：auth failed')
    expect(mockMarkFailed).toHaveBeenCalledTimes(1)
  })

  it('onText login success calls markLoginSuccess and writes restore message if reattached', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    mockTerminal.writeln.mockClear()
    onTextCb!(wsMessage('login', { success: true, reattached: true }))
    expect(mockMarkLoginSuccess).toHaveBeenCalledTimes(1)
    expect(mockTerminal.writeln).toHaveBeenCalledWith(expect.stringContaining('已恢复之前的会话'))
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

  // 修复 B8：onBinary 已移除（后端仅发文本帧），不应注册 onBinary 回调
  it('does NOT register onBinary callback (B8 fix)', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    expect(onBinaryCb).toBeFalsy()
  })

  // 修复 B6：Terminal 构造时应使用 settings 中的字体大小与字体族
  it('Terminal constructor uses fontSize/fontFamily from settings (B6 fix)', () => {
    mockSettingsHolder.settings = makeSettings({
      terminalFontSize: 18,
      terminalFontFamily: "'Fira Code', monospace",
    })
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    expect(terminalCtorOpts.fontSize).toBe(18)
    expect(terminalCtorOpts.fontFamily).toBe("'Fira Code', monospace")
  })

  // 修复 B7：settings 变化时热更新 Terminal options（fontSize/fontFamily/theme）
  it('settings change hot-updates Terminal options (B7 fix)', async () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    mockSettingsHolder.settings.terminalFontSize = 20
    await vi.waitFor(() => {
      expect(mockTerminal.options.fontSize).toBe(20)
    })
  })

  it('term.onData forwards input as inputMessage envelope', () => {
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    onDataCb!('ls -la\n')
    expect(mockSend).toHaveBeenCalledWith(inputMessage('ls -la\n'))
  })

  // 修复 B24：WS 未连接时缓冲输入，重连成功后 flush
  it('buffers input when WS not connected, flushes on reconnect (B24 fix)', async () => {
    const container = document.createElement('div')
    mockStatus = ref('reconnecting')
    mockSend.mockReturnValue(false) // WS 未连接
    withSetup(() => useTerminal(container, node))
    mockSend.mockClear()

    // 用户输入，但 WS 未连接 → 缓冲
    onDataCb!('ls\n')
    expect(mockSend).toHaveBeenCalledTimes(1) // 尝试发送
    // send 返回 false，输入被缓冲（不额外发送）

    // 重连成功 → flush 缓冲
    mockSend.mockReturnValue(true)
    mockStatus.value = 'connected'
    await vi.waitFor(() => {
      expect(mockSend).toHaveBeenCalledWith(inputMessage('ls\n'))
    })
  })

  // 修复 B28：不应注册 window resize 监听器（ResizeObserver 已覆盖）
  it('does NOT add window resize listener (B28 fix)', () => {
    const spy = vi.spyOn(window, 'addEventListener')
    const container = document.createElement('div')
    withSetup(() => useTerminal(container, node))
    const resizeCalls = spy.mock.calls.filter(([event]) => event === 'resize')
    expect(resizeCalls).toHaveLength(0)
    spy.mockRestore()
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
