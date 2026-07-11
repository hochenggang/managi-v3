<template>
  <div class="terminal-tab">
    <div class="terminal-wrapper">
      <div ref="terminalContainer" class="terminal-container"></div>
    </div>
    <div class="terminal-toolbar">
      <span class="terminal-info">{{ node ? `${node.name} (${node.host}:${node.port})` : t('xtermPanel.idle') }}</span>
      <span :class="['status', statusClass]">{{ statusText }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
// 终端标签页：每个标签独立维护 xterm.js 实例与 WebSocket 连接。
// 组件被隐藏（v-show）时不会卸载，连接保持后台运行。
// 连接状态细分为：首次连接(进行中/成功/失败) + 重试连接(进行中/成功/失败)。
// 所有状态仅通过工具栏 status 文本+颜色呈现，不使用覆盖层。
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { useTerminal, getTerminalTheme } from '@/composables/useTerminal'
import type { ConnectionStatus } from '@/composables/useWebSocket'
import { useI18n } from 'vue-i18n'
import type { ApiNode } from '@/protocol/types'

const props = defineProps<{ node: ApiNode }>()

const { t } = useI18n()
const terminalContainer = ref<HTMLElement | null>(null)
// 有节点时初始即为 connecting，确保首帧渲染就显示"正在连接…"而非"已连接"
const status = ref<ConnectionStatus>(props.node ? 'connecting' : 'idle')

let standaloneTerm: Terminal | null = null
let cleanup: (() => void) | null = null

const generateGreenText = (text: string) => `\x1B[32m${text}\x1B[0m`

const statusText = computed(() => {
  const map: Record<string, string> = {
    idle: t('xtermPanel.idle'),
    connecting: t('xtermPanel.connecting'),
    connected: t('finder.connected'),
    first_failed: t('xtermPanel.firstFailed'),
    reconnecting: t('xtermPanel.reconnecting'),
    reconnect_failed: t('xtermPanel.reconnectFailed'),
    disconnected: t('finder.disconnected'),
  }
  return map[status.value] ?? ''
})

const statusClass = computed(() => {
  const map: Record<string, string> = {
    connecting: 'connecting',
    connected: 'connected',
    first_failed: 'failed',
    reconnecting: 'connecting',
    reconnect_failed: 'failed',
    disconnected: 'disconnected',
  }
  return map[status.value] ?? ''
})

onMounted(() => {
  if (!terminalContainer.value) return
  if (!props.node) {
    standaloneTerm = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      theme: getTerminalTheme(),
    })
    standaloneTerm.open(terminalContainer.value)
    standaloneTerm.writeln(generateGreenText(t('xtermPanel.hello')))
    return
  }
  const { status: wsStatus } = useTerminal(terminalContainer.value, props.node)
  // immediate: true 确保首帧即同步，后续状态变更由 watch 驱动
  cleanup = watch(wsStatus, (val) => { status.value = val }, { immediate: true })
})

onUnmounted(() => {
  if (standaloneTerm) {
    standaloneTerm.dispose()
    standaloneTerm = null
  }
  cleanup?.()
})
</script>

<style scoped>
.terminal-tab {
  display: flex;
  flex-direction: column;
  height: 100%;
  background-color: var(--color-terminal-bg, #002b36);
}

.terminal-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 0.75rem;
  height: 2rem;
  background-color: var(--color-panel-bg);
  font-size: 0.7rem;
  color: var(--color-font-2);
  flex-shrink: 0;
  border-top: 1px solid var(--color-border);
}

.terminal-info {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.status {
  font-size: 0.75rem;
  white-space: nowrap;
  transition: color 0.25s ease;
}

.status.connected {
  color: var(--color-green);
}

.status.connecting {
  color: var(--color-yellow, #EBCB8B);
}

.status.failed {
  color: var(--color-red);
}

.status.disconnected {
  color: var(--color-font-3, #4C566A);
}

.terminal-wrapper {
  flex: 1;
  position: relative;
  min-height: 0;
  background-color: var(--color-terminal-bg, #002b36);
}

.terminal-container {
  position: absolute;
  inset: 8px;
}

.terminal-container :deep(.xterm) {
  width: 100%;
  height: 100%;
}
</style>
