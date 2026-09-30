// Package config 负责加载后端配置。
// 对应 v2 的 setting.py，从环境变量读取，提供默认值。
// 设计见 ../design-v3.md §4.1。
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config 聚合所有后端配置。
type Config struct {
	Host string
	Port int

	// SSH
	SSHTimeout        int // 秒
	KeepaliveInterval int // 秒
	SSHIdleTimeout    int // 秒，SSH 连接池空闲清理时间
	SSHPoolSize       int // 连接池常驻连接数上限

	// WebSocket
	WSReadDeadline int // 秒，读超时
	WSPingInterval int // 秒，服务端 WS Ping 间隔

	// 终端会话复用：最后一个前端断开后保留 shell 的时长（秒）
	SessionIdleTimeout int

	// SFTP
	// ChunkSize 由服务端在 upload_init 响应里下发，前端按它切片；同时决定 WS 单帧上限。
	// DownloadChunkSize 是 WS 下载每次写出的帧大小。
	ChunkSize         int
	DownloadChunkSize int

	// BasicAuth
	BasicAuthEnabled  bool
	BasicAuthUser     string
	BasicAuthPassword string
	// TrustProxy 为真时才采信 X-Forwarded-For。默认关闭：直连部署下任何人都能
	// 伪造该头，令登录失败限流按伪造 IP 计数，等于形同虚设。
	TrustProxy bool

	// SSH 主机密钥校验
	// KnownHostsFile 非空时启用严格校验（OpenSSH known_hosts 格式），
	// 取代进程内 TOFU；留空表示沿用 TOFU（首次信任）。
	KnownHostsFile string

	// 前端静态文件
	IndexHTMLPath string
	// IndexHTML 是内嵌的 index.html 内容；若设置则优先于 IndexHTMLPath
	IndexHTML []byte
}

// 配置默认值：集中定义，Load（缺失时）与 Normalize（非法时）共用。
const (
	DefaultHost               = "0.0.0.0"
	DefaultPort               = 18001
	DefaultSSHTimeout         = 15 // 秒
	DefaultKeepaliveInterval  = 30 // 秒
	DefaultSSHIdleTimeout     = 120
	DefaultSSHPoolSize        = 20
	DefaultWSReadDeadline     = 90
	DefaultWSPingInterval     = 30
	DefaultSessionIdleTimeout = 60
	DefaultChunkSize          = 1 << 20 // 1MB
	DefaultDownloadChunkSize  = 1 << 16
	// 分片上限：前端按服务端下发的分片大小切片，WS 单帧读取上限又取 2×分片，
	// 不封顶的话一个写错的环境变量就能把单帧放大到几百 MB（服务端 OOM）。
	MaxChunkSize         = 8 << 20
	MaxDownloadChunkSize = 1 << 20
)

// Load 从环境变量加载配置，未设置则使用默认值，并对显式设置的非法值做校正。
// 环境变量与 v2 保持兼容：MANAGI_HOST / MANAGI_PORT / MANAGI_SSH_TIMEOUT /
// MANAGI_KEEPALIVE / MANAGI_BASICAUTH_ENABLED / MANAGI_BASICAUTH_USERNAME /
// MANAGI_BASICAUTH_PASSWORD。
func Load() *Config {
	cfg := &Config{
		Host:               envStr("MANAGI_HOST", DefaultHost),
		Port:               envInt("MANAGI_PORT", DefaultPort),
		SSHTimeout:         envInt("MANAGI_SSH_TIMEOUT", DefaultSSHTimeout),
		KeepaliveInterval:  envInt("MANAGI_KEEPALIVE", DefaultKeepaliveInterval),
		SSHIdleTimeout:     envInt("MANAGI_SSH_IDLE_TIMEOUT", DefaultSSHIdleTimeout),
		SSHPoolSize:        envInt("MANAGI_SSH_POOL_SIZE", DefaultSSHPoolSize),
		WSReadDeadline:     envInt("MANAGI_WS_READ_DEADLINE", DefaultWSReadDeadline),
		WSPingInterval:     envInt("MANAGI_WS_PING_INTERVAL", DefaultWSPingInterval),
		SessionIdleTimeout: envInt("MANAGI_SESSION_IDLE_TIMEOUT", DefaultSessionIdleTimeout),
		ChunkSize:          envInt("MANAGI_SFTP_CHUNK_SIZE", DefaultChunkSize),
		DownloadChunkSize:  envInt("MANAGI_SFTP_DOWNLOAD_CHUNK", DefaultDownloadChunkSize),
		BasicAuthEnabled:   envBool("MANAGI_BASICAUTH_ENABLED", false),
		BasicAuthUser:      envStr("MANAGI_BASICAUTH_USERNAME", "admin"),
		// 空表示未显式配置：启用 BasicAuth 时由服务入口生成随机强口令，避免固定弱默认值
		BasicAuthPassword: envStr("MANAGI_BASICAUTH_PASSWORD", ""),
		TrustProxy:        envBool("MANAGI_TRUST_PROXY", false),
		KnownHostsFile:    expandHome(envStr("MANAGI_KNOWN_HOSTS", "")),
		IndexHTMLPath:     envStr("MANAGI_INDEX_HTML", "index.html"),
	}
	if fixed := cfg.Normalize(); len(fixed) > 0 {
		slog.Warn("invalid config replaced with defaults", "items", strings.Join(fixed, "; "))
	}
	return cfg
}

