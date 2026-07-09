// 终端协议：基于统一 WS envelope。
// 与后端 handler/terminal.go 对齐。

import type { ApiNode } from './types'
import { wsMessage, type WSResize } from './ws'

/** 构造 login 首帧（新版含 session_id，支持后端会话复用）。 */
export function loginMessage(node: ApiNode, sessionId?: string, cols?: number, rows?: number): string {
  if (sessionId) {
    return wsMessage('login', { node, session_id: sessionId, cols: cols ?? 80, rows: rows ?? 24 })
  }
  // 无 sessionId 时回退旧格式（纯 node），保持兼容
  return wsMessage<ApiNode>('login', node)
}

/** 构造终端输入消息（用户按键透传到 shell stdin）。 */
export function inputMessage(data: string): string {
  return wsMessage<string>('msg', data)
}

/** 构造 resize 消息。 */
export function resizeMessage(cols: number, rows: number): string {
  return wsMessage<WSResize>('resize', { cols, rows })
}
