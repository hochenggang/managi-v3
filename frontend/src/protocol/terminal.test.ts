import { describe, it, expect } from 'vitest'
import { isControlMessage, resizeMessage } from './terminal'

describe('isControlMessage', () => {
  it('returns true for full resize message', () => {
    expect(isControlMessage('{"type":"resize","cols":80,"rows":24}')).toBe(true)
  })
  it('returns true for truncated prefix (prefix match)', () => {
    expect(isControlMessage('{"type":"resize"')).toBe(true)
  })
  it('returns false for non-resize JSON', () => {
    expect(isControlMessage('{"type":"input","data":"hi"}')).toBe(false)
  })
  it('returns false for empty string', () => {
    expect(isControlMessage('')).toBe(false)
  })
  it('returns false for plain text', () => {
    expect(isControlMessage('ls -la')).toBe(false)
  })
})

describe('resizeMessage', () => {
  it('produces valid JSON with resize type', () => {
    const msg = resizeMessage(80, 24)
    const parsed = JSON.parse(msg)
    expect(parsed.type).toBe('resize')
    expect(parsed.cols).toBe(80)
    expect(parsed.rows).toBe(24)
  })
  it('produces different messages for different sizes', () => {
    expect(resizeMessage(120, 40)).not.toBe(resizeMessage(80, 24))
  })
})
