// Package testutil - 公共测试夹具。
package testutil

import (
	"managi/internal/config"
	"managi/internal/model"
)

// TestConfig 返回默认测试配置。
func TestConfig() *config.Config {
	return &config.Config{
		Host:               "127.0.0.1",
		Port:               18001,
		SSHTimeout:         15,
		KeepaliveInterval:  30,
		WSReadDeadline:     60,
		ChunkSize:          1 << 20,
		DownloadChunkSize:  1 << 16,
		IndexHTMLPath:      "index.html",
		SessionIdleTimeout: 60,
	}
}

// TestNode 返回密码认证的测试节点。
func TestNode(host string, port int) model.Node {
	return model.Node{
		Name:      "test-node",
		Host:      host,
		Port:      port,
		Username:  "test",
		AuthType:  model.AuthPassword,
		AuthValue: "testpass",
	}
}

// BadPasswordNode 返回错误密码的节点（用于认证失败测试）。
func BadPasswordNode(host string, port int) model.Node {
	n := TestNode(host, port)
	n.AuthValue = "wrong-password"
	return n
}
