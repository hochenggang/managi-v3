// Package sshpool - 拨号与时长：一条 SSH 连接「怎么建、多久算该动」。
package sshpool

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"

	"managi/internal/config"
	"managi/internal/model"
)

// timing 池用到的三个时长。集中一处解析，避免「配置为 0」的兜底散落在拨号、
// keepalive、清理三段各写一遍而各自漂移。
type timing struct {
	dial      time.Duration // 建连超时
	keepalive time.Duration // keepalive 周期
	idle      time.Duration // 空闲连接回收时限
}

// newTiming 把配置里的秒数换算成时长。
// 配置一律经 config.Load/Normalize 装配，非正值在那里就已校正为默认值，此处不再兜底。
func newTiming(cfg *config.Config) timing {
	sec := func(v int) time.Duration { return time.Duration(v) * time.Second }
	return timing{
		dial:      sec(cfg.SSHTimeout),
		keepalive: sec(cfg.KeepaliveInterval),
		idle:      sec(cfg.SSHIdleTimeout),
	}
}

// addr 节点的网络地址（host:port）。
func addr(node model.Node) string {
	return net.JoinHostPort(node.Host, strconv.Itoa(node.Port))
}

// dial 建立一条新 SSH 连接。生命周期（入池、keepalive、引用计数）由 Pool 负责。
func (p *Pool) dial(node model.Node) (*ssh.Client, error) {
	authMethods, err := authMethods(node)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            node.Username,
		Auth:            authMethods,
		HostKeyCallback: p.hostKeys.callback(addr(node)),
		Timeout:         p.timing.dial,
	}
	client, err := ssh.Dial("tcp", addr(node), cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr(node), err)
	}
	return client, nil
}

// authMethods 构造认证方法列表。
func authMethods(node model.Node) ([]ssh.AuthMethod, error) {
	switch node.AuthType {
	case model.AuthKey:
		// ssh.ParsePrivateKey 已覆盖 RSA/Ed25519/ECDSA/PKCS8 等常见格式
		signer, err := ssh.ParsePrivateKey([]byte(node.AuthValue))
		if err != nil {
			var ppm *ssh.PassphraseMissingError
			if errors.As(err, &ppm) {
				// 当前没有口令输入口，故给出可操作的绕开方式，而不是 "ssh: no key found"
				return nil, errors.New("私钥带口令保护，暂不支持解锁：请用 ssh-keygen -p 去掉口令后重新粘贴，或改用密码认证")
			}
			return nil, fmt.Errorf("私钥解析失败（请确认粘贴的是完整私钥，含 BEGIN/END 行）: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default: // password
		return []ssh.AuthMethod{ssh.Password(node.AuthValue)}, nil
	}
}

// probeTimeout 存活探测的超时上限（测试可缩短）。
// 半开 TCP（对端消失、链路黑洞）上 SendRequest 要等系统 TCP 超时才回错（分钟级），
// 而探测在 reuse 的请求路径上：不设上限，用户点「测试连接」会白等一刻钟。
var probeTimeout = 5 * time.Second

// isAlive 判断连接 transport 是否活跃。是一次阻塞网络调用（最长 probeTimeout），
// 调用方不得持池锁。入池的连接 client 必然非空（见 Connection 注释），故只发探测请求。
// 超时按「已死」处理并顺手关闭连接：等不回包与死亡等价，留着只会让下次再等一遍。
func isAlive(client *ssh.Client) bool {
	done := make(chan error, 1) // 带缓冲：超时路径没人接结果，不能让探测协程卡住
	go func() {
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(probeTimeout):
		_ = client.Close() // 关连接让挂着的 SendRequest 立刻返回
		return false
	}
}
