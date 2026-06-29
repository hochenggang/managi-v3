<template>
  <div class="quick-command-panel">
    <div class="command-search">
      <input v-model="search" type="text" :placeholder="t('quickCommand.searchPlaceholder')" />
    </div>
    <div class="command-categories">
      <button
        v-for="cat in categories"
        :key="cat.key"
        class="category-tab"
        :class="{ active: currentCategory === cat.key }"
        @click="currentCategory = cat.key"
      >
        {{ cat.label }}
      </button>
    </div>
    <div class="command-list">
      <div
        v-for="cmd in filteredCommands"
        :key="cmd.label"
        class="command-item"
        @click="emit('select', cmd.cmd)"
        @contextmenu.prevent="handleCommandContextMenu($event, cmd)"
        :title="cmd.cmd"
      >
        <div class="command-meta">
          <div class="command-label">{{ cmd.label }}</div>
          <div class="command-preview">{{ cmd.cmd }}</div>
        </div>
      </div>
      <div v-if="filteredCommands.length === 0" class="command-empty">
        {{ t('quickCommand.noResult') }}
      </div>
    </div>
  </div>

  <ContextMenu v-model:visible="contextMenu.visible" :x="contextMenu.x" :y="contextMenu.y" :items="contextMenu.items" />
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import ContextMenu from '@/components/ContextMenu.vue'

const props = defineProps<{
  customCommands: { label: string; cmd: string }[]
}>()

const emit = defineEmits<{
  (e: 'select', cmd: string): void
  (e: 'rename', index: number): void
  (e: 'delete', index: number): void
}>()

interface CommandItem {
  category: string
  label: string
  cmd: string
  originalIndex?: number
}

const { t, locale } = useI18n()

const search = ref('')
const currentCategory = ref('system')
const contextMenu = ref({ visible: false, x: 0, y: 0, items: [] as { label: string; action?: () => void; danger?: boolean }[] })

const categories = computed(() => {
  // eslint-disable-next-line @typescript-eslint/no-unused-expressions
  locale.value
  return [
    { key: 'system', label: t('quickCommand.categories.system') },
    { key: 'network', label: t('quickCommand.categories.network') },
    { key: 'text', label: t('quickCommand.categories.text') },
    { key: 'custom', label: t('quickCommand.categories.custom') },
  ]
})

interface BuiltInCommand {
  category: string
  label: string
  cmd: string
}

const builtInCommands = computed<BuiltInCommand[]>(() => {
  // eslint-disable-next-line @typescript-eslint/no-unused-expressions
  locale.value
  return [
    { category: 'system', label: t('quickCommand.cmds.systemInfo'), cmd: 'uname -a && lsb_release -a 2>/dev/null || cat /etc/os-release' },
    { category: 'system', label: t('quickCommand.cmds.cpu'), cmd: 'top -bn1 | grep "Cpu(s)"' },
    { category: 'system', label: t('quickCommand.cmds.memory'), cmd: 'free -h' },
    { category: 'system', label: t('quickCommand.cmds.disk'), cmd: 'df -h' },
    { category: 'system', label: t('quickCommand.cmds.process'), cmd: 'ps aux --sort=-%cpu | head -20' },
    { category: 'system', label: t('quickCommand.cmds.uptime'), cmd: 'uptime' },
    { category: 'network', label: t('quickCommand.cmds.ip'), cmd: 'ip addr' },
    { category: 'network', label: t('quickCommand.cmds.route'), cmd: 'ip route' },
    { category: 'network', label: t('quickCommand.cmds.ping'), cmd: 'ping -c 4 8.8.8.8' },
    { category: 'network', label: t('quickCommand.cmds.ports'), cmd: 'ss -tlnp' },
    { category: 'text', label: t('quickCommand.cmds.findLargeFiles'), cmd: "find / -type f -size +100M -exec ls -lh {} \\; 2>/dev/null | head -10" },
    { category: 'text', label: t('quickCommand.cmds.tailLog'), cmd: 'tail -n 50 /var/log/syslog' },
    { category: 'text', label: t('quickCommand.cmds.grep'), cmd: 'grep -rn "TODO" /etc 2>/dev/null | head -20' },
  ]
})

const filteredCommands = computed<CommandItem[]>(() => {
  const all: CommandItem[] = [
    ...builtInCommands.value,
    ...props.customCommands.map((c, idx) => ({ category: 'custom', label: c.label, cmd: c.cmd, originalIndex: idx })),
  ]
  return all.filter((cmd) => {
    const matchCategory = currentCategory.value === 'custom' ? cmd.category === 'custom' : cmd.category === currentCategory.value
    const q = search.value.trim().toLowerCase()
    const matchSearch = !q || cmd.label.toLowerCase().includes(q) || cmd.cmd.toLowerCase().includes(q)
    return matchCategory && matchSearch
  })
})

function handleCommandContextMenu(event: MouseEvent, cmd: CommandItem): void {
  if (cmd.category !== 'custom' || cmd.originalIndex === undefined) return
  const index = cmd.originalIndex
  contextMenu.value = {
    visible: true,
    x: event.clientX,
    y: event.clientY,
    items: [
      { label: t('cmdPanel.rename'), action: () => emit('rename', index) },
      { label: t('cmdPanel.delete'), danger: true, action: () => emit('delete', index) },
    ],
  }
}
</script>

<style scoped>
.quick-command-panel {
  display: flex;
  flex-direction: column;
  width: 18rem;
  border-right: 1px solid var(--color-border);
  background-color: var(--color-panel-bg);
  flex-shrink: 0;
}

.command-search {
  padding: 0.35rem;
  border-bottom: 1px solid var(--color-border);
}

.command-search input {
  width: 100%;
  padding: 0.25rem 0.4rem;
  font-size: 0.8rem;
  background-color: var(--color-input-bg);
  border: 1px solid var(--color-border);
  border-radius: 4px;
  color: var(--color-font-1);
}

.command-categories {
  display: flex;
  padding: 0 0.25rem;
}

.category-tab {
  flex: 1;
  padding: 0.3rem 0;
  font-size: 0.75rem;
  background: transparent;
  border: none;
  border-radius: 0px;
  color: var(--color-font-2);
  cursor: pointer;
}

.category-tab.active {
  position: relative;
  color: var(--color-accent);
}

.category-tab.active::after {
    content: '';
    position: absolute;
    bottom: 3px;
    left: 50%;
    transform: translateX(-50%);
    display: block;
    width: 40%;
    height: 2px;
    background-color: var(--color-accent);
  }

.command-list {
  flex: 1;
  overflow-y: auto;
  padding: 0.25rem;
}

.command-item {
  display: flex;
  align-items: center;
  padding: 0.3rem 0.4rem;
  border-radius: 4px;
  cursor: pointer;
  transition: background-color 0.15s;
}

.command-item:hover {
  background-color: var(--color-hover-bg);
}

.command-meta {
  min-width: 0;
}

.command-label {
  font-size: 0.8rem;
  color: var(--color-font-1);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.command-preview {
  font-size: 0.7rem;
  color: var(--color-font-3);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.command-empty {
  padding: 1rem;
  text-align: center;
  color: var(--color-font-3);
  font-size: 0.85rem;
}
</style>
