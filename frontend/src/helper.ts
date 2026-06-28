// 通知封装，与 v2 helper.ts 一致。

import { notify } from '@kyvg/vue3-notification'

export function handleMsg(value: string): void {
  notify({ type: 'success', text: value })
}

export function handleError(value: string): void {
  notify({ type: 'error', text: value })
}
