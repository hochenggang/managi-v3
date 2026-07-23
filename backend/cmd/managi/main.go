// Package main 是 Managi v3 后端入口。
// 对应 v2 的 app.py，启动 HTTP 服务并注册路由。
// 设计见 ../design-v3.md 第四章。
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
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
	// 修复 B35：用 flag.Visit 检测 flag 是否被显式设置，而非值比较。
	// 原逻辑 *port != defaultPort 会在用户显式传 -port 18001 时漏覆盖 cfg。
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "port":
			cfg.Port = *port
		case "host":
			cfg.Host = *host
		}
	})

	// 启用 BasicAuth 且使用默认弱口令时告警
	if cfg.BasicAuthEnabled && cfg.BasicAuthUser == "admin" && cfg.BasicAuthPassword == "admin123" {
		slog.Warn("BasicAuth 启用但使用默认弱口令 admin/admin123，请通过 MANAGI_BASICAUTH_USERNAME/PASSWORD 修改")
	}

	// 修复 B9/B10：done channel 用于通知所有后台 goroutine 退出
	done := make(chan struct{})

	mux := http.NewServeMux()
	pool := handler.Register(mux, cfg, done)

	// 健康检查端点（供 Windows 桌面端与 Docker healthcheck 使用）
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// BasicAuth 中间件包裹全部路由（内部对 /health 放行），外层加请求日志
	loggedMux := handler.RequestLogMiddleware(mux)
	finalHandler := basicAuthWrap(cfg, loggedMux, done)

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

	// 修复 B9：信号驱动的优雅关闭
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		close(done) // 通知后台 goroutine 退出

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			slog.Error("server shutdown error", "err", err)
		}
		if pool != nil {
			pool.CloseAll()
		}
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
	slog.Info("managi v3 stopped")
}

// basicAuthWrap 引入 handler 包的 BasicAuth 中间件。
// 独立函数避免 main 包直接依赖中间件实现细节。
func basicAuthWrap(cfg *config.Config, h http.Handler, done <-chan struct{}) http.Handler {
	return handler.BasicAuthMiddleware(cfg, done)(h)
}
