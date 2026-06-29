// Package config 负责加载后端配置。
// 对应 v2 的 setting.py，从环境变量读取，提供默认值。
// 设计见 ../design-v3.md §4.1。
package config

import "os"

// Config 聚合所有后端配置。
type Config struct {
	Host string
	Port int

	// SSH
	SSHTimeout        int // 秒
	KeepaliveInterval int // 秒

	// WebSocket
	WSReadDeadline int // 秒，心跳超时

	// SFTP
	ChunkSize        int // 上传分片
	DownloadChunkSize int // 下载分片

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
	return &Config{
		Host:              envStr("MANAGI_HOST", "0.0.0.0"),
		Port:              envInt("MANAGI_PORT", 18001),
		SSHTimeout:        envInt("MANAGI_SSH_TIMEOUT", 15),
		KeepaliveInterval: envInt("MANAGI_KEEPALIVE", 30),
		WSReadDeadline:    envInt("MANAGI_WS_READ_DEADLINE", 60),
		ChunkSize:         envInt("MANAGI_SFTP_CHUNK_SIZE", 1<<20), // 1MB
		DownloadChunkSize: envInt("MANAGI_SFTP_DOWNLOAD_CHUNK", 1 << 16),
		BasicAuthEnabled:  envBool("MANAGI_BASICAUTH_ENABLED", false),
		BasicAuthUser:     envStr("MANAGI_BASICAUTH_USERNAME", "admin"),
		BasicAuthPassword: envStr("MANAGI_BASICAUTH_PASSWORD", "admin123"),
		IndexHTMLPath:     envStr("MANAGI_INDEX_HTML", "index.html"),
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				return def
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	return def
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
