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
import { handleError } from '@/helper'

const CHUNK_SIZE = 1 << 20 // 1MB
const DEFAULT_TIMEOUT_MS = 30000
const CHUNK_TIMEOUT_MS = 5 * 60 * 1000 // 分片写入可能跨慢网/慢盘，给 5 分钟

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
  let pendingTimer: ReturnType<typeof setTimeout> | null = null
  let downloadBuffer: Uint8Array[] = []
  let downloadTotalSize = 0
  let downloadReceivedSize = 0

  function resolvePending(msg: WSMessage): void {
    if (!pendingResolve) return
    const fn = pendingResolve
    pendingResolve = null
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

  const { connected, connect, send, close } = useWebSocket('/ws/sftp', {
    authPayload: sftpLogin(node),
    maxReconnect: 3,
    onClose: () => {
      loading.value = false
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
            close() // 抑制重连
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
          return
        case 'complete': {
          const d = msg.data as SFTPCompleteData
          const blob = new Blob(downloadBuffer as BlobPart[])
          triggerDownload(blob, d?.filename ?? 'download')
          downloadBuffer = []
          resolvePending(msg)
          return
        }
        case 'chunk_ack':
          resolvePending(msg)
          return
        case 'error':
          loading.value = false
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
      downloadBuffer.push(new Uint8Array(data))
      downloadReceivedSize += data.byteLength
      downloadProgress.value = downloadTotalSize
        ? Math.min(100, Math.round((downloadReceivedSize / downloadTotalSize) * 100))
        : 0
    },
  })

  loading.value = true
  connect()

  /** sendAndAwait 发送并等待服务端响应（resolve 时 clear 超时，避免泄漏）。
   *  @param timeoutMs 等待响应的超时时间，分片上传等耗时操作可传入更大值。
   */
  function sendAndAwait(sendData: string | ArrayBuffer, timeoutMs = DEFAULT_TIMEOUT_MS): Promise<SFTPOperationResult> {
    return new Promise((resolve, reject) => {
      pendingResolve = resolve
      pendingTimer = setTimeout(() => {
        pendingResolve = null
        pendingTimer = null
        reject(new Error('SFTP request timeout'))
      }, timeoutMs)
      if (!send(sendData)) {
        pendingResolve = null
        if (pendingTimer) {
          clearTimeout(pendingTimer)
          pendingTimer = null
        }
        reject(new Error('WebSocket not connected'))
      }
    })
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
    connected, list, upload, download, mkdir, del, close,
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
