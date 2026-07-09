// WS 统一消息协议：所有文本帧为 {type, data} envelope。
// 与后端 handler/wsmsg.go 对齐。

/** WS 消息类型字面量联合（约束前后端协议）。 */
export type WSMessageType =
  | 'login' // 登录（首帧）/ 登录结果
  | 'msg' // 终端输入/输出
  | 'resize' // 终端尺寸调整
  | 'ping' // 心跳请求
  | 'pong' // 心跳响应
  | 'error' // 错误
  | 'list' // SFTP 列目录
  | 'ok' // SFTP 操作成功
  | 'download_start' // SFTP 下载开始
  | 'complete' // SFTP 下载完成
  | 'chunk_ack' // SFTP 分片确认
  | 'upload_init' // SFTP 上传初始化
  | 'upload_complete' // SFTP 上传完成
  | 'mkdir'
  | 'delete'
  | 'rename'
  | 'download'

/** 统一消息信封。 */
export interface WSMessage<T = unknown> {
  type: WSMessageType
  data?: T
}

/** 登录结果 data 负载。 */
export interface WSLoginResult {
  success: boolean
  message?: string
  reattached?: boolean // true=后端复用了已存在的终端会话
}

/** 错误 data 负载。 */
export interface WSError {
  message: string
}

/** resize data 负载。 */
export interface WSResize {
  cols: number
  rows: number
}

/** 构造 envelope 消息字符串。 */
export function wsMessage<T>(type: WSMessageType, data?: T): string {
  const msg: WSMessage<T> = data === undefined ? { type } : { type, data }
  return JSON.stringify(msg)
}

/** 解析 envelope，失败返回 null。 */
export function parseWSMessage(data: string): WSMessage | null {
  try {
    const obj = JSON.parse(data) as WSMessage
    if (typeof obj.type !== 'string') return null
    return obj
  } catch {
    return null
  }
}
