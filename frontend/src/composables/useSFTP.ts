// useSFTP：SFTP 文件管理 composable。
// v3 协议：统一 {type, data} envelope。服务端登录成功后主动 list /，无需前端首请求。
// 设计见 ../../../design-v3.md §6.4 §6.5。

import { ref } from 'vue'
import { useWebSocket } from './useWebSocket'
import { parseWSMessage, type WSMessage, type WSLoginResult, type WSError } from '@/protocol/ws'
import {
  sftpLogin,
  sftpList,
  sftpMkdir,
  sftpDelete,
  sftpDownload,
  sftpUploadInit,
  sftpUploadComplete,
  type SFTPFile,
  type SFTPListData,
  type SFTPDownloadStartData,
  type SFTPCompleteData,
  type SFTPUploadInitData,
} from '@/protocol/sftp'
import type { ApiNode } from '@/protocol/types'
import { downloadWithRange } from '@/api'
import { handleError } from '@/helper'

const CHUNK_SIZE = 1 << 20 // 1MB
const DEFAULT_TIMEOUT_MS = 30000
const CHUNK_TIMEOUT_MS = 5 * 60 * 1000 // 分片写入可能跨慢网/慢盘，给 5 分钟
// WS 下载缓冲上限。超此大小中止并提示走 HTTP Range 流式下载，避免浏览器 OOM。
const DOWNLOAD_BUFFER_LIMIT = 256 * 1024 * 1024 // 256MB
// 超过此大小的文件应改走 HTTP Range 流式下载，而非 WS 缓冲。导出供调用方统一判定。
export const LARGE_FILE_THRESHOLD = 100 * 1024 * 1024 // 100MB

interface SFTPOperationResult {
  success: boolean
  message?: string
  data?: unknown
}

