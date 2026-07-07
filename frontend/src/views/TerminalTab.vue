<template>
  <div class="terminal-tab">
    <div class="terminal-wrapper">
      <div ref="terminalContainer" class="terminal-container"></div>
    </div>
    <div class="terminal-toolbar">
      <span class="terminal-info">{{ node ? `${node.name} (${node.host}:${node.port})` : t('xtermPanel.idle') }}</span>
      <span v-if="connected" class="status connected">{{ t('finder.connected') }}</span>
      <span v-else class="status disconnected">{{ t('finder.disconnected') }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
// 终端标签页：每个标签独立维护 xterm.js 实例与 WebSocket 连接。
// 组件被隐藏（v-show）时不会卸载，连接保持后台运行。
import { ref, onMounted, onUnmounted, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { useTerminal, getTerminalTheme } from '@/composables/useTerminal'
import { useI18n } from 'vue-i18n'
import type { ApiNode } from '@/protocol/types'

const props = defineProps<{ node: ApiNode }>()

const { t } = useI18n()
const terminalContainer = ref<HTMLElement | null>(null)
const connected = ref(false)

let standaloneTerm: Terminal | null = null
let cleanup: (() => void) | null = null

const generateGreenText = (text: string) => `\x1B[32m${text}\x1B[0m`

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
  const { connected: wsConnected } = useTerminal(terminalContainer.value, props.node)
  connected.value = wsConnected.value
  cleanup = watch(wsConnected, (val) => {
    connected.value = val
  })
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

.status.disconnected {
  color: var(--color-red);
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
