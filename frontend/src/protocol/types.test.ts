import { describe, it, expect } from 'vitest'
import { generateNodeId, type ApiNode } from './types'

describe('generateNodeId', () => {
  const base: ApiNode = {
    name: 'test',
    host: '',
    port: 0,
    username: 'root',
    auth_type: 'password',
    auth_value: 'pass',
  }

  it('returns host:port format', () => {
    expect(generateNodeId({ ...base, host: '1.2.3.4', port: 22 })).toBe('1.2.3.4:22')
  })

  it('returns host:port for different port', () => {
    expect(generateNodeId({ ...base, host: 'example.com', port: 18001 })).toBe('example.com:18001')
  })

  it('is consistent for same node', () => {
    const node = { ...base, host: '10.0.0.1', port: 2222 }
    expect(generateNodeId(node)).toBe(generateNodeId(node))
  })
})
