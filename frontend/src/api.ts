// API 客户端：HTTP 请求封装，含重试与超时。
// 修复 v2 缺陷：网络响应丢失（无重试）。设计见 ../../../design-v3.md §6.2。

import { useRetry } from '@/composables/useRetry'
import type { ApiNode, BatchCmdRequest, CmdsTestResult, OldApiNode } from '@/protocol/types'

const API_URI = {
  sshTest: '/api/ssh/test',
  sshBatch: '/api/ssh/batch',
  sftpDownload: '/api/sftp/download',
} as const

/** getApiBase 推导当前部署的 HTTP API 基址（含协议+主机+端口）。
 *  修复 R5：与 useWebSocket.getWsHost 共享同一推导逻辑，避免两处重复实现漂移。
 */
export function getApiBase(): string {
  const stored = localStorage.getItem('managi-api-host')
  if (stored) return `${location.protocol}//${stored}`
  // https 保留非默认端口（修复 A8：部署在 8443 时丢端口）
  const port = location.port
  let host: string
  if (location.protocol === 'https:') {
    host = port && port !== '443' ? `${location.hostname}:${port}` : location.hostname
  } else {
    host = port ? `${location.hostname}:${port}` : location.hostname
  }
  return `${location.protocol}//${host}`
}

function getApiUrl(): string {
  return getApiBase()
}

const { withRetry } = useRetry()

/** 带重试与超时的 fetch 封装。
 *  C3：AbortController 在每次重试的 fn 回调内创建，避免首次超时 abort 后重试复用已 aborted 的 signal。
 */
async function fetchWithRetry(url: string, body: unknown, timeoutMs = 30000): Promise<Response> {
  return withRetry(
    () => {
      const controller = new AbortController()
      const timer = setTimeout(() => controller.abort(), timeoutMs)
      return fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
        signal: controller.signal,
      }).finally(() => clearTimeout(timer))
    },
    { maxRetries: 3, baseDelay: 1000 },
  )
}

export async function testSSH(node: ApiNode, cmds: string[]): Promise<CmdsTestResult> {
  const resp = await fetchWithRetry(`${getApiUrl()}${API_URI.sshTest}`, { node, cmds })
  if (!resp.ok) {
    const msg = await safeJSONError(resp)
    throw new Error(msg)
  }
  return resp.json()
}

export async function batchSSH(nodes: ApiNode[], cmds: string[]): Promise<CmdsTestResult[]> {
  const req: BatchCmdRequest = { nodes, cmds }
  const resp = await fetchWithRetry(`${getApiUrl()}${API_URI.sshBatch}`, req)
  if (!resp.ok) {
    const msg = await safeJSONError(resp)
    throw new Error(msg)
  }
  return resp.json()
}

/** safeJSONError 尝试从错误响应中提取 JSON error 字段，失败则回退到状态码。 */
async function safeJSONError(resp: Response): Promise<string> {
  try {
    const data = await resp.json()
    if (data.error) return data.error
  } catch { /* ignore */ }
  return `Error code ${resp.status}`
}

// v3 新增：HTTP Range 下载（断点续传）。设计见 design-v3.md §6.5。
// 凭据通过 POST body 传递（不暴露在 URL 中），path 通过 query string 传递。
export async function downloadWithRange(
  node: ApiNode,
  path: string,
  offset = 0,
): Promise<{ total: number; stream: ReadableStream<Uint8Array> }> {
  const params = new URLSearchParams({ path })
  // M6：加 AbortController 超时，防止连接挂起时无限等待（仅控制首字节响应）
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), 60000)
  try {
    const resp = await fetch(`${getApiUrl()}${API_URI.sftpDownload}?${params}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Range: `bytes=${offset}-` },
      body: JSON.stringify({ node }),
      signal: controller.signal,
      credentials: 'include',
    })
    if (!resp.ok && resp.status !== 206) throw new Error(`Error code ${resp.status}`)
    // T4：body 可能为 null（服务端错误/网络中断），显式检查避免后续 getReader() 崩溃
    if (!resp.body) throw new Error('download: response body is null')
    const total = parseTotalFromRange(resp.headers.get('Content-Range') ?? '')
    return { total, stream: resp.body }
  } finally {
    clearTimeout(timer)
  }
}

export function parseTotalFromRange(range: string): number {
  // bytes 0-1023/2048 → 2048
  const m = range.match(/\/(\d+)/)
  return m ? parseInt(m[1]) : 0
}

// ===== 节点本地缓存（与 v2 一致）=====

export function getCachedNodes(): ApiNode[] {
  const raw = localStorage.getItem('cached-nodes')
  if (!raw) return []
  try {
    const arr = JSON.parse(raw) as (ApiNode | OldApiNode)[]
    // N1：检测到凭据明文存储时提示安全风险
    const hasCreds = arr.some((n) => ('auth_value' in n && n.auth_value))
    if (hasCreds && !sessionStorage.getItem('managi-cred-warned')) {
      console.warn('[managi] 节点凭据以明文存储在 localStorage 中。如在共享环境使用，建议启用 BasicAuth 或使用 Windows 桌面端。')
      sessionStorage.setItem('managi-cred-warned', '1')
    }
    return arr.map(oldApiNodeConvert)
  } catch {
    return []
  }
}

export function setCachedNodes(list: ApiNode[]): void {
  localStorage.setItem('cached-nodes', JSON.stringify(list))
}

export function oldApiNodeConvert(n: ApiNode | OldApiNode): ApiNode {
  if ('ip' in n) {
    return {
      name: n.name,
      host: n.ip,
      port: n.port,
      username: n.ssh_username,
      auth_type: n.auth_type,
      auth_value: n.auth_value,
      group: 'group' in n ? (n as unknown as ApiNode).group : undefined,
    }
  }
  return n
}

export function getCachedGroups(): string[] {
  const raw = localStorage.getItem('cached-groups')
  if (!raw) return []
  try {
    const arr = JSON.parse(raw) as unknown
    return Array.isArray(arr) ? (arr as string[]).filter((g) => typeof g === 'string') : []
  } catch {
    return []
  }
}

export function setCachedGroups(list: string[]): void {
  localStorage.setItem('cached-groups', JSON.stringify(list))
}
