package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoad_Defaults 验证所有字段的默认值。
func TestLoad_Defaults(t *testing.T) {
	// 清空所有相关环境变量，确保使用默认值
	keys := []string{
		"MANAGI_HOST", "MANAGI_PORT", "MANAGI_SSH_TIMEOUT", "MANAGI_KEEPALIVE", "MANAGI_SSH_IDLE_TIMEOUT",
		"MANAGI_SSH_POOL_SIZE",
		"MANAGI_WS_READ_DEADLINE", "MANAGI_WS_PING_INTERVAL", "MANAGI_SFTP_CHUNK_SIZE", "MANAGI_SFTP_DOWNLOAD_CHUNK",
		"MANAGI_BASICAUTH_ENABLED", "MANAGI_BASICAUTH_USERNAME", "MANAGI_BASICAUTH_PASSWORD",
		"MANAGI_TRUST_PROXY", "MANAGI_KNOWN_HOSTS",
		"MANAGI_INDEX_HTML",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}

	cfg := Load()
	assert.Equal(t, "0.0.0.0", cfg.Host)
	assert.Equal(t, 18001, cfg.Port)
	assert.Equal(t, 15, cfg.SSHTimeout)
	assert.Equal(t, 30, cfg.KeepaliveInterval)
	assert.Equal(t, 120, cfg.SSHIdleTimeout)
	assert.Equal(t, 20, cfg.SSHPoolSize)
	assert.Equal(t, 90, cfg.WSReadDeadline)
	assert.Equal(t, 30, cfg.WSPingInterval)
	assert.Equal(t, 1<<20, cfg.ChunkSize) // 1MB
	assert.Equal(t, 1<<16, cfg.DownloadChunkSize)
	assert.False(t, cfg.BasicAuthEnabled)
	assert.Equal(t, "admin", cfg.BasicAuthUser)
	// 空表示未显式配置；启用 BasicAuth 时由服务入口生成随机强口令
	assert.Equal(t, "", cfg.BasicAuthPassword)
	// 默认不采信 XFF，默认沿用 TOFU（不设 known_hosts）
	assert.False(t, cfg.TrustProxy)
	assert.Equal(t, "", cfg.KnownHostsFile)
	// IndexHTMLPath 现在在 Load 中转为绝对路径
	assert.True(t, filepath.IsAbs(cfg.IndexHTMLPath), "IndexHTMLPath should be absolute")
	assert.True(t, filepath.Base(cfg.IndexHTMLPath) == "index.html", "IndexHTMLPath base should be index.html")
}

// TestLoad_EnvOverride 验证环境变量覆盖默认值。
func TestLoad_EnvOverride(t *testing.T) {
	// 使用跨平台绝对路径，避免 Windows 上 /var/www 被视为相对路径
	absPath := filepath.Join(t.TempDir(), "index.html")
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	t.Setenv("MANAGI_HOST", "192.168.1.1")
	t.Setenv("MANAGI_PORT", "8080")
	t.Setenv("MANAGI_SSH_TIMEOUT", "30")
	t.Setenv("MANAGI_KEEPALIVE", "60")
	t.Setenv("MANAGI_SSH_IDLE_TIMEOUT", "300")
	t.Setenv("MANAGI_SSH_POOL_SIZE", "64")
	t.Setenv("MANAGI_WS_READ_DEADLINE", "120")
	t.Setenv("MANAGI_WS_PING_INTERVAL", "20")
	t.Setenv("MANAGI_SFTP_CHUNK_SIZE", "2097152")
	t.Setenv("MANAGI_SFTP_DOWNLOAD_CHUNK", "4096")
	t.Setenv("MANAGI_BASICAUTH_ENABLED", "true")
	t.Setenv("MANAGI_BASICAUTH_USERNAME", "ops")
	t.Setenv("MANAGI_BASICAUTH_PASSWORD", "secret")
	t.Setenv("MANAGI_TRUST_PROXY", "true")
	t.Setenv("MANAGI_KNOWN_HOSTS", knownHosts)
	t.Setenv("MANAGI_INDEX_HTML", absPath)

	cfg := Load()
	assert.Equal(t, "192.168.1.1", cfg.Host)
	assert.Equal(t, 8080, cfg.Port)
	assert.Equal(t, 30, cfg.SSHTimeout)
	assert.Equal(t, 60, cfg.KeepaliveInterval)
	assert.Equal(t, 300, cfg.SSHIdleTimeout)
	assert.Equal(t, 64, cfg.SSHPoolSize)
	assert.Equal(t, 120, cfg.WSReadDeadline)
	assert.Equal(t, 20, cfg.WSPingInterval)
	assert.Equal(t, 2097152, cfg.ChunkSize)
	assert.Equal(t, 4096, cfg.DownloadChunkSize)
	assert.True(t, cfg.BasicAuthEnabled)
	assert.Equal(t, "ops", cfg.BasicAuthUser)
	assert.Equal(t, "secret", cfg.BasicAuthPassword)
	assert.True(t, cfg.TrustProxy)
	assert.Equal(t, knownHosts, cfg.KnownHostsFile)
	// 已是绝对路径，Load 不会修改
	assert.Equal(t, absPath, cfg.IndexHTMLPath)
}

