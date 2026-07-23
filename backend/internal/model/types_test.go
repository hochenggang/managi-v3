package model

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
)

// authHash 计算 auth_value 的 SHA256 前 8 字符（与 ConnectionKey 同逻辑）。
func authHash(authValue string) string {
	h := sha256.Sum256([]byte(authValue))
	return hex.EncodeToString(h[:])[:8]
}

// TestNode_ConnectionKey 验证连接键格式 host:port:username:auth_type:auth_hash。
func TestNode_ConnectionKey(t *testing.T) {
	cases := []struct {
		name     string
		node     Node
		expected string
	}{
		{
			name:     "standard",
			node:     Node{Host: "10.0.0.1", Port: 22, Username: "root", AuthType: AuthPassword, AuthValue: "pass"},
			expected: "10.0.0.1:22:root:password:" + authHash("pass"),
		},
		{
			name:     "custom_port",
			node:     Node{Host: "192.168.1.100", Port: 22022, Username: "admin", AuthType: AuthPassword, AuthValue: "p"},
			expected: "192.168.1.100:22022:admin:password:" + authHash("p"),
		},
		{
			name:     "ipv6_like",
			node:     Node{Host: "fe80::1", Port: 22, Username: "ubuntu", AuthType: AuthKey, AuthValue: "keydata"},
			expected: "[fe80::1]:22:ubuntu:key:" + authHash("keydata"),
		},
		{
			name:     "different_password_different_key",
			node:     Node{Host: "10.0.0.1", Port: 22, Username: "root", AuthType: AuthPassword, AuthValue: "other"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.expected != "" {
				assert.Equal(t, c.expected, c.node.ConnectionKey())
			}
		})
	}
	// 不同凭据产生不同 key
	n1 := Node{Host: "10.0.0.1", Port: 22, Username: "root", AuthType: AuthPassword, AuthValue: "pass1"}
	n2 := Node{Host: "10.0.0.1", Port: 22, Username: "root", AuthType: AuthPassword, AuthValue: "pass2"}
	assert.NotEqual(t, n1.ConnectionKey(), n2.ConnectionKey())
}

// TestNode_Masked 验证脱敏：AuthValue 置为 ***，原 Node 不变。
func TestNode_Masked(t *testing.T) {
	original := Node{
		Name:      "prod-server",
		Host:      "10.0.0.1",
		Port:      22,
		Username:  "root",
		AuthType:  AuthPassword,
		AuthValue: "s3cret-p@ss",
	}

	masked := original.Masked()

	// 脱敏后 AuthValue 应为 ***
	assert.Equal(t, "***", masked.AuthValue)

	// 其他字段应保持一致
	assert.Equal(t, original.Name, masked.Name)
	assert.Equal(t, original.Host, masked.Host)
	assert.Equal(t, original.Port, masked.Port)
	assert.Equal(t, original.Username, masked.Username)
	assert.Equal(t, original.AuthType, masked.AuthType)

	// 原 Node 不应被修改（值拷贝）
	assert.Equal(t, "s3cret-p@ss", original.AuthValue)
}

// TestNode_Masked_KeyAuth 验证 key 认证也能脱敏。
func TestNode_Masked_KeyAuth(t *testing.T) {
	node := Node{
		Host:      "host",
		Port:      22,
		Username:  "user",
		AuthType:  AuthKey,
		AuthValue: "-----BEGIN RSA PRIVATE KEY-----\n...",
	}
	masked := node.Masked()
	assert.Equal(t, "***", masked.AuthValue)
	assert.Equal(t, AuthKey, masked.AuthType)
}

// TestAuthType_Constants 验证认证类型常量值。
func TestAuthType_Constants(t *testing.T) {
	assert.Equal(t, AuthType("password"), AuthPassword)
	assert.Equal(t, AuthType("key"), AuthKey)
}

// TestFileOperationType_Constants 验证 SFTP 操作类型常量。
func TestFileOperationType_Constants(t *testing.T) {
	assert.Equal(t, FileOperationType("upload"), OpUpload)
	assert.Equal(t, FileOperationType("upload_init"), OpUploadInit)
	assert.Equal(t, FileOperationType("upload_chunk"), OpUploadChunk)
	assert.Equal(t, FileOperationType("upload_complete"), OpUploadDone)
	assert.Equal(t, FileOperationType("download"), OpDownload)
	assert.Equal(t, FileOperationType("delete"), OpDelete)
	assert.Equal(t, FileOperationType("list"), OpList)
	assert.Equal(t, FileOperationType("mkdir"), OpMkdir)
	assert.Equal(t, FileOperationType("rename"), OpRename)
	assert.Equal(t, FileOperationType("move"), OpMove)
}
