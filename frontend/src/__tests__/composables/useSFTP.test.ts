import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import type { ApiNode } from '@/protocol/types'
import type { WSMessage } from '@/protocol/ws'
import { useSFTP } from '@/composables/useSFTP'

// vi.hoisted 确保 mock 在 vi.mock 工厂执行前已初始化
const { mockHandleError } = vi.hoisted(() => ({
  mockHandleError: vi.fn(),
}))

vi.mock('@/helper', () => ({
  handleError: mockHandleError,
  handleMsg: vi.fn(),
}))

const node: ApiNode = {
  name: 'n1',
  host: '1.2.3.4',
  port: 22,
  username: 'root',
  auth_type: 'password',
  auth_value: 'pwd',
}

// 捕获 useWebSocket 的回调与返回值
let onTextCb: ((data: string) => void) | null = null
let onBinaryCb: ((data: ArrayBuffer) => void) | null = null
// mockSend 必须返回 true，sendAndAwait 据此判断连接可用
const mockSend = vi.fn((_data: string | ArrayBuffer) => true)
const mockConnect = vi.fn()
const mockClose = vi.fn()

vi.mock('@/composables/useWebSocket', () => ({
  useWebSocket: (_path: string, opts: any) => {
    onTextCb = opts.onText
    onBinaryCb = opts.onBinary
    return {
      connected: { value: true },
      connect: mockConnect,
      send: mockSend,
      close: mockClose,
    }
  },
}))

function withSetup<T>(composable: () => T): T {
  let result!: T
  const App = defineComponent({
    setup() {
      result = composable()
      return () => h('div')
    },
  })
  mount(App)
  return result
}

// respond 模拟服务端发送 envelope 文本帧
function respond(msg: WSMessage): void {
  if (!onTextCb) throw new Error('onText callback not captured')
  onTextCb(JSON.stringify(msg))
}

// sentPayloadAt 取第 i 次 send 调用的 JSON 负载（仅用于文本帧）
function sentPayloadAt(i: number): any {
  const call = mockSend.mock.calls[i]
  if (!call) throw new Error(`send not called at index ${i}`)
  return JSON.parse(call[0] as string)
}

// parseChunkFrame 解析二进制分片帧（与 useSFTP.buildChunkFrame 对齐）。
function parseChunkFrame(frame: ArrayBuffer): {
  uploadId: string
  chunkIndex: number
  offset: number
  data: Uint8Array
} {
  const view = new DataView(frame)
  let pos = 0
  const idLen = view.getUint32(pos); pos += 4
  const idBytes = new Uint8Array(frame, pos, idLen); pos += idLen
  const uploadId = new TextDecoder().decode(idBytes)
  const chunkIndex = view.getUint32(pos); pos += 4
  const offset = Number(view.getBigUint64(pos)); pos += 8
  const dataLen = Number(view.getBigUint64(pos)); pos += 8
  const data = new Uint8Array(frame, pos, dataLen)
  return { uploadId, chunkIndex, offset, data }
}

