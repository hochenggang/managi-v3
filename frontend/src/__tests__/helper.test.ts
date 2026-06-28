import { describe, it, expect, beforeEach, vi } from 'vitest'

// vi.hoisted 确保 mockNotify 在 vi.mock 工厂执行前已初始化
const { mockNotify } = vi.hoisted(() => ({
  mockNotify: vi.fn(),
}))

vi.mock('@kyvg/vue3-notification', () => ({
  notify: mockNotify,
}))

import { handleMsg, handleError } from '@/helper'

describe('helper', () => {
  beforeEach(() => {
    mockNotify.mockClear()
  })

  it('handleMsg calls notify with type=success', () => {
    handleMsg('done')
    expect(mockNotify).toHaveBeenCalledTimes(1)
    expect(mockNotify).toHaveBeenCalledWith({ type: 'success', text: 'done' })
  })

  it('handleError calls notify with type=error', () => {
    handleError('boom')
    expect(mockNotify).toHaveBeenCalledTimes(1)
    expect(mockNotify).toHaveBeenCalledWith({ type: 'error', text: 'boom' })
  })

  it('handleMsg/handleError pass through multi-byte strings', () => {
    handleMsg('成功')
    handleError('失败')
    expect(mockNotify).toHaveBeenNthCalledWith(1, { type: 'success', text: '成功' })
    expect(mockNotify).toHaveBeenNthCalledWith(2, { type: 'error', text: '失败' })
  })
})
