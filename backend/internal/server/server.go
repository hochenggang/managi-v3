// Package server 装配 Managi 的 HTTP 服务。
//
// 抽取动机：服务器入口与桌面托盘入口此前各自重复
// 「建 mux → 注册路由 → /health → BasicAuth → 超时参数」这段装配逻辑，
// 两处漂移即埋下不一致隐患（如超时、安全头只加在一处）。本包收敛为单一入口，
// 两种形态（cmd/managi 与 internal/desktop 的 -tray）只保留各自真正不同的部分。
//
// 调用方只需知道：New 返回一个可直接 ListenAndServe 的 *http.Server 与
// 底层 SSH 连接池；不应关心路由注册、中间件叠加等内部细节。
package server

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"managi/internal/config"
	"managi/internal/handler"
	"managi/internal/sshpool"
)

// New 装配并返回可监听的 *http.Server 与底层 SSH 连接池。
// done 用于通知后台 goroutine（连接池清理、认证限流）退出。
// 返回的 pool 由调用方在退出时调用 CloseAll 释放。
func New(cfg *config.Config, done <-chan struct{}) (*http.Server, *sshpool.Pool) {
	ensureBasicAuthPassword(cfg)

	mux := http.NewServeMux()
	pool := handler.Register(mux, cfg, done)
	mux.HandleFunc("/health", healthHandler())

	// BasicAuth 包裹全部路由（内部对 /health 放行），外层再叠访问日志与安全响应头。
	// 顺序：SecurityHeaders( AccessLog( BasicAuth( mux ) ) ) —— 访问日志在鉴权之内，
	// 未授权请求（含口令爆破尝试）也会被记录，且只记 path 不记凭据。
	h := handler.SecurityHeaders(handler.AccessLog(handler.BasicAuthMiddleware(cfg, done)(mux)))

	return &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second, // 防 Slowloris 慢速头攻击
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout 不设：WS / SFTP 下载为长连接，设写超时会误杀
	}, pool
}

// ensureBasicAuthPassword 启用 BasicAuth 但未配置密码时生成随机强口令。
// 取代固定弱默认值；密码仅能从启动日志获取，故提醒用户显式配置。
func ensureBasicAuthPassword(cfg *config.Config) {
	if cfg.BasicAuthEnabled && cfg.BasicAuthPassword == "" {
		cfg.BasicAuthPassword = handler.RandomBasicAuthPassword()
		slog.Warn("BasicAuth 已启用但未配置密码，已生成随机口令（请用 MANAGI_AUTH=user:pass 显式设置）",
			"generated_password", cfg.BasicAuthPassword)
	}
}

// healthHandler 返回探活端点，供 Docker healthcheck 与桌面端就绪探测使用。
// 该端点在 BasicAuth 中间件内被放行，未鉴权即可访问。
func healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}
