<template>
  <main class="settings-view">
    <aside class="settings-nav">
      <button
        v-for="section in sections"
        :key="section.key"
        class="nav-item"
        :class="{ active: currentSection === section.key }"
        @click="currentSection = section.key"
      >
        {{ section.label }}
      </button>
    </aside>

    <section class="settings-content">
      <div v-if="currentSection === 'appearance'" class="settings-group">
        <h2>{{ t('settings.appearance.title') }}</h2>
        <div class="setting-item">
          <label>{{ t('settings.appearance.theme') }}</label>
          <div class="theme-options">
            <button
              v-for="theme in themes"
              :key="theme.key"
              class="theme-card"
              :class="{ active: settingsStore.settings.theme === theme.key }"
              @click="settingsStore.setTheme(theme.key as ThemeName)"
            >
              <span class="theme-preview" :style="{ background: theme.preview }"></span>
              <span>{{ theme.label }}</span>
            </button>
          </div>
        </div>
        <div class="setting-item">
          <label>{{ t('settings.appearance.language') }}</label>
          <select v-model="settingsStore.settings.language" @change="handleLanguageChange">
            <option value="zh">中文</option>
            <option value="en">English</option>
          </select>
        </div>
      </div>

      <div v-if="currentSection === 'terminal'" class="settings-group">
        <h2>{{ t('settings.terminal.title') }}</h2>
        <div class="setting-item">
          <label>{{ t('settings.terminal.fontSize') }}</label>
          <input type="number" v-model.number="settingsStore.settings.terminalFontSize" min="8" max="32" />
        </div>
        <div class="setting-item">
          <label>{{ t('settings.terminal.fontFamily') }}</label>
          <input type="text" v-model="settingsStore.settings.terminalFontFamily" />
        </div>
      </div>

      <div v-if="currentSection === 'security'" class="settings-group">
        <h2>{{ t('settings.security.title') }}</h2>
        <p class="setting-desc">{{ t('settings.security.desc') }}</p>
      </div>

      <div v-if="currentSection === 'about'" class="settings-group">
        <h2>{{ t('settings.about.title') }}</h2>
        <p class="setting-desc">Managi v3</p>
        <p class="setting-desc">{{ t('settings.about.desc') }}</p>
      </div>

      <div v-if="currentSection === 'data'" class="settings-group">
        <h2>{{ t('settings.data.title') }}</h2>
        <p class="setting-desc">{{ t('settings.data.desc') }}</p>
        <div class="setting-actions">
          <button class="small-button" @click="exportConfig">{{ t('settings.data.export') }}</button>
          <button class="small-button" @click="importConfig">{{ t('settings.data.import') }}</button>
        </div>
      </div>
    </section>
  </main>
</template>

<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSettingsStore, type ThemeName, type Settings } from '@/stores/settingsStore'
import { useNodesStore } from '@/stores/nodesStore'
import { useShortcutsStore } from '@/stores/shortcutsStore'
import { handleError, handleMsg } from '@/helper'
import type { ApiNode, AppConfig, OldApiNode, ShortcutItem } from '@/protocol/types'
import { oldApiNodeConvert } from '@/api'

const { t, locale } = useI18n()
const settingsStore = useSettingsStore()
const nodesStore = useNodesStore()
const shortcutsStore = useShortcutsStore()

const currentSection = ref('appearance')

const sections = computed(() => {
  // eslint-disable-next-line @typescript-eslint/no-unused-expressions
  locale.value
  return [
    { key: 'appearance', label: t('settings.appearance.title') },
    { key: 'terminal', label: t('settings.terminal.title') },
    // { key: 'security', label: t('settings.security.title') },
    { key: 'data', label: t('settings.data.title') },
    { key: 'about', label: t('settings.about.title') },
  ]
})

const themes = [
  { key: 'nord', label: 'Nord', preview: '#2E3440' },
  { key: 'one-dark-pro', label: 'One Dark Pro', preview: '#282C34' },
  { key: 'catppuccin-mocha', label: 'Catppuccin Mocha', preview: '#1E1E2E' },
  { key: 'gruvbox-dark', label: 'Gruvbox Dark', preview: '#282828' },
  { key: 'solarized-dark', label: 'Solarized Dark', preview: '#002B36' },
  { key: 'monokai', label: 'Monokai', preview: '#272822' },
  { key: 'github-dark', label: 'GitHub Dark', preview: '#0D1117' },
  { key: 'github-light', label: 'GitHub Light', preview: '#FFFFFF' },
]

