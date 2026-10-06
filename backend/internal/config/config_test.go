package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearEnv 清空全部受支持的环境变量，让 Load 走默认值路径。
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"MANAGI_HOST", "MANAGI_PORT", "MANAGI_AUTH", "MANAGI_KNOWN_HOSTS", "MANAGI_INDEX_HTML",
		"MANAGI_TLS_CERT", "MANAGI_TLS_KEY", "MANAGI_TRUSTED_PROXIES",
	} {
		t.Setenv(k, "")
	}
}

// TestLoad_Defaults 验证所有字段的默认值。
func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)

	cfg := Load()
	assert.Equal(t, "0.0.0.0", cfg.Host)
	assert.Equal(t, 18001, cfg.Port)
	assert.Equal(t, 15, cfg.SSHTimeout)
	assert.Equal(t, 30, cfg.KeepaliveInterval)
	assert.Equal(t, 120, cfg.SSHIdleTimeout)
	assert.Equal(t, 20, cfg.SSHPoolSize)
	assert.Equal(t, 90, cfg.WSReadDeadline)
	assert.Equal(t, 30, cfg.WSPingInterval)
	assert.Equal(t, 60, cfg.SessionIdleTimeout)
	assert.Equal(t, 1<<20, cfg.ChunkSize) // 1MB
	assert.Equal(t, 1<<16, cfg.DownloadChunkSize)
	// MANAGI_AUTH 未填即不启用鉴权
	assert.False(t, cfg.BasicAuthEnabled)
	assert.Equal(t, "", cfg.BasicAuthUser)
	assert.Equal(t, "", cfg.BasicAuthPassword)
	// 默认沿用 TOFU（不设 known_hosts）
	assert.Equal(t, "", cfg.KnownHostsFile)
	// TOFU 信任库默认落在 ~/.managi/known_hosts（不碰 OpenSSH 自己的 known_hosts）
	home, err := os.UserHomeDir()
	require.NoError(t, err, "测试依赖可用的用户目录")
	assert.Equal(t, filepath.Join(home, ".managi", "known_hosts"), cfg.TOFUKnownHostsFile)
	// 默认明文，且无可信代理：XFF 一律不采信
	assert.False(t, cfg.TLSEnabled())
	assert.Empty(t, cfg.TrustedProxies)
	// IndexHTMLPath 在 Load 中转为绝对路径
	assert.True(t, filepath.IsAbs(cfg.IndexHTMLPath), "IndexHTMLPath should be absolute")
	assert.Equal(t, "index.html", filepath.Base(cfg.IndexHTMLPath))
}

// TestLoad_EnvOverride 验证受支持的环境变量覆盖默认值。
func TestLoad_EnvOverride(t *testing.T) {
	// 使用跨平台绝对路径，避免 Windows 上 /var/www 被视为相对路径
	absPath := filepath.Join(t.TempDir(), "index.html")
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	certPath := filepath.Join(t.TempDir(), "cert.pem")
	keyPath := filepath.Join(t.TempDir(), "key.pem")
	clearEnv(t)
	t.Setenv("MANAGI_HOST", "192.168.1.1")
	t.Setenv("MANAGI_PORT", "8080")
	t.Setenv("MANAGI_AUTH", "ops:sec:ret")
	t.Setenv("MANAGI_KNOWN_HOSTS", knownHosts)
	t.Setenv("MANAGI_INDEX_HTML", absPath)
	t.Setenv("MANAGI_TLS_CERT", certPath)
	t.Setenv("MANAGI_TLS_KEY", keyPath)
	t.Setenv("MANAGI_TRUSTED_PROXIES", "10.0.0.0/8, 127.0.0.1")

	cfg := Load()
	assert.Equal(t, "192.168.1.1", cfg.Host)
	assert.Equal(t, 8080, cfg.Port)
	assert.True(t, cfg.BasicAuthEnabled)
	assert.Equal(t, "ops", cfg.BasicAuthUser)
	// 只按首个冒号切分：密码里带冒号不会被截断
	assert.Equal(t, "sec:ret", cfg.BasicAuthPassword)
	assert.Equal(t, knownHosts, cfg.KnownHostsFile)
	// 已是绝对路径，Load 不会修改
	assert.Equal(t, absPath, cfg.IndexHTMLPath)
	assert.True(t, cfg.TLSEnabled())
	assert.Equal(t, certPath, cfg.TLSCertFile)
	assert.Equal(t, keyPath, cfg.TLSKeyFile)
	require.Len(t, cfg.TrustedProxies, 2)
	assert.True(t, cfg.TrustedProxies[0].Contains(net.ParseIP("10.1.2.3")))
	assert.True(t, cfg.TrustedProxies[1].Contains(net.ParseIP("127.0.0.1")))
}

// TestValidate_TLSPairing 验证 TLS 证书/私钥必须成对：
// 只配一半时运维意图明确是 HTTPS，静默退回明文是一次安全降级，必须拒绝启动。
func TestValidate_TLSPairing(t *testing.T) {
	assert.NoError(t, (&Config{}).Validate())

	cfg := &Config{TLSCertFile: "cert.pem"}
	assert.Error(t, cfg.Validate())

	cfg = &Config{TLSKeyFile: "key.pem"}
	assert.Error(t, cfg.Validate())

	cfg = &Config{TLSCertFile: "cert.pem", TLSKeyFile: "key.pem"}
	assert.NoError(t, cfg.Validate())
	assert.True(t, cfg.TLSEnabled())
}

