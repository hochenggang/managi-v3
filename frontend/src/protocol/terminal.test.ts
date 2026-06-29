import { describe, it, expect } from 'vitest'
import { loginMessage, inputMessage, resizeMessage } from './terminal'
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
