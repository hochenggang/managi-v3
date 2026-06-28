// 终端协议：终端会话消息类型。
// v3 修正：resize 改为结构化消息，替代 v2 的 \x1b[8;rows;cols t 转义序列。
// 设计见 ../../../design-v3.md §6.1。

/** 终端控制消息（前缀 type 字段区分，与字节流透传分离）。 */
export interface TerminalControlMessage {
  type: 'resize'
  cols: number
  rows: number
}

/** 判断字符串是否为结构化控制消息。 */
export function isControlMessage(data: string): boolean {
  return data.startsWith('{"type":"resize"')
}

/** 构造 resize 消息。 */
export function resizeMessage(cols: number, rows: number): string {
  return JSON.stringify({ type: 'resize', cols, rows } satisfies TerminalControlMessage)
}
