// useTerminal：xterm.js 终端实例管理 composable。
// v3 协议：只渲染 {type:"msg"} 输出；登录失败格式化错误并 close() 抑制重连。
// 设计见 ../../../design-v3.md §6.1。

import { ref, onUnmounted } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useWebSocket } from './useWebSocket'
import { loginMessage, inputMessage, resizeMessage } from '@/protocol/terminal'
import { parseWSMessage, type WSLoginResult, type WSError } from '@/protocol/ws'
import type { ApiNode } from '@/protocol/types'
import { handleError } from '@/helper'

export function useTerminal(container: HTMLElement, node: ApiNode) {
  const term = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    theme: {
      background: '#002b36', // 保留 v2 solarized dark 配色
      foreground: '#cce4f5',
    },
  })
  const fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(container)
  fitAddon.fit()
  term.focus()

  const { connected, connect, send, close } = useWebSocket('/ws', {
    authPayload: loginMessage(node),
    maxReconnect: 3,
    onText: (data) => {
      const msg = parseWSMessage(data)
      if (!msg) return // 非协议消息忽略，避免渲染垃圾
      switch (msg.type) {
        case 'msg':
          if (typeof msg.data === 'string') {
            term.write(msg.data)
          }
          break
        case 'login': {
          const r = msg.data as WSLoginResult
          if (r && !r.success) {
            const m = r.message ?? 'unknown'
            term.writeln(`\x1b[31m登录失败：${m}\x1b[0m`)
            handleError(`登录失败：${m}`)
            close() // 抑制重连，避免无限重试加剧账号锁定
          }
          break
        }
        case 'error': {
          const e = msg.data as WSError
          term.writeln(`\x1b[31m错误：${e?.message ?? 'unknown'}\x1b[0m`)
          break
        }
        case 'pong':
          break
        default:
          break
      }
    },
    onBinary: (data) => {
      term.write(new TextDecoder('utf-8').decode(data))
    },
  })

  // 用户输入透传（封装为 {type:"msg"}），终端尺寸变化发 resize 消息。
  term.onData((data) => send(inputMessage(data)))
  term.onSelectionChange(() => {
    if (term.getSelection()) {
      navigator.clipboard.writeText(term.getSelection())
    }
  })

  // 首次连接即发送当前尺寸，避免 v2 的 80×24 默认值导致换行错乱。
  const onResize = () => {
    fitAddon.fit()
    send(resizeMessage(term.cols, term.rows))
  }
  window.addEventListener('resize', onResize)
  connect()
  onResize() // 立即同步一次

  onUnmounted(() => {
    window.removeEventListener('resize', onResize)
    close()
    term.dispose()
  })

  return { term, connected }
}
