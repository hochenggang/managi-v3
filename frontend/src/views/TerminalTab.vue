<template>
  <div class="terminal-tab">
    <div class="terminal-wrapper">
      <div ref="terminalContainer" class="terminal-container"></div>
      <div v-if="node && showOverlay" class="terminal-overlay">
        <span class="overlay-text">{{ overlayText }}</span>
      </div>
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
const status = ref<ConnectionStatus>('idle')

let standaloneTerm: Terminal | null = null
let cleanup: (() => void) | null = null

const generateGreenText = (text: string) => `\x1B[32m${text}\x1B[0m`

// 覆盖层仅在连接中/失败状态显示（不含 connected/idle/disconnected）
const showOverlay = computed(() =>
  ['connecting', 'first_failed', 'reconnecting', 'reconnect_failed'].includes(status.value)
)

const overlayText = computed(() => {
  const map: Record<string, string> = {
    connecting: t('xtermPanel.connecting'),
    first_failed: t('xtermPanel.firstFailed'),
    reconnecting: t('xtermPanel.reconnecting'),
    reconnect_failed: t('xtermPanel.reconnectFailed'),
  }
  return map[status.value] ?? ''
})

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
  // 修复 L4：immediate: true 确保首帧即同步，避免值拷贝导致初始状态过期
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

.terminal-overlay {
  position: absolute;
  inset: 0;
  background: rgba(46, 52, 64, 0.7);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10;
  pointer-events: none;
}

.overlay-text {
  color: var(--color-font-2, #D8DEE9);
  font-size: 0.9rem;
  padding: 0.5rem 1rem;
  background: rgba(0, 0, 0, 0.4);
  border-radius: 4px;
}

.terminal-container :deep(.xterm) {
  width: 100%;
  height: 100%;
}
</style>
