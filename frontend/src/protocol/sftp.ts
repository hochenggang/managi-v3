// SFTP 协议：基于统一 WS envelope。
// 与后端 handler/sftp.go 对齐。

import type { ApiNode } from './types'
import { wsMessage } from './ws'

/** 目录项。 */
export interface SFTPFile {
  filename: string
  size: number
  mode: string
  is_dir: boolean
  mtime: number
}

// ===== 请求构造 =====

export const sftpLogin = (node: ApiNode): string => wsMessage<ApiNode>('login', node)
export const sftpList = (path: string): string => wsMessage('list', { path })
export const sftpMkdir = (path: string): string => wsMessage('mkdir', { path })
export const sftpDelete = (path: string): string => wsMessage('delete', { path })
export const sftpRename = (oldPath: string, newPath: string): string =>
  wsMessage('rename', { old_path: oldPath, new_path: newPath })
export const sftpDownload = (path: string, offset = 0): string =>
  wsMessage('download', { path, offset })
export const sftpUploadInit = (
  remotePath: string,
  filename: string,
  totalSize: number,
  chunkSize: number,
): string =>
  wsMessage('upload_init', {
    remote_path: remotePath,
    filename,
    total_size: totalSize,
    chunk_size: chunkSize,
  })
export const sftpUploadComplete = (uploadId: string): string =>
  wsMessage('upload_complete', { upload_id: uploadId })

// ===== 响应 data 负载 =====

export interface SFTPListData {
  files: SFTPFile[]
  path?: string
}
export interface SFTPDownloadStartData {
  total: number
}
export interface SFTPCompleteData {
  filename: string
}
export interface SFTPChunkAckData {
  chunk_index: number
}
export interface SFTPUploadInitData {
  upload_id: string
  offset: number
}