export function useSFTP(node: ApiNode) {
  const currentPath = ref('/')
  const files = ref<SFTPFile[]>([])
  const loading = ref(false)
  const uploadProgress = ref(0)
  const downloadProgress = ref(0)

  let pendingResolve: ((r: SFTPOperationResult) => void) | null = null
  let pendingReject: ((e: Error) => void) | null = null
  let pendingTimer: ReturnType<typeof setTimeout> | null = null
  // seq 关联：每个请求分配递增序号，响应按 seq 匹配；迟到的旧响应（seq 不匹配）被丢弃。
  // 取代原 discardNextResponse 兜底（C4），根因修复请求-响应错配风险。
  let seqCounter = 0
  let pendingSeq = 0
  let downloadBuffer: Uint8Array[] = []
  let downloadTotalSize = 0
  let downloadReceivedSize = 0
  // 下载激活标志，仅在 download_start → complete 期间为 true，避免前次下载的延迟二进制帧污染新下载
  let downloadActive = false

  /** rejectPending 拒绝并清理 pending Promise。供 close/onClose/超时调用。 */
  function rejectPending(err: Error): void {
    if (pendingTimer) {
      clearTimeout(pendingTimer)
      pendingTimer = null
    }
    if (pendingResolve) {
      pendingResolve = null
    }
    if (pendingReject) {
      const fn = pendingReject
      pendingReject = null
      fn(err)
    }
  }

  function resolvePending(msg: WSMessage): void {
    // seq 关联：响应必须匹配当前等待的请求序号，否则是迟到的旧响应，丢弃。
    // 旧后端不带 seq 时回退按到达顺序 resolve（向后兼容）。
    if (msg.seq !== undefined && msg.seq !== pendingSeq) return
    if (!pendingResolve) return
    const fn = pendingResolve
    pendingResolve = null
    pendingReject = null
    if (pendingTimer) {
      clearTimeout(pendingTimer)
      pendingTimer = null
    }
    fn({
      success: msg.type !== 'error',
      message: msg.type === 'error' ? (msg.data as WSError)?.message : undefined,
      data: msg.data,
    })
  }

  const { status, connected, connect, send, close: wsClose, markFailed, markLoginSuccess } = useWebSocket('/ws/sftp', {
    authPayload: sftpLogin(node),
    // 原 maxReconnect:3 过低，网络抖动时 SFTP 早早放弃。后端 SSH 连接池维持会话，
    // 提高到 10 与终端一致。登录失败由 markFailed 抑制重连。
    maxReconnect: 10,
    onClose: () => {
      loading.value = false
      downloadActive = false
      // WS 断开时 reject pending Promise，避免用户卡住等待超时
      rejectPending(new Error('WebSocket closed'))
    },
    onText: (data) => {
      const msg = parseWSMessage(data)
      if (!msg) return
      switch (msg.type) {
        case 'login': {
          const r = msg.data as WSLoginResult
          if (r && !r.success) {
            loading.value = false
            handleError(`登录失败：${r.message ?? 'unknown'}`)
            // 用 markFailed 替代 close，设置 first_failed 状态而非 disconnected，
            // UI 可区分"登录失败"与"主动关闭"
            markFailed()
          } else if (r && r.success) {
            // 登录成功才置 connected（与 useTerminal 一致，避免 WS 握手即显示已连接）
            markLoginSuccess()
          }
          return
        }
        case 'list': {
          // 服务端登录后主动推送 list /，或响应客户端 list 请求
          const d = msg.data as SFTPListData
          if (d) {
            files.value = d.files
            if (d.path) currentPath.value = d.path
          }
          loading.value = false
          resolvePending(msg)
          return
        }
        case 'download_start':
          downloadTotalSize = (msg.data as SFTPDownloadStartData)?.total ?? 0
          // 标记下载激活，后续二进制帧才会被接纳
          downloadActive = true
          return
        case 'complete': {
          const d = msg.data as SFTPCompleteData
          const blob = new Blob(downloadBuffer as BlobPart[])
          triggerDownload(blob, d?.filename ?? 'download')
          downloadBuffer = []
          downloadActive = false
          resolvePending(msg)
          return
        }
        case 'chunk_ack':
          resolvePending(msg)
          return
        case 'error':
          loading.value = false
          // 错误时关闭下载激活标志，避免后续二进制帧误纳入
          downloadActive = false
          handleError((msg.data as WSError)?.message ?? 'SFTP error')
          resolvePending(msg)
          return
        case 'ok':
          resolvePending(msg)
          return
        case 'pong':
          return
        default:
          resolvePending(msg)
      }
    },
    onBinary: (data) => {
      // 仅在被激活的下载期间接纳二进制帧，避免前次下载延迟帧污染新下载
      if (!downloadActive) return
      // 缓冲上限保护，超限中止并提示走 HTTP Range，避免浏览器 OOM
      if (downloadReceivedSize + data.byteLength > DOWNLOAD_BUFFER_LIMIT) {
        handleError(`文件超过 ${DOWNLOAD_BUFFER_LIMIT / 1024 / 1024}MB，请使用 downloadViaHTTP 流式下载`)
        downloadBuffer = []
        downloadReceivedSize = 0
        downloadTotalSize = 0
        downloadActive = false
        wsClose()
        return
      }
      downloadBuffer.push(new Uint8Array(data))
      downloadReceivedSize += data.byteLength
      downloadProgress.value = downloadTotalSize
        ? Math.min(100, Math.round((downloadReceivedSize / downloadTotalSize) * 100))
        : 0
    },
  })

  /** close 包装：手动关闭时先 reject pending Promise，再关闭 WS。
   *  修复 B5：原 close() 设 ws.onclose=null 导致 onClose 回调永不触发，
   *  pending Promise 永不 reject，组件卸载时泄漏。
   */
  function close(): void {
    rejectPending(new Error('SFTP closed manually'))
    downloadActive = false
    wsClose()
  }

  loading.value = true
  connect()

  /** sendAndAwait 发送并等待服务端响应（resolve 时 clear 超时，避免泄漏）。
   *  @param timeoutMs 等待响应的超时时间，分片上传等耗时操作可传入更大值。
   *
   *  每个请求分配递增 seq 并注入到消息（文本 JSON 注入，二进制分片帧前置 8 字节），
   *  响应按 seq 匹配；迟到的旧响应（seq 不匹配）被 resolvePending 丢弃。
   *  串行语义保留：前序未完成再发请求直接抛错。
   */
  function sendAndAwait(sendData: string | ArrayBuffer, timeoutMs = DEFAULT_TIMEOUT_MS): Promise<SFTPOperationResult> {
    if (pendingResolve) {
      return Promise.reject(new Error('SFTP busy: previous operation pending'))
    }
    const seq = ++seqCounter
    pendingSeq = seq
    const payload = typeof sendData === 'string'
      ? injectTextSeq(sendData, seq)
      : prependChunkSeq(sendData, seq)
    return new Promise((resolve, reject) => {
      pendingResolve = resolve
      pendingReject = reject
      pendingTimer = setTimeout(() => {
        pendingResolve = null
        pendingReject = null
        pendingTimer = null
        reject(new Error('SFTP request timeout'))
      }, timeoutMs)
      if (!send(payload)) {
        pendingResolve = null
        pendingReject = null
        if (pendingTimer) {
          clearTimeout(pendingTimer)
          pendingTimer = null
        }
        reject(new Error('WebSocket not connected'))
      }
    })
  }

  // injectTextSeq 把 seq 注入到已序列化的文本 envelope（JSON 解析 + 重序列化）。
  function injectTextSeq(s: string, seq: number): string {
    const env = JSON.parse(s) as Record<string, unknown>
    env.seq = seq
    return JSON.stringify(env)
  }

  // prependChunkSeq 在二进制分片帧前置 8 字节大端序 seq，与后端 parseChunkFrame 对齐。
  function prependChunkSeq(frame: ArrayBuffer, seq: number): ArrayBuffer {
    const out = new ArrayBuffer(8 + frame.byteLength)
    new DataView(out).setBigUint64(0, BigInt(seq))
    new Uint8Array(out, 8).set(new Uint8Array(frame))
    return out
  }

  async function list(path: string): Promise<void> {
    loading.value = true
    try {
      const r = await sendAndAwait(sftpList(path))
      if (!r.success) throw new Error(r.message)
    } finally {
      loading.value = false
    }
  }

  // v3 断点续传上传：upload_init 查询 offset → 二进制帧头发送分片 → upload_complete。
  // remoteDir 为远程目标目录，文件名取自 file.name。
  async function upload(remoteDir: string, file: File): Promise<void> {
    uploadProgress.value = 0
    const init = await sendAndAwait(sftpUploadInit(remoteDir, file.name, file.size, CHUNK_SIZE))
    if (!init.success) throw new Error(init.message)
    const initData = init.data as SFTPUploadInitData
    const offset = initData?.offset ?? 0
    let idx = Math.floor(offset / CHUNK_SIZE)
    for (let pos = offset; pos < file.size; pos += CHUNK_SIZE) {
      const chunk = file.slice(pos, pos + CHUNK_SIZE)
      const buf = await chunk.arrayBuffer()
      const frame = buildChunkFrame(initData!.upload_id, idx, pos, new Uint8Array(buf))
      const ack = await sendAndAwait(frame, CHUNK_TIMEOUT_MS)
      if (!ack.success) throw new Error(ack.message)
      // 进度用实际写入字节数，封顶 100%（修复 A7：小文件进度爆表）
      const written = Math.min(pos + buf.byteLength, file.size)
      uploadProgress.value = Math.round((written / file.size) * 100)
      idx++
    }
    const done = await sendAndAwait(sftpUploadComplete(initData!.upload_id))
    if (!done.success) throw new Error(done.message)
  }

  async function download(remotePath: string): Promise<void> {
    downloadProgress.value = 0
    downloadBuffer = []
    downloadReceivedSize = 0
    downloadTotalSize = 0
    const r = await sendAndAwait(sftpDownload(remotePath))
    if (!r.success) throw new Error(r.message)
  }

  /** downloadViaHTTP 大文件流式下载（HTTP Range，分块触发 Blob 下载，避免内存累积）。
   *  修复 B8：WS 下载会将全部 chunk 累积到内存，GB 级文件会 OOM。
   *  调用方在文件已知较大（超 LARGE_FILE_THRESHOLD）时应直接使用本方法。
   *  修复 B29：错误/超限提前返回时重置进度并释放 reader，避免残留非零进度与流悬挂。
   */
  async function downloadViaHTTP(remotePath: string): Promise<void> {
    downloadProgress.value = 0
    const { total, stream } = await downloadWithRange(node, remotePath, 0)
    const chunks: Uint8Array[] = []
    let received = 0
    const reader = stream.getReader()
    let succeeded = false
    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        if (value) {
          chunks.push(value)
          received += value.byteLength
          downloadProgress.value = total ? Math.min(100, Math.round((received / total) * 100)) : 0
          // 流式路径同样设上限，避免极端大文件 OOM
          if (received > DOWNLOAD_BUFFER_LIMIT) {
            handleError(`文件超过 ${DOWNLOAD_BUFFER_LIMIT / 1024 / 1024}MB 下载上限`)
            return
          }
        }
      }
      succeeded = true
    } finally {
      reader.releaseLock()
      // 失败/超限时重置进度，避免残留值影响下次下载显示
      if (!succeeded) downloadProgress.value = 0
    }
    const blob = new Blob(chunks as BlobPart[])
    triggerDownload(blob, remotePath.split('/').pop() ?? 'download')
  }

  async function mkdir(remotePath: string): Promise<void> {
    const r = await sendAndAwait(sftpMkdir(remotePath))
    if (!r.success) throw new Error(r.message)
  }

  async function del(remotePath: string): Promise<void> {
    const r = await sendAndAwait(sftpDelete(remotePath))
    if (!r.success) throw new Error(r.message)
  }

  return {
    currentPath, files, loading, uploadProgress, downloadProgress,
    connected, status, list, upload, download, downloadViaHTTP, mkdir, del, close,
  }
}

// buildChunkFrame 构造二进制分片帧（与后端 parseChunkFrame 对齐）。
// 帧格式（大端序）：[4字节 upload_id_len][upload_id][4字节 chunk_index][8字节 offset][8字节 data_len][data]
function buildChunkFrame(uploadId: string, chunkIndex: number, offset: number, data: Uint8Array): ArrayBuffer {
  const idBytes = new TextEncoder().encode(uploadId)
  const headerLen = 4 + idBytes.length + 4 + 8 + 8
  const buf = new ArrayBuffer(headerLen + data.length)
  const view = new DataView(buf)
  let pos = 0
  view.setUint32(pos, idBytes.length); pos += 4
  new Uint8Array(buf, pos, idBytes.length).set(idBytes); pos += idBytes.length
  view.setUint32(pos, chunkIndex); pos += 4
  view.setBigUint64(pos, BigInt(offset)); pos += 8
  view.setBigUint64(pos, BigInt(data.length)); pos += 8
  new Uint8Array(buf, pos, data.length).set(data)
  return buf
}

function triggerDownload(blob: Blob, filename: string): void {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = filename
  a.click()
  URL.revokeObjectURL(a.href)
}
