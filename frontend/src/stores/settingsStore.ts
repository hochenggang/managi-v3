// Pinia store：全局用户设置。
// 支持主题、界面语言、终端字体大小等持久化。

import { defineStore } from 'pinia'
import { ref, watch } from 'vue'

export type ThemeName = 'nord' | 'nord-light' | 'dracula' | 'tokyo-night'

export interface Settings {
  theme: ThemeName
  language: 'zh' | 'en'
  terminalFontSize: number
  terminalFontFamily: string
}

const STORAGE_KEY = 'managi-settings'

const defaults: Settings = {
  theme: 'nord',
  language: 'zh',
  terminalFontSize: 14,
  terminalFontFamily: "'JetBrains Mono', 'Fira Code', monospace",
}

function loadSettings(): Settings {
  const raw = localStorage.getItem(STORAGE_KEY)
  if (!raw) return { ...defaults }
  try {
    const parsed = JSON.parse(raw) as Partial<Settings>
    return { ...defaults, ...parsed }
  } catch {
    return { ...defaults }
  }
}

export const useSettingsStore = defineStore('settings', () => {
  const settings = ref<Settings>(loadSettings())

  function setTheme(theme: ThemeName): void {
    settings.value.theme = theme
    applyTheme(theme)
  }

  function setLanguage(language: 'zh' | 'en'): void {
    settings.value.language = language
  }

  function setTerminalFontSize(size: number): void {
    settings.value.terminalFontSize = Math.max(8, Math.min(32, size))
  }

  function setTerminalFontFamily(family: string): void {
    settings.value.terminalFontFamily = family
  }

  function importSettings(partial: Partial<Settings>): void {
    if (isValidTheme(partial.theme)) {
      setTheme(partial.theme)
    }
    if (partial.language === 'zh' || partial.language === 'en') {
      setLanguage(partial.language)
    }
    if (typeof partial.terminalFontSize === 'number') {
      setTerminalFontSize(partial.terminalFontSize)
    }
    if (typeof partial.terminalFontFamily === 'string') {
      setTerminalFontFamily(partial.terminalFontFamily)
    }
  }

  function reset(): void {
    settings.value = { ...defaults }
    applyTheme(defaults.theme)
  }

  watch(
    settings,
    (val) => {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(val))
    },
    { deep: true },
  )

  // 初始化时应用主题
  applyTheme(settings.value.theme)

  return {
    settings,
    setTheme,
    setLanguage,
    setTerminalFontSize,
    setTerminalFontFamily,
    importSettings,
    reset,
  }
})

function applyTheme(theme: ThemeName): void {
  document.documentElement.classList.remove('theme-nord', 'theme-nord-light', 'theme-dracula', 'theme-tokyo-night')
  document.documentElement.classList.add(`theme-${theme}`)
}

function isValidTheme(theme: ThemeName | undefined): theme is ThemeName {
  return (
    theme === 'nord' ||
    theme === 'nord-light' ||
    theme === 'dracula' ||
    theme === 'tokyo-night'
  )
}
