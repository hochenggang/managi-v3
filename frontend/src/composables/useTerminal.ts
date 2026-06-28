// useTerminal：xterm.js 终端实例管理 composable。
// 修复 v2 缺陷：终端换行错乱（结构化 resize + 首次 fit 即同步尺寸）。
// 设计见 ../../../design-v3.md §6.1。

import { ref, onUnmounted } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useWebSocket } from './useWebSocket'
import { resizeMessage, isControlMessage } from '@/protocol/terminal'
import type { ApiNode } from '@/protocol/types'

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
    authPayload: node, // 首包认证
    onText: (data) => {
      if (!isControlMessage(data)) {
        term.write(data)
      }
    },
    onBinary: (data) => {
      term.write(new TextDecoder('utf-8').decode(data))
    },
  })

  // v3 修正：用户输入透传（v2 行为），终端尺寸变化发结构化 resize 消息（替代转义序列）。
  term.onData((data) => send(data))
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
