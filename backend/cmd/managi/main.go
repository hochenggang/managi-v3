// Package main 是 Managi v3 后端入口。
// 对应 v2 的 app.py，启动 HTTP 服务并注册路由。
// 设计见 ../design-v3.md 第四章。
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"

	"managi/internal/config"
	"managi/internal/handler"
)

func main() {
	port := flag.Int("port", 18001, "服务监听端口")
	host := flag.String("host", "0.0.0.0", "服务监听地址")
	flag.Parse()

	cfg := config.Load()
	if *port != 18001 {
		cfg.Port = *port
	}
	if *host != "0.0.0.0" {
		cfg.Host = *host
	}

	mux := http.NewServeMux()
	handler.Register(mux, cfg)

	// 健康检查端点（供 Tauri sidecar 与 Docker healthcheck 使用）
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := cfg.Host + ":" + itoa(cfg.Port)
	slog.Info("managi v3 starting", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// itoa 简易整数转字符串，避免引入 strconv 占行。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [16]byte{}
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
