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
    rightClickSelectsWord: false,
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

  // 右键菜单：有选区则复制，无选区则粘贴（屏蔽浏览器默认右键菜单）。
  const handleContextMenu = async (ev: MouseEvent) => {
    ev.preventDefault()
    const selection = term.getSelection()
    if (selection) {
      try {
        await navigator.clipboard.writeText(selection)
        term.clearSelection()
      } catch (e) {
        handleError('复制失败')
      }
      return
    }
    try {
      const text = await navigator.clipboard.readText()
      if (text) {
        term.paste(text)
      }
    } catch (e) {
      handleError('粘贴失败，请确认已授予剪贴板权限')
    }
  }
  container.addEventListener('contextmenu', handleContextMenu)

  // 首次连接即发送当前尺寸，避免 v2 的 80×24 默认值导致换行错乱。
  const onResize = () => {
    fitAddon.fit()
    send(resizeMessage(term.cols, term.rows))
  }
  window.addEventListener('resize', onResize)

  // 监听容器尺寸变化，比 window.resize 更可靠，避免面板缩放时行列数不同步。
  const resizeObserver = new ResizeObserver(() => onResize())
  resizeObserver.observe(container)

  connect()
  onResize() // 立即同步一次

  onUnmounted(() => {
    window.removeEventListener('resize', onResize)
    container.removeEventListener('contextmenu', handleContextMenu)
    resizeObserver.disconnect()
    close()
    term.dispose()
  })

  return { term, connected }
}
