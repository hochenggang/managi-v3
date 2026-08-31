// 协议层：集中定义所有 WebSocket 与 HTTP 消息类型。
// 取代 v2 散落在组件内的隐式约定，与后端 internal/model/types.go 对齐。
// 设计见 ../../../design-v3.md §5.2.1。

/** 节点：远程 SSH 服务器描述（与 v2 typeApiNode 兼容）。 */
export interface ApiNode {
  name: string
  host: string
  port: number
  username: string
  auth_type: 'password' | 'key'
  auth_value: string
  group?: string
}

/** 旧版节点格式（兼容迁移用）。 */
export interface OldApiNode {
  name: string
  ip: string
  port: number
  ssh_username: string
  auth_type: 'password' | 'key'
  auth_value: string
}

/** 单节点命令执行结果。 */
export interface CmdsTestResult {
  time_elapsed: number
  success: boolean
  output: string[]
  error: string[]
  node: ApiNode
  cmds: string
}

/** 批量命令请求体。 */
export interface BatchCmdRequest {
  nodes: ApiNode[]
  cmds: string[]
}

/** 快捷命令项。 */
export interface ShortcutItem {
  label: string
  cmd: string
}

/** 应用配置文件（v3），导出/导入时使用。 */
export interface AppConfig {
  version: 3
  nodes: Record<string, ApiNode>
  groups: string[]
  shortcuts: ShortcutItem[]
  settings?: Partial<import('@/stores/settingsStore').Settings>
}

/** 节点唯一 ID：host:port:username。
 *  与后端连接池键（model.ConnectionKey）保持一致：同主机同端口但不同用户名的节点
 *  是不同节点，若 ID 不含 username 会在前端互相覆盖、静默丢数据。
 */
export function generateNodeId(node: ApiNode): string {
  return `${node.host}:${node.port}:${node.username}`
}
