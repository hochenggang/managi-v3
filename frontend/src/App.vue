<script setup lang="ts">
// 根布局组件：左侧节点列表 + 右侧多标签工作区。
// 标签页通过 tabsStore 管理；所有标签组件实例保持挂载（v-show），以实现后台连接保活。
import { onBeforeMount } from 'vue'
import NodeList from '@/components/NodeList.vue'
import TabBar from '@/components/TabBar.vue'
import CmdsView from '@/views/CmdsView.vue'
import TerminalTab from '@/views/TerminalTab.vue'
import SftpTab from '@/views/SftpTab.vue'
import SettingsView from '@/views/SettingsView.vue'
import { useSidebar } from '@/composables/useSidebar'
import { useTabsStore } from '@/stores/tabsStore'
import { useSettingsStore } from '@/stores/settingsStore'
import { i18n } from '@/i18n'
import type { TabItem } from '@/stores/tabsStore'

const { width } = useSidebar()
const tabsStore = useTabsStore()
const settingsStore = useSettingsStore()

function resolveComponent(type: string) {
  switch (type) {
    case 'batch':
      return CmdsView
    case 'terminal':
      return TerminalTab
    case 'sftp':
      return SftpTab
    case 'settings':
      return SettingsView
    default:
      return CmdsView
  }
}

onBeforeMount(() => {
  settingsStore.setTheme(settingsStore.settings.theme)
  i18n.global.locale.value = settingsStore.settings.language
  localStorage.setItem('lang', settingsStore.settings.language)
  if (tabsStore.tabs.length === 0) {
    tabsStore.openBatch()
  }
})
</script>

<template>
  <notifications position="top right" />
  <div class="app-root" :style="{ '--sidebar-width': width }">
    <NodeList />
    <main class="workspace">
      <TabBar />
      <div class="tab-panels">
        <div
          v-for="tab in tabsStore.tabs"
          :key="tab.id"
          class="tab-panel"
          :class="{ active: tab.id === tabsStore.activeTabId }"
        >
          <component :is="resolveComponent(tab.type)" v-bind="tab.props ?? {}" />
        </div>
        <div v-if="tabsStore.tabs.length === 0" class="empty-tabs">
          {{ $t('tabs.empty') }}
        </div>
      </div>
    </main>
  </div>
</template>

<style>
.app-root {
  display: flex;
  position: fixed;
  inset: 0;
  background-color: var(--color-bg);
  color: var(--color-font-1);
}

.workspace {
  display: flex;
  flex-direction: column;
  flex: 1;
  margin-left: var(--sidebar-width, 20rem);
  transition: margin-left 0.2s ease-in-out;
  min-width: 0;
}

.tab-panels {
  flex: 1;
  position: relative;
  min-height: 0;
  overflow: hidden;
}

.tab-panel {
  position: absolute;
  inset: 0;
  display: none;
  overflow: hidden;
}

.tab-panel.active {
  display: flex;
  flex-direction: column;
}

.empty-tabs {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--color-font-3);
}
</style>
