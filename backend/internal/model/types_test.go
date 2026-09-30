package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNode_ConnectionKey 验证连接键格式 [host]:port:username:凭据指纹。
func TestNode_ConnectionKey(t *testing.T) {
	cases := []struct {
		name     string
		node     Node
		expected string
	}{
		{
			name:     "standard",
			node:     Node{Host: "10.0.0.1", Port: 22, Username: "root"},
			expected: "10.0.0.1:22:root:6e340b9c",
		},
		{
			name:     "custom_port",
			node:     Node{Host: "192.168.1.100", Port: 22022, Username: "admin"},
			expected: "192.168.1.100:22022:admin:6e340b9c",
		},
		{
			name:     "ipv6_like",
			node:     Node{Host: "fe80::1", Port: 22, Username: "ubuntu"},
			expected: "[fe80::1]:22:ubuntu:6e340b9c",
		},
		{
			name:     "empty_username",
			node:     Node{Host: "host", Port: 22, Username: ""},
			expected: "host:22::6e340b9c",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, c.node.ConnectionKey())
		})
	}
}

// TestNode_ConnectionKey_DistinguishesCredentials 验证同 host:port:username
// 但凭据不同（改过密码的条目 vs 旧条目）不会落到同一个池键上。
func TestNode_ConnectionKey_DistinguishesCredentials(t *testing.T) {
	base := Node{Host: "10.0.0.1", Port: 22, Username: "root"}
	oldPass := base
	oldPass.AuthType, oldPass.AuthValue = AuthPassword, "old-pass"
	newPass := base
	newPass.AuthType, newPass.AuthValue = AuthPassword, "new-pass"
	keyed := base
	keyed.AuthType, keyed.AuthValue = AuthKey, "old-pass" // 与 oldPass 字面值相同但认证方式不同

	assert.NotEqual(t, oldPass.ConnectionKey(), newPass.ConnectionKey())
	assert.NotEqual(t, oldPass.ConnectionKey(), keyed.ConnectionKey())
	assert.Equal(t, oldPass.ConnectionKey(), oldPass.ConnectionKey()) // 稳定，可作 map 键
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
