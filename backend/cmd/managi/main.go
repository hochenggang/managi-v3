// Package main 是 Managi v3 后端入口。
// 对应 v2 的 app.py，启动 HTTP 服务并注册路由。
// 设计见 ../design-v3.md 第四章。
package main

import (
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"managi/internal/config"
	"managi/internal/handler"
)

// 默认监听参数（修复 S4：提取常量，避免字面量重复）。
const (
	defaultPort = 18001
	defaultHost = "0.0.0.0"
)

func main() {
	port := flag.Int("port", defaultPort, "服务监听端口")
	host := flag.String("host", defaultHost, "服务监听地址")
	flag.Parse()

	cfg := config.Load()
	if *port != defaultPort {
		cfg.Port = *port
	}
	if *host != defaultHost {
		cfg.Host = *host
	}

	// 启用 BasicAuth 且使用默认弱口令时告警
	if cfg.BasicAuthEnabled && cfg.BasicAuthUser == "admin" && cfg.BasicAuthPassword == "admin123" {
		slog.Warn("BasicAuth 启用但使用默认弱口令 admin/admin123，请通过 MANAGI_BASICAUTH_USERNAME/PASSWORD 修改")
	}

	mux := http.NewServeMux()
	handler.Register(mux, cfg)

	// 健康检查端点（供 Tauri sidecar 与 Docker healthcheck 使用）
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// BasicAuth 中间件包裹全部路由（内部对 /health 放行）
	finalHandler := basicAuthWrap(cfg, mux)

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	slog.Info("managi v3 starting", "addr", addr, "basicAuth", cfg.BasicAuthEnabled)
	server := &http.Server{
		Addr:              addr,
		Handler:           finalHandler,
		ReadHeaderTimeout: 10 * time.Second, // 防 Slowloris 慢速头攻击
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout 不设：WS / SFTP 下载为长连接，设写超时会误杀
	}
	if err := server.ListenAndServe(); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// basicAuthWrap 引入 handler 包的 BasicAuth 中间件。
// 独立函数避免 main 包直接依赖中间件实现细节。
func basicAuthWrap(cfg *config.Config, h http.Handler) http.Handler {
	return handler.BasicAuthMiddleware(cfg)(h)
}
