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

/** 单帧终端输入的最大字符数。
 *  xterm 对整段粘贴只回调一次 onData，不分帧会把几十 KB 塞进一帧；
 *  后端超限即以 1009 掐断整条会话（表现为「粘贴长文本就断线」）。
 */
export const INPUT_CHUNK_CHARS = 8 * 1024

/** chunkInput 把长输入切成不超过 max 字符的片段，保持顺序。
 *  切点若落在代理对（emoji 等）中间，被截断的孤立码元经 JSON 会变成 U+FFFD，
 *  故末尾是高代理项时回退一位。
 *  max 下限钳到 1，且回退不允许退到 0：否则切点为空、rest 永不缩短，
 *  调用方传错参数表现为「页面卡死」而不是报错。
 */
export function chunkInput(data: string, max: number = INPUT_CHUNK_CHARS): string[] {
  const limit = Math.max(1, Math.floor(max))
  if (data.length <= limit) return [data]
  const parts: string[] = []
  let rest = data
  while (rest.length > limit) {
    let cut = limit
    const prev = rest.charCodeAt(cut - 1)
    if (cut > 1 && prev >= 0xd800 && prev <= 0xdbff) cut -= 1
    parts.push(rest.slice(0, cut))
    rest = rest.slice(cut)
  }
  if (rest) parts.push(rest)
  return parts
}

/** 构造 resize 消息。 */
export function resizeMessage(cols: number, rows: number): string {
  return wsMessage<WSResize>('resize', { cols, rows })
}
