import { describe, it, expect, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useSettingsStore, TERMINAL_FONT_OPTIONS } from '@/stores/settingsStore'

const STORAGE_KEY = 'managi-settings'

// 最后一项是「系统字体」，其余内嵌字体都必须以它为兜底
const SYSTEM_FONT_FAMILY = TERMINAL_FONT_OPTIONS[TERMINAL_FONT_OPTIONS.length - 1].fontFamily

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('终端字体选项', () => {
  it('四个选项，全部以系统字体栈兜底', () => {
    expect(TERMINAL_FONT_OPTIONS).toHaveLength(4)
    for (const option of TERMINAL_FONT_OPTIONS) {
      expect(option.fontFamily.endsWith(SYSTEM_FONT_FAMILY)).toBe(true)
    }
  })

  it('前三项是内嵌字体，排在兜底栈之前', () => {
    const embedded = TERMINAL_FONT_OPTIONS.slice(0, 3).map((o) => o.fontFamily)
    expect(embedded.every((f) => f.startsWith("'Managi "))).toBe(true)
  })

  it('没有存档时使用清单第一项', () => {
    expect(useSettingsStore().settings.terminalFontFamily).toBe(TERMINAL_FONT_OPTIONS[0].fontFamily)
  })
})

describe('旧存档里的自定义字体串', () => {
  it('不在清单内则归到默认值，避免下拉渲染成空白', () => {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ terminalFontFamily: "'JetBrains Mono', 'Fira Code', monospace" }),
    )
    setActivePinia(createPinia())
    expect(useSettingsStore().settings.terminalFontFamily).toBe(TERMINAL_FONT_OPTIONS[0].fontFamily)
  })

  it('setter 忽略清单外的字体族', () => {
    const store = useSettingsStore()
    store.setTerminalFontFamily("'Comic Sans MS', cursive")
    expect(store.settings.terminalFontFamily).toBe(TERMINAL_FONT_OPTIONS[0].fontFamily)
  })
})
