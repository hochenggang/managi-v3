// 终端协议：基于统一 WS envelope。
// 与后端 handler/terminal.go 对齐。

import type { ApiNode } from './types'
import { wsMessage, type WSResize } from './ws'

/** 构造 login 首帧。 */
export function loginMessage(node: ApiNode): string {
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
