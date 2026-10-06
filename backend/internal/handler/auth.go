// Package handler - HTTP Basic Auth 中间件、认证失败速率限制、WS Origin 校验。
// 设计见 design-v5.md §4.1 与 plan: managi-v3-auth-conn-stability-fixes.md。
package handler

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"managi/internal/config"
)

// 认证失败速率限制参数（硬编码合理默认，保持简约）。
const (
	authFailWindow      = 60 * time.Second // 滑动窗口
	authFailMaxAttempts = 10               // 每窗口每 IP 最大失败次数
)

// authFailLimiter 每 IP 滑动窗口失败计数器。
type authFailLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time // ip → 失败时间戳列表
}

func newAuthFailLimiter() *authFailLimiter {
	return &authFailLimiter{attempts: make(map[string][]time.Time)}
}

// tooMany 返回该 IP 是否已超限（窗口内失败次数 >= 上限）。
func (l *authFailLimiter) tooMany(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-authFailWindow)
	ts := l.attempts[ip]
	// 丢弃过期时间戳
	keep := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	l.attempts[ip] = keep
	return len(keep) >= authFailMaxAttempts
}

// recordFailure 记录一次失败。
func (l *authFailLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[ip] = append(l.attempts[ip], time.Now())
}

// reset 清除该 IP 的失败记录（成功后调用）。
func (l *authFailLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

// Start 启动后台清理 goroutine，定期删除全过期的 IP 条目。
// 防止攻击者用大量不同 IP 发起失败认证导致 attempts map 无限增长。
// 接收 done channel，进程退出时停止协程，避免 goroutine 泄漏。
func (l *authFailLimiter) Start(done <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(authFailWindow)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				l.pruneExpired()
			}
		}
	}()
}

// pruneExpired 清理所有已过期条目（无持锁时间戳可保留时删除整个 IP 记录）。
func (l *authFailLimiter) pruneExpired() {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-authFailWindow)
	for ip, ts := range l.attempts {
		keep := ts[:0]
		for _, t := range ts {
			if t.After(cutoff) {
				keep = append(keep, t)
			}
		}
		if len(keep) == 0 {
			delete(l.attempts, ip)
		} else {
			l.attempts[ip] = keep
		}
	}
}

// BasicAuthMiddleware 返回 Basic Auth 中间件。
// cfg.BasicAuthEnabled == false 时透传（零开销）。
// 浏览器在首次 401+WWW-Authenticate 后缓存凭据，后续同源 HTTP 与 WebSocket 升级请求自动携带。
// 凭据为空（配置层 Validate 已拒绝这种配置启动）时例外：一切请求直接 401，
// 空口令绝不能被当作有效凭据比对通过。
// done 用于停止 limiter 后台 goroutine。
func BasicAuthMiddleware(cfg *config.Config, done <-chan struct{}) func(http.Handler) http.Handler {
	if !cfg.BasicAuthEnabled {
		return func(next http.Handler) http.Handler { return next }
	}
	limiter := newAuthFailLimiter()
	limiter.Start(done) // 启动后台清理，防止 attempts map 无限增长
	expectedUser := []byte(cfg.BasicAuthUser)
	expectedPass := []byte(cfg.BasicAuthPassword)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// /health 放行：Docker healthcheck / Tauri sidecar 探活
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}
			// 兜底闸门：空用户名或空口令一律拒绝。ConstantTimeCompare 对两个空串判等，
			// 少了这段，绕过配置层（直连 server.New）的「启用但无口令」会变成人人可进的假鉴权。
			if len(expectedUser) == 0 || len(expectedPass) == 0 {
				w.Header().Set("WWW-Authenticate", `Basic realm="managi", charset="UTF-8"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			ip := clientIP(r, cfg.TrustedProxies)
			if limiter.tooMany(ip) {
				writeJSONError(w, http.StatusTooManyRequests, "too many auth failures, retry later")
				return
			}
			user, pass, ok := r.BasicAuth()
			if !ok ||
				subtle.ConstantTimeCompare([]byte(user), expectedUser) != 1 ||
				subtle.ConstantTimeCompare([]byte(pass), expectedPass) != 1 {
				limiter.recordFailure(ip)
				w.Header().Set("WWW-Authenticate", `Basic realm="managi", charset="UTF-8"`)
				// 401 保持纯文本：浏览器弹出的是登录框，直接访问时人眼读到的也是这个 body，
				// JSON 反而添乱；调用方只需要状态码即可判定未授权。
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			limiter.reset(ip)
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP 提取客户端 IP。默认只取真实连接地址：任何人都能伪造 X-Forwarded-For，
// 无条件采信会让限流按伪造 IP 计数而形同虚设。
// 部署在反向代理（如 nginx）之后时用 MANAGI_TRUSTED_PROXIES 声明代理网段：
// 仅当对端落在可信网段内，才从 XFF 右侧取其后的第一个非代理地址——
// 代理是追加式的（$proxy_add_x_forwarded_for），最右侧才是真正连上来的地址，
// 客户端自己塞进前缀的伪造值不影响归属。
// 复用 net.SplitHostPort 正确处理 IPv6 地址（原手写按 ':' 截断会破坏 IPv6）。
func clientIP(r *http.Request, trusted []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr // 无端口的异常形态：原样使用，后续按不可信处理
	}
	if !ipInNets(net.ParseIP(host), trusted) {
		return host
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		cand := strings.TrimSpace(parts[i])
		ip := net.ParseIP(cand)
		if ip == nil {
			continue // 畸形条目跳过：宁可落回连接地址，也不采信解析不了的值
		}
		if !ipInNets(ip, trusted) {
			return cand
		}
	}
	return host // XFF 缺失或整条链都是可信代理：归因到直连的那一跳
}

// ipInNets ip 是否落在任一网段内；ip 为 nil（解析失败）或网段表为空一律 false。
func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// checkOrigin 校验 WebSocket 升级请求来源。
// Origin 为空 → 放行（非浏览器客户端如 Tauri sidecar / 测试工具）。
// 非空 → 必须与请求 Host 同源（防 WS CSRF）。
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}