// TestEnvInt_Invalid 验证非数字环境变量回退默认值。
func TestEnvInt_Invalid(t *testing.T) {
	t.Setenv("MANAGI_PORT", "abc")
	t.Setenv("MANAGI_SSH_TIMEOUT", "12.5")

	cfg := Load()
	assert.Equal(t, 18001, cfg.Port)    // 非数字 → 默认
	assert.Equal(t, 15, cfg.SSHTimeout) // 含小数点 → 默认
}

// TestEnvBool_Variants 验证 envBool 的各种输入。
func TestEnvBool_Variants(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{"true", true},
		{"1", true},
		{"yes", true},
		{"false", false},
		{"0", false},
		{"no", false},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			t.Setenv("MANAGI_BASICAUTH_ENABLED", c.input)
			cfg := Load()
			assert.Equal(t, c.expected, cfg.BasicAuthEnabled)
		})
	}
}

// TestEnvBool_Default 验证空值使用默认值。
func TestEnvBool_Default(t *testing.T) {
	t.Setenv("MANAGI_BASICAUTH_ENABLED", "")
	cfg := Load()
	assert.False(t, cfg.BasicAuthEnabled) // 默认 false
}

// TestEnvStr_EmptyString 验证空字符串使用默认值。
func TestEnvStr_EmptyString(t *testing.T) {
	t.Setenv("MANAGI_HOST", "")
	cfg := Load()
	assert.Equal(t, "0.0.0.0", cfg.Host)
}

// TestLoad_RelativePathConvertedToAbsolute 验证相对 IndexHTMLPath 被转为绝对路径（B36 修复）。
func TestLoad_RelativePathConvertedToAbsolute(t *testing.T) {
	t.Setenv("MANAGI_INDEX_HTML", "relative/path/index.html")
	cfg := Load()
	assert.True(t, filepath.IsAbs(cfg.IndexHTMLPath), "relative path should be converted to absolute")
	assert.Equal(t, "index.html", filepath.Base(cfg.IndexHTMLPath))
}

// TestNormalize_RejectsInvalidValues 验证非法配置被校正为默认值并被报告出来
// （历史上这些值会静默生效，0 分片 / 0 超时把传输与心跳拖死也无人察觉）。
func TestNormalize_RejectsInvalidValues(t *testing.T) {
	cfg := &Config{
		Host:               "",
		Port:               0,
		SSHTimeout:         -1,
		KeepaliveInterval:  0,
		SSHIdleTimeout:     -5,
		WSReadDeadline:     90,
		WSPingInterval:     30,
		SessionIdleTimeout: 0,
		ChunkSize:          0,
		DownloadChunkSize:  -1,
	}

	fixed := cfg.Normalize()

	assert.Equal(t, DefaultHost, cfg.Host)
	assert.Equal(t, DefaultPort, cfg.Port)
	assert.Equal(t, DefaultSSHTimeout, cfg.SSHTimeout)
	assert.Equal(t, DefaultKeepaliveInterval, cfg.KeepaliveInterval)
	assert.Equal(t, DefaultSSHIdleTimeout, cfg.SSHIdleTimeout)
	assert.Equal(t, DefaultSessionIdleTimeout, cfg.SessionIdleTimeout)
	assert.Equal(t, DefaultChunkSize, cfg.ChunkSize)
	assert.Equal(t, DefaultDownloadChunkSize, cfg.DownloadChunkSize)

	// 每个被修正的字段都要有对应说明，供启动日志告警
	for _, want := range []string{"MANAGI_PORT", "MANAGI_SSH_TIMEOUT", "MANAGI_SFTP_CHUNK_SIZE", "MANAGI_HOST"} {
		assert.Contains(t, strings.Join(fixed, "; "), want)
	}
}

