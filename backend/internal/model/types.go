// Package model 定义后端核心数据结构。
// 对应 v2 的 models.py，与前端 protocol/types.ts 对齐。
// 设计见 ../design-v3.md §4.1。
package model

import (
	"net"
	"strconv"
)

// AuthType SSH 认证方式。
type AuthType string

const (
	AuthPassword AuthType = "password"
	AuthKey      AuthType = "key"
)

// Node 远程节点描述。
type Node struct {
	Name      string   `json:"name"`
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	Username  string   `json:"username"`
	AuthType  AuthType `json:"auth_type"`
	AuthValue string   `json:"auth_value"`
}

// ConnectionKey 返回连接池键：[host]:port:username（IPv6 地址自动加方括号）。
// 注意：不含凭据，与 v2 行为一致。
func (n Node) ConnectionKey() string {
	return net.JoinHostPort(n.Host, strconv.Itoa(n.Port)) + ":" + n.Username
}

// CmdsTestResult 单节点命令执行结果。
type CmdsTestResult struct {
	TimeElapsed float64  `json:"time_elapsed"`
	Success     bool     `json:"success"`
	Output      []string `json:"output"`
	Error       []string `json:"error"`
	Node        Node     `json:"node"` // 已脱敏
	Cmds        string   `json:"cmds"`
}

// Masked 返回脱敏后的 Node 副本（auth_value 置为 ***）。
func (n Node) Masked() Node {
	cp := n
	cp.AuthValue = "***"
	return cp
}

// BatchCmdRequest 批量命令请求。
type BatchCmdRequest struct {
	Nodes []Node   `json:"nodes"`
	Cmds  []string `json:"cmds"`
}

// FileItem 目录项。
type FileItem struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Mode     string `json:"mode"`
	IsDir    bool   `json:"is_dir"`
	Mtime    int64  `json:"mtime"`
}
