// useTerminal：xterm.js 终端实例管理 composable。
// v3 协议：只渲染 {type:"msg"} 输出；登录失败格式化错误并 close() 抑制重连。
// 设计见 ../../../design-v3.md §6.1。

import { ref, onUnmounted } from 'vue'
import { Terminal, type ITheme } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useWebSocket } from './useWebSocket'
import { loginMessage, inputMessage, resizeMessage } from '@/protocol/terminal'
import { parseWSMessage, type WSLoginResult, type WSError } from '@/protocol/ws'
import type { ApiNode } from '@/protocol/types'
import { handleError } from '@/helper'

// 会话 ID 缓存：按 host:port:username 索引，同节点复用同一 sessionId。
// 前端断线重连时携带相同 sessionId，后端即可复用已维护的 shell 会话。
// 修复 E2：模块级 Map 永不清理会导致长期使用后内存泄漏。提供 clearSessionId 供节点删除场景调用。
const sessionIds = new Map<string, string>()
function getSessionId(node: ApiNode): string {
  const key = `${node.host}:${node.port}:${node.username}`
  let id = sessionIds.get(key)
  if (!id) {
    id = crypto.randomUUID?.() ?? Math.random().toString(36).slice(2) + Date.now().toString(36)
    sessionIds.set(key, id)
  }
  return id
}

/** clearSessionId 清除指定节点的会话 ID 缓存。
 *  在节点被删除时调用，避免 sessionId 残留导致复用到已失效的后端会话。
 */
export function clearSessionId(node: ApiNode): void {
  sessionIds.delete(`${node.host}:${node.port}:${node.username}`)
}

/** clearAllSessionIds 清空全部会话 ID 缓存。
 *  M1：在 clearNodes/setAllNodes（导入配置覆盖全部节点）时调用，
 *  避免旧节点的 sessionId 残留导致复用到已失效的后端会话。
 */
export function clearAllSessionIds(): void {
  sessionIds.clear()
}

export function useTerminal(container: HTMLElement, node: ApiNode) {
  const term = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    rightClickSelectsWord: false,
    theme: getTerminalTheme(),
  })
  const fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(container)
  fitAddon.fit()
  term.focus()

  const sessionId = getSessionId(node)
  const { connected, connect, send, close } = useWebSocket('/ws', {
    authPayload: loginMessage(node, sessionId, term.cols, term.rows),
    maxReconnect: 10, // 后端维持会话，前端应积极重连
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
          } else if (r && r.reattached) {
            term.writeln(`\x1b[32m[已恢复之前的会话]\x1b[0m`)
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

export function getTerminalTheme(): ITheme {
  const root = getComputedStyle(document.documentElement)
  const get = (name: string, fallback: string): string =>
    root.getPropertyValue(name).trim() || fallback

  return {
    background: get('--color-terminal-bg', '#2E3440'),
    foreground: get('--color-terminal-fg', '#D8DEE9'),
    cursor: get('--color-terminal-cursor', '#D8DEE9'),
    cursorAccent: get('--color-terminal-cursor-accent', '#2E3440'),
    selectionBackground: get('--color-terminal-selection', 'rgba(136, 192, 208, 0.3)'),
    black: get('--color-terminal-black', '#3B4252'),
    red: get('--color-terminal-red', '#BF616A'),
    green: get('--color-terminal-green', '#A3BE8C'),
    yellow: get('--color-terminal-yellow', '#EBCB8B'),
    blue: get('--color-terminal-blue', '#81A1C1'),
    magenta: get('--color-terminal-magenta', '#B48EAD'),
    cyan: get('--color-terminal-cyan', '#88C0D0'),
    white: get('--color-terminal-white', '#E5E9F0'),
    brightBlack: get('--color-terminal-brightBlack', '#4C566A'),
    brightRed: get('--color-terminal-brightRed', '#BF616A'),
    brightGreen: get('--color-terminal-brightGreen', '#A3BE8C'),
    brightYellow: get('--color-terminal-brightYellow', '#EBCB8B'),
    brightBlue: get('--color-terminal-brightBlue', '#81A1C1'),
    brightMagenta: get('--color-terminal-brightMagenta', '#B48EAD'),
    brightCyan: get('--color-terminal-brightCyan', '#8FBCBB'),
    brightWhite: get('--color-terminal-brightWhite', '#ECEFF4'),
  }
}