// TestNormalize_ReadDeadlineAbovePingInterval 验证读超时不会小于心跳间隔：
// 否则连接会在两次 ping 之间被自己判死，表现为周期性掉线。
func TestNormalize_ReadDeadlineAbovePingInterval(t *testing.T) {
	cfg := &Config{WSPingInterval: 30, WSReadDeadline: 10}
	fixed := cfg.Normalize()

	assert.Equal(t, 90, cfg.WSReadDeadline)
	assert.Contains(t, strings.Join(fixed, "; "), "MANAGI_WS_READ_DEADLINE")
}

// TestNormalize_KeepsValidValues 验证合法配置原样保留，并报告为空。
func TestNormalize_KeepsValidValues(t *testing.T) {
	cfg := &Config{
		Host: "127.0.0.1", Port: 8080, SSHTimeout: 30, KeepaliveInterval: 60,
		SSHIdleTimeout: 300, SSHPoolSize: 50, WSReadDeadline: 120, WSPingInterval: 20,
		SessionIdleTimeout: 90, ChunkSize: 4096, DownloadChunkSize: 2048,
	}
	before := *cfg

	assert.Empty(t, cfg.Normalize())
	assert.Equal(t, before.Host, cfg.Host)
	assert.Equal(t, before.Port, cfg.Port)
	assert.Equal(t, before.WSReadDeadline, cfg.WSReadDeadline)
	assert.Equal(t, before.ChunkSize, cfg.ChunkSize)
}

// TestNormalize_BoundsChunkSize 验证分片大小有上界：
// WS 单帧读取上限取 2×分片，前端又按下发值切片，一个数量级写错的环境变量
// 就能把单帧放大到几百 MB（服务端 OOM、浏览器跟着崩）。
func TestNormalize_BoundsChunkSize(t *testing.T) {
	cfg := &Config{
		Host: "127.0.0.1", Port: 8080, SSHTimeout: 30, KeepaliveInterval: 60,
		SSHIdleTimeout: 300, SSHPoolSize: 50, WSReadDeadline: 120, WSPingInterval: 20,
		SessionIdleTimeout: 90, ChunkSize: 1 << 30, DownloadChunkSize: 1 << 30,
	}
	fixed := cfg.Normalize()

	assert.Equal(t, MaxChunkSize, cfg.ChunkSize)
	assert.Equal(t, MaxDownloadChunkSize, cfg.DownloadChunkSize)
	assert.Contains(t, strings.Join(fixed, "; "), "MANAGI_SFTP_CHUNK_SIZE")
	assert.Contains(t, strings.Join(fixed, "; "), "MANAGI_SFTP_DOWNLOAD_CHUNK")
}

// TestExpandHome 验证 "~" 展开：用户习惯直接写 MANAGI_KNOWN_HOSTS=~/.ssh/known_hosts，
// 而 Go 自己不展开波浪号，不处理就会以「文件不存在」的形式在启动日志里冒出来。
// 已是绝对路径或相对路径的原样返回。
func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "测试依赖可用的用户目录")

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"波浪号单独", "~", home},
		{"波浪号加斜杠", "~/.ssh/known_hosts", filepath.Join(home, ".ssh", "known_hosts")},
		{"绝对路径不变", "/etc/ssh/known_hosts", "/etc/ssh/known_hosts"},
		{"相对路径不变", "known_hosts", "known_hosts"},
		{"波浪号不在开头不变", "a~b", "a~b"},
		{"空串不变", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, expandHome(c.input))
		})
	}

	// Windows 上用户可能照抄 cmd 写法 ~\.ssh\known_hosts
	if filepath.Separator != '/' {
		assert.Equal(t, filepath.Join(home, ".ssh", "known_hosts"), expandHome(`~\.ssh\known_hosts`))
	}
}

// TestLoad_KnownHostsTilde 验证 Load 会展开 MANAGI_KNOWN_HOSTS 的波浪号。
func TestLoad_KnownHostsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	t.Setenv("MANAGI_KNOWN_HOSTS", "~/.ssh/known_hosts")

	cfg := Load()
	assert.Equal(t, filepath.Join(home, ".ssh", "known_hosts"), cfg.KnownHostsFile)
}
