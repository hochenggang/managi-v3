// Pinia store：全局多标签页管理。
// 允许批量执行、终端、SFTP、设置等视图以独立标签同时存在；
// 通过保留所有标签组件实例实现连接后台保活。

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { i18n } from '@/i18n'
import type { ApiNode } from '@/protocol/types'

export type TabType = 'welcome' | 'batch' | 'terminal' | 'sftp' | 'settings'

export interface TabItem {
  id: string
  type: TabType
  title: string
  icon?: string
  props?: Record<string, unknown>
}

let idCounter = 0

function makeId(): string {
  return `tab-${Date.now()}-${++idCounter}`
}

export const useTabsStore = defineStore('tabs', () => {
  const tabs = ref<TabItem[]>([])
  const activeTabId = ref<string>('')

  const activeTab = computed(() => tabs.value.find((t) => t.id === activeTabId.value) || null)

  function activate(id: string): void {
    if (tabs.value.some((t) => t.id === id)) {
      activeTabId.value = id
    }
  }

  function add(tab: Omit<TabItem, 'id'>, options?: { activate?: boolean }): TabItem {
    const item: TabItem = { ...tab, id: makeId() }
    tabs.value.push(item)
    if (options?.activate !== false) {
      activeTabId.value = item.id
    }
    return item
  }

  function openBatch(): TabItem {
    const existing = tabs.value.find((t) => t.type === 'batch')
    if (existing) {
      activeTabId.value = existing.id
      return existing
    }
    return add({ type: 'batch', title: i18n.global.t('tabs.batch'), icon: 'batch' })
  }

  function openTerminal(node: ApiNode): TabItem {
    const existing = tabs.value.find(
      (t) => t.type === 'terminal' && (t.props?.node as ApiNode | undefined)?.host === node.host,
    )
    if (existing) {
      activeTabId.value = existing.id
      return existing
    }
    return add({ type: 'terminal', title: `${node.name}`, icon: 'terminal', props: { node } })
  }

  function openSftp(node: ApiNode): TabItem {
    const existing = tabs.value.find(
      (t) => t.type === 'sftp' && (t.props?.node as ApiNode | undefined)?.host === node.host,
    )
    if (existing) {
      activeTabId.value = existing.id
      return existing
    }
    return add({ type: 'sftp', title: `${node.name} ${i18n.global.t('tabs.sftp')}`, icon: 'sftp', props: { node } })
  }

  function openSettings(): TabItem {
    const existing = tabs.value.find((t) => t.type === 'settings')
    if (existing) {
      activeTabId.value = existing.id
      return existing
    }
    return add({ type: 'settings', title: i18n.global.t('tabs.settings'), icon: 'settings' })
  }

  function close(id: string): void {
    const idx = tabs.value.findIndex((t) => t.id === id)
    if (idx === -1) return
    tabs.value.splice(idx, 1)
    if (activeTabId.value === id) {
      const next = tabs.value[Math.max(0, idx - 1)] || tabs.value[idx] || null
      activeTabId.value = next?.id ?? ''
    }
  }

  function closeOthers(id: string): void {
    const keep = tabs.value.find((t) => t.id === id)
    if (!keep) return
    tabs.value = [keep]
    activeTabId.value = keep.id
  }

  function closeAll(): void {
    tabs.value = []
    activeTabId.value = ''
  }

  return {
    tabs,
    activeTabId,
    activeTab,
    activate,
    add,
    openBatch,
    openTerminal,
    openSftp,
    openSettings,
    close,
    closeOthers,
    closeAll,
  }
})
