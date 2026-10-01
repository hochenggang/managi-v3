// Package model 定义后端核心数据结构。
// 对应 v2 的 models.py，与前端 protocol/types.ts 对齐。
// 设计见 design-v5.md §4.1。
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// Validate 校验节点必填项，返回首个问题（消息直接展示给用户，故写中文）。
// 必须在拨号前挡住：空 host、越界端口到 SSH 层只会变成
// "dial tcp: missing address" 这类看不出该改哪里的技术报错。
func (n Node) Validate() error {
	switch {
	case n.Host == "":
		return errors.New("节点地址(host)为空")
	case n.Port <= 0 || n.Port > 65535:
		return fmt.Errorf("端口 %d 非法（应为 1-65535）", n.Port)
	case n.Username == "":
		return errors.New("登录用户名(username)为空")
	case n.AuthValue == "":
		return errors.New("认证内容为空：请填写密码或粘贴私钥")
	}
	switch n.AuthType {
	case "", AuthPassword, AuthKey: // 空按 password 处理，与 v2 兼容
		return nil
	default:
		return fmt.Errorf("不支持的认证方式 %q（仅 password / key）", n.AuthType)
	}
}

// ConnectionKey 返回连接池键：[host]:port:username:凭据指纹（IPv6 地址自动加方括号）。
// 凭据指纹不可省：同一 host:port:username 用不同密码/私钥（例如改过密码的节点条目
// 与残留的旧条目）若共用一条连接，后者会静默使用前者的凭据登录。
func (n Node) ConnectionKey() string {
	return net.JoinHostPort(n.Host, strconv.Itoa(n.Port)) + ":" + n.Username + ":" + n.authFingerprint()
}

// authFingerprint 返回认证材料的短指纹（sha256 前 4 字节的十六进制，8 字符）。
// 含 AuthType，使口令与密钥认证即便字面值相同也不会互相复用。
func (n Node) authFingerprint() string {
	sum := sha256.Sum256([]byte(string(n.AuthType) + "\x00" + n.AuthValue))
	return hex.EncodeToString(sum[:4])
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
