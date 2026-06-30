<template>
  <div class="panel">
    <div class="bar">
      <button class="small-button" @click="handleBack">{{ t("xtermPanel.back") }}</button>
    </div>
    <div class="terminal-wrapper">
      <div ref="terminalContainer" class="terminal-container"></div>
    </div>
  </div>
</template>

<script setup lang="ts">
// Web SSH 终端视图：基于 useTerminal composable 管理 xterm.js 实例与 WebSocket 连接。
// 修正 v2 缺陷 N1：使用结构化 JSON resize 消息替代 `\x1b[8;rows;cols t` 转义序列，
// 心跳与重连由 useWebSocket 管理（替代 v2 的 `\x00` 心跳与自管重连）。
// composable 内部注册 onUnmounted 清理（在 onMounted 内注册对当前组件实例有效）。
import { ref, onMounted, onUnmounted } from 'vue'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { useTerminal, getTerminalTheme } from '@/composables/useTerminal'
import { useNodesStore } from '@/stores/nodesStore'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

const router = useRouter()
const nodesStore = useNodesStore()
const { t } = useI18n()

const terminalContainer = ref<HTMLElement | null>(null)

// 无节点时的独立终端实例（仅用于显示欢迎信息），需手动清理。
let standaloneTerm: Terminal | null = null

const generateGreenText = (text: string) => `\x1B[32m${text}\x1B[0m`

const handleBack = () => {
  // useTerminal 场景由 composable 内部 onUnmounted 清理；组件卸载即触发。
  router.push({ name: 'cmds' })
}

onMounted(() => {
  if (!terminalContainer.value) return
  const node = nodesStore.currentXtremNode
  if (!node) {
    // 无选中节点：显示欢迎信息（保留 v2 行为），不建立 WebSocket 连接。
    standaloneTerm = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      theme: getTerminalTheme(),
    })
    standaloneTerm.open(terminalContainer.value)
    standaloneTerm.writeln(generateGreenText(t('xtermPanel.hello')))
    return
  }
  // 有节点：交由 useTerminal 管理终端实例、resize、心跳与输入透传。
  useTerminal(terminalContainer.value, node)
})

onUnmounted(() => {
  // 仅清理无节点场景的独立终端；useTerminal 场景由 composable 自行清理。
  if (standaloneTerm) {
    standaloneTerm.dispose()
    standaloneTerm = null
  }
})
</script>

<style scoped>
.panel {
  background-color: var(--color-terminal-bg, #002b36);
  color: var(--color-terminal-fg, #cce4f5);
  height: 100%;
  display: flex;
  flex-direction: column;
}

.bar {
  border-bottom: 1px solid var(--color-border, #073642);
  padding: 0.5rem;
  flex-shrink: 0;
}

.terminal-wrapper {
  flex: 1;
  position: relative;
  min-height: 0;
  background-color: var(--color-terminal-bg, #002b36);
}

.terminal-container {
  position: absolute;
  top: 8px;
  left: 8px;
  right: 8px;
  bottom: 8px;
}

.terminal-container :deep(.xterm) {
  width: 100%;
  height: 100%;
}

.small-button {
  color: var(--color-terminal-fg, #cce4f5);
  background-color: transparent;
  border: 1px solid var(--color-terminal-fg, #cce4f5);
  border-radius: 4px;
  padding: 0.25rem 0.5rem;
  cursor: pointer;
  font-size: 0.85rem;
}

.small-button:hover {
  background-color: var(--color-hover-bg);
}
</style>
