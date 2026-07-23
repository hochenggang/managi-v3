// Package config 负责加载后端配置。
// 对应 v2 的 setting.py，从环境变量读取，提供默认值。
// 设计见 ../design-v3.md §4.1。
package config

import (
	"os"
	"path/filepath"
	"strconv"
)

// Config 聚合所有后端配置。
type Config struct {
	Host string
	Port int

	// SSH
	SSHTimeout        int // 秒
	KeepaliveInterval int // 秒
	SSHIdleTimeout    int // 秒，SSH 连接池空闲清理时间

	// WebSocket
	WSReadDeadline int // 秒，读超时
	WSPingInterval int // 秒，服务端 WS Ping 间隔

	// 终端会话复用：最后一个前端断开后保留 shell 的时长（秒）
	SessionIdleTimeout int

	// SFTP
	ChunkSize         int // 上传分片
	DownloadChunkSize int // 下载分片
	UploadIdleTimeout int // 秒，上传分片空闲超时

	// 连接池
	SSHPoolSize int // 连接池最大连接数

	// BasicAuth
	BasicAuthEnabled  bool
	BasicAuthUser     string
	BasicAuthPassword string

	// 前端静态文件
	IndexHTMLPath string
	// IndexHTML 是内嵌的 index.html 内容；若设置则优先于 IndexHTMLPath
	IndexHTML []byte
}

// Load 从环境变量加载配置，未设置则使用默认值。
// 环境变量与 v2 保持兼容：MANAGI_HOST / MANAGI_PORT / MANAGI_SSH_TIMEOUT /
// MANAGI_KEEPALIVE / MANAGI_BASICAUTH_ENABLED / MANAGI_BASICAUTH_USERNAME /
// MANAGI_BASICAUTH_PASSWORD。
func Load() *Config {
	cfg := &Config{
		Host:               envStr("MANAGI_HOST", "0.0.0.0"),
		Port:               envIntRange("MANAGI_PORT", 18001, 1, 65535),
		SSHTimeout:         envIntRange("MANAGI_SSH_TIMEOUT", 15, 1, 300),
		KeepaliveInterval:  envIntRange("MANAGI_KEEPALIVE", 30, 5, 600),
		SSHIdleTimeout:     envIntRange("MANAGI_SSH_IDLE_TIMEOUT", 120, 10, 3600),
		WSReadDeadline:     envIntRange("MANAGI_WS_READ_DEADLINE", 90, 10, 600),
		WSPingInterval:     envIntRange("MANAGI_WS_PING_INTERVAL", 30, 5, 300),
		SessionIdleTimeout: envIntRange("MANAGI_SESSION_IDLE_TIMEOUT", 60, 5, 600),
		ChunkSize:          envIntRange("MANAGI_SFTP_CHUNK_SIZE", 1<<20, 1024, 100<<20),
		DownloadChunkSize:  envIntRange("MANAGI_SFTP_DOWNLOAD_CHUNK", 1<<16, 1024, 1<<20),
		UploadIdleTimeout:  envIntRange("MANAGI_SFTP_UPLOAD_TIMEOUT", 1800, 60, 7200),
		SSHPoolSize:        envIntRange("MANAGI_SSH_POOL_SIZE", 20, 1, 100),
		BasicAuthEnabled:   envBool("MANAGI_BASICAUTH_ENABLED", false),
		BasicAuthUser:      envStr("MANAGI_BASICAUTH_USERNAME", "admin"),
		BasicAuthPassword:  envStr("MANAGI_BASICAUTH_PASSWORD", "admin123"),
		IndexHTMLPath:      envStr("MANAGI_INDEX_HTML", "index.html"),
	}
	// 修复 A19/B36：启动时将相对 IndexHTMLPath 转为绝对路径，避免 CWD 不确定时 404。
	// 从 handler.Register 移到此处，保持 config 为唯一的配置归一化入口。
	if cfg.IndexHTMLPath != "" && !filepath.IsAbs(cfg.IndexHTMLPath) {
		if abs, err := filepath.Abs(cfg.IndexHTMLPath); err == nil {
			cfg.IndexHTMLPath = abs
		}
	}
	return cfg
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// envIntRange 读取环境变量并校验范围，越界则使用默认值。
// C1：防止用户设置不合理的值导致运行异常。
func envIntRange(key string, def, min, max int) int {
	n := envInt(key, def)
	if n < min || n > max {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	switch os.Getenv(key) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return def
	}
}