function exportConfig(): void {
  shortcutsStore.ensureLoaded()
  const config: AppConfig = {
    version: 3,
    nodes: nodesStore.nodes,
    groups: nodesStore.groups,
    shortcuts: shortcutsStore.shortcuts,
    settings: { ...settingsStore.settings },
  }
  const blob = new Blob([JSON.stringify(config, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `managi-config-${new Date().getTime()}.json`
  a.click()
  URL.revokeObjectURL(url)
  handleMsg(t('settings.data.exportSuccess'))
}

function importConfig(): void {
  const input = document.createElement('input')
  input.type = 'file'
  input.accept = 'application/json'
  input.click()
  input.onchange = () => {
    const file = input.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = () => {
      try {
        const raw = JSON.parse(reader.result as string)
        if (typeof raw !== 'object' || raw === null) {
          handleError(t('settings.data.importError'))
          return
        }

        const isV3Config = Object.prototype.hasOwnProperty.call(raw, 'nodes')
        const inputNodes = isV3Config
          ? (raw.nodes as Record<string, ApiNode | OldApiNode>)
          : (raw as Record<string, ApiNode | OldApiNode>)
        const inputShortcuts = isV3Config ? (raw.shortcuts as ShortcutItem[] | undefined) : undefined
        const inputGroups = isV3Config ? (raw.groups as string[] | undefined) : undefined
        const inputSettings = isV3Config ? (raw.settings as Partial<Settings> | undefined) : undefined

        for (const key1 in inputNodes) {
          const requiredKeys = ['port', 'auth_type', 'auth_value']
          for (const key2 of requiredKeys) {
            if (!Object.prototype.hasOwnProperty.call(inputNodes[key1], key2)) {
              handleError(`${t('settings.data.importError')} -> [${key1}].${key2}`)
              return
            }
          }
        }

        if (inputShortcuts !== undefined) {
          if (!Array.isArray(inputShortcuts)) {
            handleError(t('settings.data.importError'))
            return
          }
          for (let i = 0; i < inputShortcuts.length; i++) {
            const sc = inputShortcuts[i]
            if (typeof sc?.label !== 'string' || typeof sc?.cmd !== 'string') {
              handleError(`${t('settings.data.importError')} -> shortcuts[${i}]`)
              return
            }
          }
        }

        if (inputSettings !== undefined && (typeof inputSettings !== 'object' || inputSettings === null)) {
          handleError(t('settings.data.importError'))
          return
        }

        nodesStore.setAllNodes(Object.values(inputNodes).map(oldApiNodeConvert), inputGroups)
        if (inputShortcuts) {
          shortcutsStore.setAll(inputShortcuts)
        }
        if (inputSettings) {
          settingsStore.importSettings(inputSettings)
        }

        handleMsg(t('settings.data.importSuccess'))
      } catch (error) {
        handleError(`${t('settings.data.importError')} ${error}`)
      }
    }
    reader.readAsText(file)
  }
}

function handleLanguageChange(): void {
  locale.value = settingsStore.settings.language
  localStorage.setItem('lang', settingsStore.settings.language)
}

watch(
  () => settingsStore.settings.language,
  (lang) => {
    locale.value = lang
    localStorage.setItem('lang', lang)
  },
)
</script>

<style scoped>
.settings-view {
  display: flex;
  height: 100%;
  background-color: var(--color-bg);
  overflow: hidden;
}

.settings-nav {
  width: 12rem;
  border-right: 1px solid var(--color-border);
  background-color: var(--color-panel-bg);
  flex-shrink: 0;
  padding: 0.5rem;
}

.nav-item {
  width: 100%;
  text-align: left;
  padding: 0.5rem 0.75rem;
  margin-bottom: 0.25rem;
  border: none;
  border-radius: 0; 
  background: transparent;
  color: var(--color-font-2);
  font-size: 0.9rem;
  cursor: pointer;
}

.nav-item.active,
.nav-item:hover {
  background-color: var(--color-hover-bg);
  color: var(--color-font-1);
}

.nav-item.active {
  color: var(--color-accent);
}

.settings-content {
  flex: 1;
  padding: 1rem;
  overflow-y: auto;
}

.settings-group h2 {
  font-size: 1.1rem;
  margin-bottom: 1rem;
  color: var(--color-font-1);
  text-align: left;
}

.setting-item {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  margin-bottom: 1rem;
  max-width: 30rem;
}

.setting-item label {
  font-size: 0.85rem;
  color: var(--color-font-2);
}

.setting-item input,
.setting-item select {
  padding: 0.4rem 0.5rem;
  background-color: var(--color-input-bg);
  border: 1px solid var(--color-border);
  border-radius: 0; 
  color: var(--color-font-1);
  font-size: 0.9rem;
  text-align: left;
}

.theme-options {
  display: flex;
  gap: 0.75rem;
  flex-wrap: wrap;
}

.theme-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.35rem;
  width: 5rem;
  padding: 0.5rem;
  border: 1px solid var(--color-border);
  border-radius: 0;
  background-color: var(--color-panel-bg);
  color: var(--color-font-1);
  font-size: 0.8rem;
  cursor: pointer;
}

.theme-card.active {
  border-color: var(--color-accent);
  background-color: var(--color-selected-bg);
}

.theme-preview {
  width: 100%;
  height: 2.5rem;
  border-radius: 0;
  border: 1px solid var(--color-border);
}

.setting-desc {
  color: var(--color-font-3);
  font-size: 0.85rem;
  line-height: 1.5;
}

.setting-actions {
  display: flex;
  gap: 0.75rem;
  margin-top: 1rem;
}
</style>