describe('useSFTP', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    onTextCb = null
    onBinaryCb = null
  })

  it('calls connect() on creation', () => {
    withSetup(() => useSFTP(node))
    expect(mockConnect).toHaveBeenCalledTimes(1)
  })

  it('list sends type=list and updates files/currentPath on success', async () => {
    const s = withSetup(() => useSFTP(node))
    const p = s.list('/home')
    expect(sentPayloadAt(0)).toEqual({ type: 'list', data: { path: '/home' } })
    respond({
      type: 'list',
      data: {
        files: [{ filename: 'a.txt', size: 1, mode: '0644', is_dir: false, mtime: 0 }],
        path: '/home',
      },
    })
    await p
    expect(s.files.value).toHaveLength(1)
    expect(s.files.value[0].filename).toBe('a.txt')
    expect(s.currentPath.value).toBe('/home')
    expect(s.loading.value).toBe(false)
  })

  it('mkdir sends type=mkdir', async () => {
    const s = withSetup(() => useSFTP(node))
    const p = s.mkdir('/new')
    expect(sentPayloadAt(0)).toEqual({ type: 'mkdir', data: { path: '/new' } })
    respond({ type: 'ok' })
    await p
  })

  it('del sends type=delete', async () => {
    const s = withSetup(() => useSFTP(node))
    const p = s.del('/file')
    expect(sentPayloadAt(0)).toEqual({ type: 'delete', data: { path: '/file' } })
    respond({ type: 'ok' })
    await p
  })

  it('upload single-chunk file: init → binary frame → complete', async () => {
    const s = withSetup(() => useSFTP(node))
    const buf = new Uint8Array(10)
    const file = new File([buf], 't.txt', { type: 'application/octet-stream' })
    const p = s.upload('/remote/t.txt', file)

    // send 调用 0：upload_init JSON
    await vi.waitFor(() => expect(mockSend.mock.calls.length).toBeGreaterThanOrEqual(1))
    expect(sentPayloadAt(0)).toMatchObject({
      type: 'upload_init',
      data: {
        remote_path: '/remote/t.txt',
        filename: 't.txt',
        total_size: 10,
        chunk_size: 1 << 20,
      },
    })
    respond({ type: 'upload_init', data: { upload_id: 'u1', offset: 0 } })

    // send 调用 1：二进制帧（含帧头）
    await vi.waitFor(() => expect(mockSend.mock.calls.length).toBeGreaterThanOrEqual(2))
    const frame = mockSend.mock.calls[1][0] as ArrayBuffer
    expect(frame).toBeInstanceOf(ArrayBuffer)
    const parsed = parseChunkFrame(frame)
    expect(parsed.uploadId).toBe('u1')
    expect(parsed.chunkIndex).toBe(0)
    expect(parsed.offset).toBe(0)
    expect(parsed.data.length).toBe(10)
    // 响应 chunk_ack
    respond({ type: 'chunk_ack', data: { chunk_index: 0 } })

    // send 调用 2：upload_complete JSON
    await vi.waitFor(() => expect(mockSend.mock.calls.length).toBeGreaterThanOrEqual(3))
    expect(sentPayloadAt(2)).toMatchObject({
      type: 'upload_complete',
      data: { upload_id: 'u1' },
    })
    respond({ type: 'ok' })

    await p
    // 单分片 10 字节文件上传完成应正好 100%
    expect(s.uploadProgress.value).toBe(100)
  })

  it('upload resumes from offset', async () => {
    const s = withSetup(() => useSFTP(node))
    // 文件大小 1MB + 100 字节，offset=1MB → 仅剩 100 字节需上传（1 个 chunk）
    const buf = new Uint8Array((1 << 20) + 100)
    const file = new File([buf], 'big.bin', { type: 'application/octet-stream' })
    const guard = s.upload('/r/big.bin', file).catch((e) => e)

    await vi.waitFor(() => expect(mockSend.mock.calls.length).toBeGreaterThanOrEqual(1))
    expect(sentPayloadAt(0).type).toBe('upload_init')
    // 假装已上传 1MB（第 1 片已完成）
    respond({ type: 'upload_init', data: { upload_id: 'u2', offset: 1 << 20 } })

    // 等待 send(二进制帧)
    await vi.waitFor(() => expect(mockSend.mock.calls.length).toBeGreaterThanOrEqual(2))
    const frame = mockSend.mock.calls[1][0] as ArrayBuffer
    const parsed = parseChunkFrame(frame)
    // chunk_index 应为 1（offset 1MB / CHUNK_SIZE 1MB = 1）
    expect(parsed.chunkIndex).toBe(1)
    expect(parsed.offset).toBe(1 << 20)
    // 响应 chunk ack
    respond({ type: 'chunk_ack', data: { chunk_index: 1 } })

    // 等待 upload_complete 发送
    await vi.waitFor(() => expect(mockSend.mock.calls.length).toBeGreaterThanOrEqual(3))
    expect(sentPayloadAt(2).type).toBe('upload_complete')
    respond({ type: 'ok' })

    await guard
  })

  it('upload progress never exceeds 100 for small files', async () => {
    const s = withSetup(() => useSFTP(node))
    // 1 字节文件：旧公式 (0+1MB)/1*100 会爆表，新公式封顶 100
    const buf = new Uint8Array(1)
    const file = new File([buf], 'tiny.txt', { type: 'application/octet-stream' })
    const p = s.upload('/r/tiny.txt', file)

    await vi.waitFor(() => expect(mockSend.mock.calls.length).toBeGreaterThanOrEqual(1))
    respond({ type: 'upload_init', data: { upload_id: 'u3', offset: 0 } })
    await vi.waitFor(() => expect(mockSend.mock.calls.length).toBeGreaterThanOrEqual(2))
    respond({ type: 'chunk_ack', data: { chunk_index: 0 } })
    await vi.waitFor(() => expect(mockSend.mock.calls.length).toBeGreaterThanOrEqual(3))
    respond({ type: 'ok' })

    await p
    expect(s.uploadProgress.value).toBeLessThanOrEqual(100)
    expect(s.uploadProgress.value).toBe(100)
  })

  it('download sends type=download and resets progress', async () => {
    const s = withSetup(() => useSFTP(node))
    s.downloadProgress.value = 50
    const p = s.download('/file')
    expect(sentPayloadAt(0)).toEqual({ type: 'download', data: { path: '/file', offset: 0 } })
    expect(s.downloadProgress.value).toBe(0)
    respond({ type: 'ok' })
    await p
  })

  it('onBinary aggregates with download_start and complete triggers triggerDownload', async () => {
    const s = withSetup(() => useSFTP(node))
    const p = s.download('/file')
    await vi.waitFor(() => expect(mockSend.mock.calls.length).toBeGreaterThanOrEqual(1))
    // 模拟服务端：download_start（total=3）→ 二进制 chunk → complete
    respond({ type: 'download_start', data: { total: 3 } })
    onBinaryCb?.(new Uint8Array([1, 2, 3]).buffer)
    respond({ type: 'complete', data: { filename: 'file' } })
    await p
    // 验证进度为 100%（3 字节已全部接收）
    expect(s.downloadProgress.value).toBe(100)
  })

  it('login failure calls handleError and close', () => {
    withSetup(() => useSFTP(node))
    respond({ type: 'login', data: { success: false, message: 'auth failed' } })
    expect(mockHandleError).toHaveBeenCalledWith('登录失败：auth failed')
    expect(mockClose).toHaveBeenCalledTimes(1)
  })
})
