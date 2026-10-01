// Package config 负责加载后端配置。
// 设计见 design-v5.md §4.1。
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 用户可配置的环境变量只有下面 5 个：
//
//	MANAGI_HOST        监听地址
//	MANAGI_PORT        监听端口
//	MANAGI_AUTH        Basic Auth 凭据，格式 user:pass；非空即启用鉴权
//	MANAGI_INDEX_HTML  前端单页文件路径
//	MANAGI_KNOWN_HOSTS OpenSSH known_hosts 路径，设置即启用严格主机密钥校验
//
// 其余超时、心跳、连接池容量与分片大小都是协议调优参数而非用户选项：
// 改错的代价（周期性掉线、单帧放大到几百 MB 打爆内存）远大于收益，故不再开放。
// 下面的字段仍保留，供装配与测试读取/覆写，非法值由 Normalize 统一校正回默认。
type Config struct {
	Host string
	Port int

	// SSH 连接与保活
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
	// ChunkSize 是客户端→服务端单帧载荷上限：随 open/upload 响应下发，前端按它切片，
	// 同时决定 WS 读上限（超限的帧会以 1009 掐断连接）。
	// DownloadChunkSize 是 WS 下载每次写出的帧大小。
	ChunkSize         int
	DownloadChunkSize int

	// BasicAuth：Enabled 由 MANAGI_AUTH 是否非空决定，不单独开放开关
	BasicAuthEnabled  bool
	BasicAuthUser     string
	BasicAuthPassword string

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
	// MANAGI_AUTH 缺冒号时整串当密码，用户名回退到这里（出声提醒写法）
	DefaultBasicAuthUser = "admin"
	// 分片大小上界：前端按下发值切片，WS 单帧读取上限又取 2× 分片，
	// 不封顶的话一个写错的常量就能把单帧放大到几百 MB（服务端 OOM）。
	MaxChunkSize         = 8 << 20
	MaxDownloadChunkSize = 1 << 20
)

// Load 从环境变量加载配置，未设置则使用默认值，并对非法值做校正。
func Load() *Config {
	user, pass := parseBasicAuth(os.Getenv("MANAGI_AUTH"))
	cfg := &Config{
		Host:               envStr("MANAGI_HOST", DefaultHost),
		Port:               envInt("MANAGI_PORT", DefaultPort),
		SSHTimeout:         DefaultSSHTimeout,
		KeepaliveInterval:  DefaultKeepaliveInterval,
		SSHIdleTimeout:     DefaultSSHIdleTimeout,
		SSHPoolSize:        DefaultSSHPoolSize,
		WSReadDeadline:     DefaultWSReadDeadline,
		WSPingInterval:     DefaultWSPingInterval,
		SessionIdleTimeout: DefaultSessionIdleTimeout,
		ChunkSize:          DefaultChunkSize,
		DownloadChunkSize:  DefaultDownloadChunkSize,
		// 空表示未显式配置：启用 BasicAuth 时由服务入口生成随机强口令，避免固定弱默认值
		BasicAuthEnabled:  user != "",
		BasicAuthUser:     user,
		BasicAuthPassword: pass,
		KnownHostsFile:    expandHome(envStr("MANAGI_KNOWN_HOSTS", "")),
		IndexHTMLPath:     envStr("MANAGI_INDEX_HTML", "index.html"),
	}
	if fixed := cfg.Normalize(); len(fixed) > 0 {
		slog.Warn("invalid config replaced with defaults", "items", strings.Join(fixed, "; "))
	}
	return cfg
}

// parseBasicAuth 解析 MANAGI_AUTH=user:pass。
// 只有一个凭据项的部署面板不该再提供独立的启用开关：填了凭据就是要鉴权，
// 「写了密码却忘了打开关」是最容易发生的静默不生效。
// 按首个冒号切分（密码里可以有冒号）；没有冒号时整串当密码。
func parseBasicAuth(v string) (user, pass string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ""
	}
	u, p, found := strings.Cut(v, ":")
	if !found {
		slog.Warn("MANAGI_AUTH 应为 user:pass 格式，已按密码处理", "user", DefaultBasicAuthUser)
		return DefaultBasicAuthUser, v
	}
	if u == "" {
		u = DefaultBasicAuthUser
	}
	return u, p
}

// Normalize 把非法配置校正为可用值，返回每条修正说明（供入口在启动日志告警）。
// 此前各使用点自行「<=0 就临时兜底」，非法值静默生效：0 分片、0 超时这类配置
// 会把传输或心跳拖进死循环。集中校正一次，使用点仍保留 <=0 兜底以覆盖
// 测试中直接构造的零值 Config。
// 调用约定：Load 之后（或手工构造 Config 之后）在任何装配前调用一次。
func (c *Config) Normalize() []string {
	var fixed []string
	clamp := func(name string, value *int, def int) {
		if *value <= 0 {
			fixed = append(fixed, fmt.Sprintf("%s=%d→%d", name, *value, def))
			*value = def
		}
	}

	clamp("Port", &c.Port, DefaultPort)
	clamp("SSHTimeout", &c.SSHTimeout, DefaultSSHTimeout)
	clamp("KeepaliveInterval", &c.KeepaliveInterval, DefaultKeepaliveInterval)
	clamp("SSHIdleTimeout", &c.SSHIdleTimeout, DefaultSSHIdleTimeout)
	clamp("SSHPoolSize", &c.SSHPoolSize, DefaultSSHPoolSize)
	clamp("WSReadDeadline", &c.WSReadDeadline, DefaultWSReadDeadline)
	clamp("WSPingInterval", &c.WSPingInterval, DefaultWSPingInterval)
	clamp("SessionIdleTimeout", &c.SessionIdleTimeout, DefaultSessionIdleTimeout)
	clamp("ChunkSize", &c.ChunkSize, DefaultChunkSize)
	clamp("DownloadChunkSize", &c.DownloadChunkSize, DefaultDownloadChunkSize)
	bound := func(name string, value *int, max int) {
		if *value > max {
			fixed = append(fixed, fmt.Sprintf("%s=%d>%d→%d", name, *value, max, max))
			*value = max
		}
	}
	bound("ChunkSize", &c.ChunkSize, MaxChunkSize)
	bound("DownloadChunkSize", &c.DownloadChunkSize, MaxDownloadChunkSize)

	if c.Host == "" {
		fixed = append(fixed, "Host=(empty)→"+DefaultHost)
		c.Host = DefaultHost
	}

	// 读超时必须大于 Ping 间隔：否则两次心跳之间连接就被自己判死（周期性掉线）。
	if min := c.WSPingInterval * 3; c.WSReadDeadline <= c.WSPingInterval {
		fixed = append(fixed, fmt.Sprintf("WSReadDeadline=%d<=心跳间隔→%d", c.WSReadDeadline, min))
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