// TestValidate_EmptyAuthPassword 验证启用鉴权但密码为空会拒绝启动：
// 空口令不构成有效凭据，兜底生成随机口令等于把密码轮换进日志，必须让部署显式修配置。
func TestValidate_EmptyAuthPassword(t *testing.T) {
	assert.Error(t, (&Config{BasicAuthEnabled: true}).Validate())

	cfg := &Config{BasicAuthEnabled: true, BasicAuthPassword: "secret"}
	assert.NoError(t, cfg.Validate())

	// 未启用鉴权时密码为空无妨：那是「明确不鉴权」的正常形态
	assert.NoError(t, (&Config{}).Validate())
}

// TestParseTrustedProxies 验证网段解析：CIDR 与裸 IP 都接受，非法项出声跳过。
func TestParseTrustedProxies(t *testing.T) {
	nets := parseTrustedProxies(" 10.0.0.0/8 , 127.0.0.1 , ::1 , junk , , 192.168.1.0/24")
	require.Len(t, nets, 4)

	assert.True(t, nets[0].Contains(net.ParseIP("10.9.9.9")))
	assert.True(t, nets[1].Contains(net.ParseIP("127.0.0.1")))
	assert.False(t, nets[1].Contains(net.ParseIP("127.0.0.2")))
	assert.True(t, nets[2].Contains(net.ParseIP("::1")))
	assert.False(t, nets[2].Contains(net.ParseIP("::2")))
	assert.True(t, nets[3].Contains(net.ParseIP("192.168.1.55")))

	assert.Empty(t, parseTrustedProxies(""))
}

// TestParseBasicAuth 验证 MANAGI_AUTH 的解析：填了凭据就是要鉴权，
// 不再存在「密码写了但开关没开」这种静默不生效的组合。
func TestParseBasicAuth(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantUser string
		wantPass string
	}{
		{"未填", "", "", ""},
		{"空白", "   ", "", ""},
		{"user:pass", "admin:pw", "admin", "pw"},
		{"两侧空白", " admin:pw ", "admin", "pw"},
		{"密码含冒号", "admin:a:b", "admin", "a:b"},
		{"缺冒号整串当密码", "pw-only", DefaultBasicAuthUser, "pw-only"},
		{"空用户名", ":pw", DefaultBasicAuthUser, "pw"},
		{"空密码（解析保留，Validate 拒绝启动）", "admin:", "admin", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			user, pass := parseBasicAuth(c.input)
			assert.Equal(t, c.wantUser, user)
			assert.Equal(t, c.wantPass, pass)
		})
	}
}

// TestEnvInt_Invalid 验证非数字端口回退默认值（且出声告警，不静默生效）。
func TestEnvInt_Invalid(t *testing.T) {
	clearEnv(t)
	t.Setenv("MANAGI_PORT", "abc")

	assert.Equal(t, 18001, Load().Port)
}

// TestLoad_RelativePathConvertedToAbsolute 验证相对 IndexHTMLPath 被转为绝对路径（B36 修复）。
func TestLoad_RelativePathConvertedToAbsolute(t *testing.T) {
	clearEnv(t)
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
		SSHPoolSize:        0,
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
	assert.Equal(t, DefaultSSHPoolSize, cfg.SSHPoolSize)
	assert.Equal(t, DefaultSessionIdleTimeout, cfg.SessionIdleTimeout)
	assert.Equal(t, DefaultChunkSize, cfg.ChunkSize)
	assert.Equal(t, DefaultDownloadChunkSize, cfg.DownloadChunkSize)

	// 每个被修正的字段都要有对应说明，供启动日志告警
	for _, want := range []string{"Port", "SSHTimeout", "ChunkSize", "Host"} {
		assert.Contains(t, strings.Join(fixed, "; "), want)
	}
}

// TestNormalize_ReadDeadlineAbovePingInterval 验证读超时不会小于心跳间隔：
// 否则连接会在两次 ping 之间被自己判死，表现为周期性掉线。
func TestNormalize_ReadDeadlineAbovePingInterval(t *testing.T) {
	cfg := &Config{WSPingInterval: 30, WSReadDeadline: 10}
	fixed := cfg.Normalize()

	assert.Equal(t, 90, cfg.WSReadDeadline)
	assert.Contains(t, strings.Join(fixed, "; "), "WSReadDeadline")
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
	assert.Equal(t, before, *cfg)
}

// TestNormalize_BoundsChunkSize 验证分片大小有上界：
// WS 单帧读取上限取 2×分片，前端又按下发值切片，一个数量级写错的常量
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
	assert.Contains(t, strings.Join(fixed, "; "), "ChunkSize")
	assert.Contains(t, strings.Join(fixed, "; "), "DownloadChunkSize")
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
	clearEnv(t)
	t.Setenv("MANAGI_KNOWN_HOSTS", "~/.ssh/known_hosts")

	cfg := Load()
	assert.Equal(t, filepath.Join(home, ".ssh", "known_hosts"), cfg.KnownHostsFile)
}
