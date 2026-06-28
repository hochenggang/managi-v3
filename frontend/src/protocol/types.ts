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

/** 节点唯一 ID：host:port。 */
export function generateNodeId(node: ApiNode): string {
  return `${node.host}:${node.port}`
}
