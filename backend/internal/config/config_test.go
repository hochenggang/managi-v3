package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLoad_Defaults 验证所有字段的默认值。
func TestLoad_Defaults(t *testing.T) {
	// 清空所有相关环境变量，确保使用默认值
	keys := []string{
		"MANAGI_HOST", "MANAGI_PORT", "MANAGI_SSH_TIMEOUT", "MANAGI_KEEPALIVE", "MANAGI_SSH_IDLE_TIMEOUT",
		"MANAGI_WS_READ_DEADLINE", "MANAGI_WS_PING_INTERVAL", "MANAGI_SFTP_CHUNK_SIZE", "MANAGI_SFTP_DOWNLOAD_CHUNK",
		"MANAGI_BASICAUTH_ENABLED", "MANAGI_BASICAUTH_USERNAME", "MANAGI_BASICAUTH_PASSWORD",
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
	assert.Equal(t, 90, cfg.WSReadDeadline)
	assert.Equal(t, 30, cfg.WSPingInterval)
	assert.Equal(t, 1<<20, cfg.ChunkSize) // 1MB
	assert.Equal(t, 1<<16, cfg.DownloadChunkSize)
	assert.False(t, cfg.BasicAuthEnabled)
	assert.Equal(t, "admin", cfg.BasicAuthUser)
	assert.Equal(t, "admin123", cfg.BasicAuthPassword)
	// 修复 B36：IndexHTMLPath 现在在 Load 中转为绝对路径
	assert.True(t, filepath.IsAbs(cfg.IndexHTMLPath), "IndexHTMLPath should be absolute")
	assert.True(t, filepath.Base(cfg.IndexHTMLPath) == "index.html", "IndexHTMLPath base should be index.html")
}

// TestLoad_EnvOverride 验证环境变量覆盖默认值。
func TestLoad_EnvOverride(t *testing.T) {
	// 使用跨平台绝对路径，避免 Windows 上 /var/www 被视为相对路径
	absPath := filepath.Join(t.TempDir(), "index.html")
	t.Setenv("MANAGI_HOST", "192.168.1.1")
	t.Setenv("MANAGI_PORT", "8080")
	t.Setenv("MANAGI_SSH_TIMEOUT", "30")
	t.Setenv("MANAGI_KEEPALIVE", "60")
	t.Setenv("MANAGI_SSH_IDLE_TIMEOUT", "300")
	t.Setenv("MANAGI_WS_READ_DEADLINE", "120")
	t.Setenv("MANAGI_WS_PING_INTERVAL", "20")
	t.Setenv("MANAGI_SFTP_CHUNK_SIZE", "2097152")
	t.Setenv("MANAGI_SFTP_DOWNLOAD_CHUNK", "4096")
	t.Setenv("MANAGI_BASICAUTH_ENABLED", "true")
	t.Setenv("MANAGI_BASICAUTH_USERNAME", "ops")
	t.Setenv("MANAGI_BASICAUTH_PASSWORD", "secret")
	t.Setenv("MANAGI_INDEX_HTML", absPath)

	cfg := Load()
	assert.Equal(t, "192.168.1.1", cfg.Host)
	assert.Equal(t, 8080, cfg.Port)
	assert.Equal(t, 30, cfg.SSHTimeout)
	assert.Equal(t, 60, cfg.KeepaliveInterval)
	assert.Equal(t, 300, cfg.SSHIdleTimeout)
	assert.Equal(t, 120, cfg.WSReadDeadline)
	assert.Equal(t, 20, cfg.WSPingInterval)
	assert.Equal(t, 2097152, cfg.ChunkSize)
	assert.Equal(t, 4096, cfg.DownloadChunkSize)
	assert.True(t, cfg.BasicAuthEnabled)
	assert.Equal(t, "ops", cfg.BasicAuthUser)
	assert.Equal(t, "secret", cfg.BasicAuthPassword)
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
