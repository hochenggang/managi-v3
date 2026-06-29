<template>
  <teleport to="body">
    <div
      v-if="visible"
      class="context-menu"
      :style="{ top: y + 'px', left: x + 'px' }"
      @click.stop
    >
      <div
        v-for="item in items"
        :key="item.label"
        class="context-menu-item"
        :class="{ danger: item.danger, disabled: item.disabled }"
        @click="handleClick(item)"
      >
        {{ item.label }}
      </div>
    </div>
    <div v-if="visible" class="context-menu-overlay" @click="close"></div>
  </teleport>
</template>

<script setup lang="ts">
export interface MenuItem {
  label: string
  action?: () => void
  danger?: boolean
  disabled?: boolean
}

const props = defineProps<{
  visible: boolean
  x: number
  y: number
  items: MenuItem[]
}>()

const emit = defineEmits<{ (e: 'update:visible', value: boolean): void }>()

function close(): void {
  emit('update:visible', false)
}

function handleClick(item: MenuItem): void {
  if (item.disabled) return
  item.action?.()
  close()
}
</script>

<style scoped>
.context-menu {
  position: fixed;
  min-width: 8rem;
  padding: 0.35rem 0;
  background-color: var(--color-menu-bg);
  border: 1px solid var(--color-border);
  border-radius: 6px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35);
  z-index: 2000;
}

.context-menu-item {
  padding: 0.45rem 0.75rem;
  font-size: 0.8rem;
  color: var(--color-font-1);
  cursor: pointer;
  transition: background-color 0.15s;
}

.context-menu-item:hover:not(.disabled) {
  background-color: var(--color-hover-bg);
}

.context-menu-item.danger {
  color: var(--color-red);
}

.context-menu-item.disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.context-menu-overlay {
  position: fixed;
  inset: 0;
  z-index: 1999;
}
</style>
