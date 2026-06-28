// SFTP 协议：操作类型枚举 + 断点续传扩展消息。
// 与后端 model.FileOperationType 对齐，v3 新增 upload_init/upload_chunk/upload_complete。
// 设计见 ../../../design-v3.md §6.4。

export type FileOperation =
  | 'list'
  | 'mkdir'
  | 'delete'
  | 'rename'
  | 'move'
  | 'upload'
  | 'download'
  | 'upload_init'      // v3 新增：断点续传初始化
  | 'upload_chunk'     // v3 新增：分片上传
  | 'upload_complete'  // v3 新增：上传完成

/** 目录项。 */
export interface SFTPFile {
  filename: string
  size: number
  mode: string
  is_dir: boolean
  mtime: number
}

/** SFTP 请求（与后端 FileOperationRequest 对齐）。 */
export interface SFTPRequest {
  operation: FileOperation
  remote_path: string
  new_path?: string
  // v3 断点续传扩展
  upload_id?: string
  filename?: string
  total_size?: number
  chunk_size?: number
  chunk_index?: number
  offset?: number
}

/** SFTP 响应。 */
export interface SFTPResponse {
  type?: string
  success: boolean
  message?: string
  path?: string
  size?: number
  files?: SFTPFile[]
  filename?: string
  complete?: boolean
  progress?: number
  operation?: string
  // v3 断点续传扩展
  upload_id?: string
  uploaded_offset?: number
  chunk_index?: number
  received_offset?: number
}
