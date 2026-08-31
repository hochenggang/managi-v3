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

  it('returns host:port:username format', () => {
    expect(generateNodeId({ ...base, host: '1.2.3.4', port: 22 })).toBe('1.2.3.4:22:root')
  })

  it('returns host:port:username for different port', () => {
    expect(generateNodeId({ ...base, host: 'example.com', port: 18001 })).toBe('example.com:18001:root')
  })

  it('is consistent for same node', () => {
    const node = { ...base, host: '10.0.0.1', port: 2222 }
    expect(generateNodeId(node)).toBe(generateNodeId(node))
  })

  it('distinguishes same host:port with different usernames', () => {
    // 同主机同端口但不同用户名是不同节点，ID 必须不同，否则会互相覆盖
    const a = { ...base, host: '10.0.0.1', port: 22, username: 'root' }
    const b = { ...base, host: '10.0.0.1', port: 22, username: 'deploy' }
    expect(generateNodeId(a)).not.toBe(generateNodeId(b))
  })
})
