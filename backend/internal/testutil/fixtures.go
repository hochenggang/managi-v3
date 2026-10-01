// Package testutil - 公共测试夹具。
package testutil

import (
	"managi/internal/config"
	"managi/internal/model"
)

// TestConfig 返回测试配置：只覆写测试关心的项，其余交给 Normalize。
// 与生产入口 config.Load 走同一条校正路径，默认值因此全仓只有 config 一处定义，
// 各使用点也就不必再写「零值就兜底」的分支。
func TestConfig() *config.Config {
	cfg := config.Config{Host: "127.0.0.1", Port: 18001}
	cfg.Normalize()
	return &cfg
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
