import { describe, it, expect } from 'vitest'
import router from '@/router'

describe('router', () => {
  it('has cmds route at /', () => {
    const route = router.getRoutes().find((r) => r.path === '/')
    expect(route).toBeDefined()
    expect(route?.name).toBe('cmds')
  })

  it('has xterm route at /xterm', () => {
    const route = router.getRoutes().find((r) => r.path === '/xterm')
    expect(route).toBeDefined()
    expect(route?.name).toBe('xterm')
  })

  it('has exactly 2 routes', () => {
    expect(router.getRoutes()).toHaveLength(2)
  })
})