// Normalize 把非法配置校正为可用值，返回每条修正说明（供入口在启动日志告警）。
// 此前各使用点自行「<=0 就临时兜底」，非法值静默生效：改错环境变量既不报错，
// 也让 0 分片、0 超时这类配置把传输或心跳拖进死循环。集中校正一次，
// 使用点仍保留 <=0 兜底以覆盖测试中直接构造的零值 Config。
// 调用约定：Load 之后（或手工构造 Config 之后）在任何装配前调用一次。
func (c *Config) Normalize() []string {
	var fixed []string
	clamp := func(name string, value *int, def int) {
		if *value <= 0 {
			fixed = append(fixed, fmt.Sprintf("%s=%d→%d", name, *value, def))
			*value = def
		}
	}

	clamp("MANAGI_PORT", &c.Port, DefaultPort)
	clamp("MANAGI_SSH_TIMEOUT", &c.SSHTimeout, DefaultSSHTimeout)
	clamp("MANAGI_KEEPALIVE", &c.KeepaliveInterval, DefaultKeepaliveInterval)
	clamp("MANAGI_SSH_IDLE_TIMEOUT", &c.SSHIdleTimeout, DefaultSSHIdleTimeout)
	clamp("MANAGI_SSH_POOL_SIZE", &c.SSHPoolSize, DefaultSSHPoolSize)
	clamp("MANAGI_WS_READ_DEADLINE", &c.WSReadDeadline, DefaultWSReadDeadline)
	clamp("MANAGI_WS_PING_INTERVAL", &c.WSPingInterval, DefaultWSPingInterval)
	clamp("MANAGI_SESSION_IDLE_TIMEOUT", &c.SessionIdleTimeout, DefaultSessionIdleTimeout)
	clamp("MANAGI_SFTP_CHUNK_SIZE", &c.ChunkSize, DefaultChunkSize)
	clamp("MANAGI_SFTP_DOWNLOAD_CHUNK", &c.DownloadChunkSize, DefaultDownloadChunkSize)
	// 分片大小还须有上界：见 MaxChunkSize 注释（单帧过大直接把内存打爆）
	bound := func(name string, value *int, max int) {
		if *value > max {
			fixed = append(fixed, fmt.Sprintf("%s=%d>%d→%d", name, *value, max, max))
			*value = max
		}
	}
	bound("MANAGI_SFTP_CHUNK_SIZE", &c.ChunkSize, MaxChunkSize)
	bound("MANAGI_SFTP_DOWNLOAD_CHUNK", &c.DownloadChunkSize, MaxDownloadChunkSize)

	if c.Host == "" {
		fixed = append(fixed, "MANAGI_HOST=(empty)→"+DefaultHost)
		c.Host = DefaultHost
	}

	// 读超时必须大于 Ping 间隔：否则两次心跳之间连接就被自己判死（周期性掉线）。
	if min := c.WSPingInterval * 3; c.WSReadDeadline <= c.WSPingInterval {
		fixed = append(fixed, fmt.Sprintf("MANAGI_WS_READ_DEADLINE=%d<=心跳间隔→%d", c.WSReadDeadline, min))
		c.WSReadDeadline = min
	}

	// 相对路径在启动时转绝对，避免 CWD 不确定时 404。
	if c.IndexHTMLPath != "" && !filepath.IsAbs(c.IndexHTMLPath) {
		if abs, err := filepath.Abs(c.IndexHTMLPath); err == nil {
			c.IndexHTMLPath = abs
		}
	}
	return fixed
}

// expandHome 展开开头的 "~"（用户会直接写 MANAGI_KNOWN_HOSTS=~/.ssh/known_hosts）。
// 展开失败（拿不到 home）时原样返回：让上层按「文件不存在」报错，比静默换成别的路径好。
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, "~\\") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, filepath.FromSlash(p[2:]))
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
		// 显式写了却读不出必须出声：静默回退默认值会让人以为改过的配置已经生效
		slog.Warn("invalid integer env, using default", "key", key, "value", v, "default", def)
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	switch v {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	// 拼写错误（如 MANAGI_BASICAUTH_ENABLED=ture）会静默落回默认值，
	// 对鉴权开关来说是最坏的一种「配置没生效」，必须告警。
	if v != "" {
		slog.Warn("invalid boolean env, using default", "key", key, "value", v, "default", def)
	}
	return def
}
