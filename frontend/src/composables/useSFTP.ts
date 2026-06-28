// useSFTP：SFTP 文件管理 composable。
// 修复 v2 缺陷：上传/下载断点续传。
// 设计见 ../../../design-v3.md §6.4 §6.5。

import { ref } from 'vue'
import { useWebSocket } from './useWebSocket'
import type { SFTPFile, SFTPRequest, SFTPResponse } from '@/protocol/sftp'
import type { ApiNode } from '@/protocol/types'

const CHUNK_SIZE = 1 << 20 // 1MB

export function useSFTP(node: ApiNode) {
  const currentPath = ref('/')
  const files = ref<SFTPFile[]>([])
  const loading = ref(false)
  const uploadProgress = ref(0)
  const downloadProgress = ref(0)

  let pendingResolve: ((r: SFTPResponse) => void) | null = null
  let pendingTimer: ReturnType<typeof setTimeout> | null = null
  let downloadBuffer: Uint8Array[] = []
  let downloadTotalSize = 0
  let downloadReceivedSize = 0

  const { connected, connect, send, close } = useWebSocket('/ws/sftp', {
    authPayload: node,
    onText: (data) => {
      const resp = JSON.parse(data) as SFTPResponse
      if (resp.type === 'connected') return
      if (resp.type === 'download_start') {
        // 后端流式下载起始帧，告知总大小供进度计算
        downloadTotalSize = resp.total ?? 0
        return
      }
      if (resp.type === 'progress') {
        downloadProgress.value = resp.progress ?? 0
        return
      }
      if (resp.files) {
        files.value = resp.files
      }
      if (resp.complete) {
        // 下载完成：合并分块触发浏览器下载
        const blob = new Blob(downloadBuffer as BlobPart[])
        triggerDownload(blob, resp.filename ?? 'download')
        downloadBuffer = []
      }
      // resolve 等待中的请求（上传 chunk_ack / list / mkdir 等）
      if (pendingResolve) {
        const fn = pendingResolve
        pendingResolve = null
        if (pendingTimer) {
          clearTimeout(pendingTimer)
          pendingTimer = null
        }
        fn(resp)
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

  connect()

  // sendAndAwait 发送文本或二进制帧并等待服务端响应（resolve 时 clear 超时，避免 A18 泄漏）。
  function sendAndAwait(sendData: string | ArrayBuffer): Promise<SFTPResponse> {
    return new Promise((resolve, reject) => {
      pendingResolve = resolve
      pendingTimer = setTimeout(() => {
        pendingResolve = null
        pendingTimer = null
        reject(new Error('SFTP request timeout'))
      }, 30000)
      send(sendData)
    })
  }

  function sendRequest(req: SFTPRequest): Promise<SFTPResponse> {
    return sendAndAwait(JSON.stringify(req))
  }

  async function list(path: string): Promise<void> {
    loading.value = true
    const r = await sendRequest({ operation: 'list', remote_path: path })
    if (r.success) currentPath.value = path
    loading.value = false
  }

  // v3 断点续传上传：upload_init 查询 offset → 二进制帧头发送分片 → upload_complete。
  // 帧格式见 buildChunkFrame。设计见 design-v3.md §6.4。
  async function upload(remotePath: string, file: File): Promise<void> {
    uploadProgress.value = 0
    const init = await sendRequest({
      operation: 'upload_init',
      remote_path: remotePath,
      filename: file.name,
      total_size: file.size,
      chunk_size: CHUNK_SIZE,
    })
    const offset = init.uploaded_offset ?? 0
    let idx = Math.floor(offset / CHUNK_SIZE)
    for (let pos = offset; pos < file.size; pos += CHUNK_SIZE) {
      const chunk = file.slice(pos, pos + CHUNK_SIZE)
      const buf = await chunk.arrayBuffer()
      const frame = buildChunkFrame(init.upload_id!, idx, pos, new Uint8Array(buf))
      await sendAndAwait(frame)
      // 进度用实际写入字节数，封顶 100%（修复 A7：小文件进度爆表）
      const written = Math.min(pos + buf.byteLength, file.size)
      uploadProgress.value = Math.round((written / file.size) * 100)
      idx++
    }
    await sendRequest({ operation: 'upload_complete', upload_id: init.upload_id, remote_path: remotePath })
  }

  // v3 断点续传下载：改用 HTTP Range（design-v3.md §6.5），WS 仅小文件回退。
  // 完整 HTTP Range 实现见 api.ts downloadWithRange。
  async function download(remotePath: string): Promise<void> {
    downloadProgress.value = 0
    downloadBuffer = []
    downloadReceivedSize = 0
    downloadTotalSize = 0
    await sendRequest({ operation: 'download', remote_path: remotePath })
  }

  async function mkdir(remotePath: string): Promise<void> {
    await sendRequest({ operation: 'mkdir', remote_path: remotePath })
  }

  async function del(remotePath: string): Promise<void> {
    await sendRequest({ operation: 'delete', remote_path: remotePath })
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
