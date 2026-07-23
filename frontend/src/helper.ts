// 通知封装，与 v2 helper.ts 一致。

import { notify } from '@kyvg/vue3-notification'

export function handleMsg(value: string): void {
  notify({ type: 'success', text: value })
}

/**
 * handleError 接受任意错误值并归一化为字符串后通知。
 * 修复 B1/B2：原签名 (value: string) 导致调用方传入 Error 对象时
 * 渲染为 [object Object]。放宽为 unknown 并内部归一化，所有调用方自动安全。
 */
export function handleError(value: unknown): void {
  notify({ type: 'error', text: toErrorMessage(value) })
}

/**
 * toErrorMessage 将任意值归一化为错误消息字符串。
 * - string 原样返回
 * - Error 返回 message
 * - 其他对象/原始值用 String() 转换
 * 供需要在模板字符串中拼接错误的调用方使用。
 */
export function toErrorMessage(value: unknown): string {
  if (typeof value === 'string') return value
  if (value instanceof Error) return value.message
  return String(value)
}

/** copyToClipboard 复制文本到剪贴板。
 *  修复 B14：非安全上下文（HTTP）下 navigator.clipboard 不可用，降级到 execCommand。
 */
export async function copyToClipboard(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text)
    return
  }
  // 降级：用临时 textarea + execCommand('copy')
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  try {
    document.execCommand('copy')
  } finally {
    document.body.removeChild(ta)
  }
}

/** readFromClipboard 从剪贴板读取文本。
 *  修复 B14：非安全上下文降级返回空字符串（execCommand 无 paste 等价物，
 *  浏览器安全策略禁止 JS 读取剪贴板，只能提示用户用 Ctrl+V）。
 */
export async function readFromClipboard(): Promise<string> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      return await navigator.clipboard.readText()
    } catch {
      return ''
    }
  }
  // 非安全上下文无法读取剪贴板，返回空（用户可用 Ctrl+V 粘贴）
  return ''
}
