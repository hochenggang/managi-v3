// useTerminal：xterm.js 终端实例管理 composable。
// v3 协议：只渲染 {type:"msg"} 输出；登录失败格式化错误并 close() 抑制重连。
// 设计见 ../../../design-v3.md §6.1。

import { ref, onUnmounted, watch } from 'vue'
import { Terminal, type ITheme } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useWebSocket } from './useWebSocket'
import { loginMessage, inputMessage, resizeMessage } from '@/protocol/terminal'
import { parseWSMessage, type WSLoginResult, type WSError } from '@/protocol/ws'
import type { ApiNode } from '@/protocol/types'
import { handleError, copyToClipboard, readFromClipboard } from '@/helper'
import { useSettingsStore } from '@/stores/settingsStore'

// 会话 ID 缓存：按 host:port:username 索引，同节点复用同一 sessionId。
// 前端断线重连时携带相同 sessionId，后端即可复用已维护的 shell 会话。
// 修复 E2：模块级 Map 永不清理会导致长期使用后内存泄漏。提供 clearSessionId 供节点删除场景调用。
const sessionIds = new Map<string, string>()
function getSessionId(node: ApiNode): string {
  const key = `${node.host}:${node.port}:${node.username}`
  let id = sessionIds.get(key)
  if (!id) {
    id = crypto.randomUUID()
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
  // 修复 B6/B7：从设置 store 读取终端字体大小与字体族，并在变化时热更新
  const settings = useSettingsStore()
  const term = new Terminal({
    cursorBlink: true,
    fontSize: settings.settings.terminalFontSize,
    fontFamily: settings.settings.terminalFontFamily,
    rightClickSelectsWord: false,
    theme: getTerminalTheme(),
  })
  const fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(container)
  fitAddon.fit()
  term.focus()

  const sessionId = getSessionId(node)
  // 修复 B24：重连期间缓冲用户输入，重连后 flush
  // T1：缓冲上限，避免长时间断连导致内存无限增长
  const MAX_INPUT_BUFFER = 64 * 1024 // 64KB
  let inputBuffer = ''
  const { status, connect, send, close, markFailed, markLoginSuccess } = useWebSocket('/ws', {
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
            markFailed() // 替代 close()，设置 first_failed/reconnect_failed 并抑制重连
          } else if (r && r.success) {
            markLoginSuccess() // 标记登录成功，后续断线重连时状态为 reconnecting 而非 connecting
            if (r.reattached) {
              term.writeln(`\x1b[32m[已恢复之前的会话]\x1b[0m`)
            }
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
    // 移除 onBinary：v3 协议后端仅发送文本帧（writeEnvelope → TextMessage），
    // 终端输出统一走 {type:"msg"} 文本帧，二进制处理为死代码。
  })

  // 修复 B24：用户输入透传，WS 未连接时缓冲，重连后 flush
  term.onData((data) => {
    if (!send(inputMessage(data))) {
      // T1：超限截断头部，保留最新输入
      if (inputBuffer.length + data.length > MAX_INPUT_BUFFER) {
        inputBuffer = inputBuffer.slice(inputBuffer.length + data.length - MAX_INPUT_BUFFER)
      }
      inputBuffer += data
    }
  })

  // 修复 B24：watch status，重连成功后 flush 缓冲的输入
  const stopStatusWatch = watch(status, (s) => {
    if (s === 'connected' && inputBuffer) {
      send(inputMessage(inputBuffer))
      inputBuffer = ''
    }
  })

  // 右键菜单：有选区则复制，无选区则粘贴（屏蔽浏览器默认右键菜单）。
  // 修复 B14：非安全上下文（HTTP）下 navigator.clipboard 不可用，降级到 execCommand。
  const handleContextMenu = async (ev: MouseEvent) => {
    ev.preventDefault()
    const selection = term.getSelection()
    if (selection) {
      await copyToClipboard(selection)
      term.clearSelection()
      return
    }
    const text = await readFromClipboard()
    if (text) {
      // 修复 B15：bracketed paste，多行粘贴时用 ESC[200~ ... ESC[201~ 包裹，
      // 让 shell 识别为粘贴而非手动输入，避免意外执行命令
      term.paste(`\x1b[200~${text}\x1b[201~`)
    }
  }
  container.addEventListener('contextmenu', handleContextMenu)

  // 首次连接即发送当前尺寸，避免 v2 的 80×24 默认值导致换行错乱。
  // T2：用 rAF 节流 resize，避免窗口拖动时高频触发 fit + resize 消息淹没通道
  let resizeRafPending = false
  const onResize = () => {
    if (resizeRafPending) return
    resizeRafPending = true
    requestAnimationFrame(() => {
      resizeRafPending = false
      fitAddon.fit()
      send(resizeMessage(term.cols, term.rows))
    })
  }

  // 修复 B28：移除冗余的 window.addEventListener('resize')，
  // ResizeObserver 已覆盖容器尺寸变化（含 window resize 导致的变化）。
  const resizeObserver = new ResizeObserver(() => onResize())
  resizeObserver.observe(container)

  // 修复 B6/B7：监听终端字体/主题变化，热更新 xterm 实例并重新 fit
  const stopSettingsWatch = watch(
    () => settings.settings,
    (s) => {
      term.options.fontSize = s.terminalFontSize
      term.options.fontFamily = s.terminalFontFamily
      // 主题变更时重新读取 CSS 变量（applyTheme 会切换 document 上的 class）
      term.options.theme = getTerminalTheme()
      // 字体/主题变化影响字符宽高，需重新 fit 同步行列数到后端
      fitAddon.fit()
      send(resizeMessage(term.cols, term.rows))
    },
    { deep: true },
  )

  connect()
  fitAddon.fit() // 立即同步一次尺寸，不走 rAF 节流

  onUnmounted(() => {
    stopStatusWatch()
    stopSettingsWatch()
    container.removeEventListener('contextmenu', handleContextMenu)
    resizeObserver.disconnect()
    close()
    term.dispose()
    // 优化：tab 关闭时清除 sessionId 缓存。
    // WS 关闭后后端会话将进入 60s 空闲回收，重新打开 tab 应创建新会话而非尝试 reattach 已失效的会话。
    clearSessionId(node)
  })

  return { term, status }
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
