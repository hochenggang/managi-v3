import { describe, it, expect } from 'vitest'
import { loginMessage, inputMessage, resizeMessage, chunkInput, INPUT_CHUNK_CHARS } from './terminal'
import { parseWSMessage } from './ws'
import type { ApiNode } from './types'

const node: ApiNode = {
  name: 'n1',
  host: '1.2.3.4',
  port: 22,
  username: 'root',
  auth_type: 'password',
  auth_value: 'pwd',
}

describe('loginMessage', () => {
  it('produces envelope with type=login and node as data', () => {
    const msg = parseWSMessage(loginMessage(node))
    expect(msg).not.toBeNull()
    expect(msg!.type).toBe('login')
    expect(msg!.data).toEqual(node)
  })
})

describe('inputMessage', () => {
  it('produces envelope with type=msg and string data', () => {
    const msg = parseWSMessage(inputMessage('ls -la\n'))
    expect(msg).not.toBeNull()
    expect(msg!.type).toBe('msg')
    expect(msg!.data).toBe('ls -la\n')
  })
})

describe('resizeMessage', () => {
  it('produces envelope with type=resize and cols/rows data', () => {
    const msg = parseWSMessage(resizeMessage(120, 40))
    expect(msg).not.toBeNull()
    expect(msg!.type).toBe('resize')
    expect(msg!.data).toEqual({ cols: 120, rows: 40 })
  })

  it('produces different messages for different sizes', () => {
    expect(resizeMessage(120, 40)).not.toBe(resizeMessage(80, 24))
  })
})

// 整段粘贴只有一次 onData 回调，必须分帧：单帧超过后端 ReadLimit 即以 1009 掐断会话。
describe('chunkInput', () => {
  it('keeps short input as a single frame', () => {
    expect(chunkInput('ls -la\n')).toEqual(['ls -la\n'])
    expect(chunkInput('')).toEqual([''])
  })

  it('does not split input exactly at max', () => {
    const data = 'a'.repeat(INPUT_CHUNK_CHARS)
    expect(chunkInput(data)).toEqual([data])
  })

  it('splits long input in order, each part within max', () => {
    const data = 'a'.repeat(INPUT_CHUNK_CHARS * 2 + 500)
    const parts = chunkInput(data)
    expect(parts).toHaveLength(3)
    expect(parts.join('')).toBe(data)
    parts.forEach((p) => expect(p.length).toBeLessThanOrEqual(INPUT_CHUNK_CHARS))
  })

  it('honours a custom max', () => {
    expect(chunkInput('abcdef', 2)).toEqual(['ab', 'cd', 'ef'])
  })

  // max 被传成 0/1 时，代理对回退会把切点退到 0：rest 永不缩短，表现为页面卡死而不是报错
  it('clamps degenerate max instead of looping forever', () => {
    expect(chunkInput('abc', 1)).toEqual(['a', 'b', 'c'])
    expect(chunkInput('ab', 0)).toEqual(['a', 'b'])
    expect(chunkInput('abcdef', -5)).toEqual(['a', 'b', 'c', 'd', 'e', 'f'])
    expect(chunkInput('abcd', 2.5)).toEqual(['ab', 'cd'])
  })

  // 切点落在代理对中间时，孤立码元经 JSON 序列化会变成 U+FFFD
  it('never cuts between a surrogate pair', () => {
    const data = 'x'.repeat(INPUT_CHUNK_CHARS - 1) + '\u{1F600}' + 'tail'
    const parts = chunkInput(data)
    expect(parts.join('')).toBe(data)
    parts.forEach((p) => {
      const head = p.charCodeAt(0)
      const tail = p.charCodeAt(p.length - 1)
      expect(head >= 0xdc00 && head <= 0xdfff).toBe(false)
      expect(tail >= 0xd800 && tail <= 0xdbff).toBe(false)
    })
  })

  it('keeps emoji-only long text lossless', () => {
    const data = '\u{1F600}'.repeat(5000)
    const parts = chunkInput(data, 101)
    expect(parts.join('')).toBe(data)
    expect(parts.every((p) => p.length <= 101)).toBe(true)
  })
})
