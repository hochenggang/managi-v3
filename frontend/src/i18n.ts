// i18n：vue-i18n 配置，默认中文。
// 与 v2 i18n.ts 一致，locale 资源从 locales 加载（实现阶段迁移）。

import { createI18n } from 'vue-i18n'

// TODO(P0): 从 ../managi-frontend-vue3/src/locales 迁移 zh.json / en.json
const zh = {}
const en = {}

export const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh',
  fallbackLocale: 'en',
  messages: { zh, en },
})
