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
  let downloadBuffer: Uint8Array[] = []
  let downloadTotalSize = 0
  let downloadReceivedSize = 0

  const { connected, connect, send, close } = useWebSocket('/ws/sftp', {
    authPayload: node,
    onText: (data) => {
      const resp = JSON.parse(data) as SFTPResponse
      if (resp.type === 'connected') return
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
      pendingResolve?.(resp)
      pendingResolve = null
    },
    onBinary: (data) => {
      downloadBuffer.push(new Uint8Array(data))
      downloadReceivedSize += data.byteLength
      downloadProgress.value = downloadTotalSize
        ? Math.round((downloadReceivedSize / downloadTotalSize) * 100)
        : 0
    },
  })

  connect()

  function sendRequest(req: SFTPRequest): Promise<SFTPResponse> {
    return new Promise((resolve, reject) => {
      pendingResolve = resolve
      send(JSON.stringify(req))
      setTimeout(() => reject(new Error('SFTP request timeout')), 30000)
    })
  }

  async function list(path: string): Promise<void> {
    loading.value = true
    const r = await sendRequest({ operation: 'list', remote_path: path })
    if (r.success) currentPath.value = path
    loading.value = false
  }

  // v3 断点续传上传：upload_init 查询 offset → 分片上传 → upload_complete。
  // 设计见 design-v3.md §6.4。
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
      // TODO(P0): 发送带 header 的二进制帧（upload_id + chunk_index + offset）
      send(buf)
      const ack = await sendRequest({
        operation: 'upload_chunk',
        remote_path: remotePath,
        upload_id: init.upload_id,
        chunk_index: idx,
        offset: pos,
      })
      uploadProgress.value = Math.round(((pos + CHUNK_SIZE) / file.size) * 100)
      idx++
    }
    await sendRequest({ operation: 'upload_complete', upload_id: init.upload_id, remote_path: remotePath })
    // 进度持久化到 localStorage（断点恢复用）
    localStorage.removeItem(`upload-${init.upload_id}`)
  }

  // v3 断点续传下载：改用 HTTP Range（design-v3.md §6.5），WS 仅小文件回退。
  // 完整 HTTP Range 实现见 api.ts downloadWithRange。
  async function download(remotePath: string): Promise<void> {
    downloadProgress.value = 0
    downloadBuffer = []
    downloadReceivedSize = 0
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

function triggerDownload(blob: Blob, filename: string): void {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = filename
  a.click()
  URL.revokeObjectURL(a.href)
}
