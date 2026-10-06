// Package main 是 Managi v3 后端入口：同一个二进制，两种形态。
//   - 默认：HTTP/WebSocket 服务器（跳板机部署，systemd / OpenRC / Docker）
//   - -tray：Windows 桌面托盘（只监听 127.0.0.1、内嵌前端、自动开浏览器）
//
// 设计见 design-v5.md 第四章。
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"managi/internal/config"
	"managi/internal/desktop"
	"managi/internal/server"
)

// 默认监听参数（提取常量，避免字面量重复）。
const (
	defaultPort = 18001
	defaultHost = "0.0.0.0"
)

func main() {
	port := flag.Int("port", defaultPort, "服务监听端口")
	host := flag.String("host", defaultHost, "服务监听地址")
	// 桌面构建（-tags desktop）默认进托盘：exe 用 -H=windowsgui 链接，双击后没有控制台，
	// 若默认走服务器形态就成了一个看不见的进程。-tray=false 可显式退回服务器形态。
	tray := flag.Bool("tray", desktop.DefaultEnabled, "托盘模式（Windows 桌面构建）：只监听 127.0.0.1，内嵌前端并自动打开浏览器")
	flag.Parse()

	cfg := config.Load()
	// 用 flag.Visit 检测 flag 是否被显式设置，而非值比较。
	// 值比较会在用户显式传 -port 18001（恰为默认值）时漏覆盖。
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "port":
			cfg.Port = *port
		case "host":
			cfg.Host = *host
		}
	})

	// 必须人工修正的配置（如 TLS 只配了证书或私钥）：拒绝启动，不静默降级
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid config", "err", err)
		os.Exit(1)
	}

	if *tray {
		if err := desktop.Run(cfg); err != nil {
			slog.Error("tray mode unavailable", "err", err)
			os.Exit(1)
		}
		return
	}

	// done channel 用于通知所有后台 goroutine 退出
	done := make(chan struct{})
	srv, pool := server.New(cfg, done)

	slog.Info("managi v3 starting", "addr", srv.Addr, "basicAuth", cfg.BasicAuthEnabled, "tls", cfg.TLSEnabled())
	// 免鉴权 + 非回环监听 = 开放跳板机：任何能连通该端口的人都能免密操作 SSH。
	// 不拒绝启动（反代终结鉴权的部署合法），但必须把后果说清楚。
	if !cfg.BasicAuthEnabled && !isLoopbackHost(cfg.Host) {
		slog.Warn("BasicAuth 未启用且监听地址非本机回环，端口可达者即可免密使用 SSH 跳板；建议设置 MANAGI_AUTH=user:pass 启用鉴权", "host", cfg.Host)
	}

	// 信号驱动的优雅关闭
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		close(done) // 通知后台 goroutine 退出

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("server shutdown error", "err", err)
		}
		if pool != nil {
			pool.CloseAll()
		}
	}()

	// TLS 证书对已配置就走 HTTPS/wss；否则明文（默认，前置反代终结 TLS 时也走这里）
	var serveErr error
	if cfg.TLSEnabled() {
		serveErr = srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
	} else {
		serveErr = srv.ListenAndServe()
	}
	if serveErr != nil && serveErr != http.ErrServerClosed {
		slog.Error("server failed", "err", serveErr)
		os.Exit(1)
	}
	slog.Info("managi v3 stopped")
}

// isLoopbackHost 判断监听地址是否仅限本机回环（"localhost" 或回环 IP）。
// 用于免鉴权告警的误报控制：绑 0.0.0.0 / 内网 IP / "::" 都算对外开放。
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
