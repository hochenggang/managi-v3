// Package handler 实现 HTTP 与 WebSocket 端点。
// 对应 v2 的 routers.py，路由保持兼容。
// 设计见 ../design-v3.md §4.1 与 §4.4。
package handler

import (
	"bytes"
	"net/http"
	"path/filepath"
	"time"

	"managi/internal/config"
	"managi/internal/sshpool"
)

// Register 注册全部路由到给定 mux。
func Register(mux *http.ServeMux, cfg *config.Config) {
	// 修复 A19：启动时将相对 IndexHTMLPath 转为绝对路径，避免 CWD 不确定时 404
	if cfg.IndexHTMLPath != "" && !filepath.IsAbs(cfg.IndexHTMLPath) {
		if abs, err := filepath.Abs(cfg.IndexHTMLPath); err == nil {
			cfg.IndexHTMLPath = abs
		}
	}

	pool := sshpool.New(cfg)
	pool.StartCleaner()

	// 静态首页（v2 GET /）
	mux.HandleFunc("/", indexHandler(cfg))

	// SSH 命令执行
	mux.HandleFunc("/api/ssh/test", testHandler(pool, cfg))
	mux.HandleFunc("/api/ssh/batch", batchHandler(pool, cfg))

	// WebSocket 端点
	mgr := newSessionManager(pool, cfg)
	mux.HandleFunc("/ws", terminalWSHandler(mgr, cfg))
	mux.HandleFunc("/ws/sftp", sftpWSHandler(pool, cfg))

	// v3 新增：SFTP 下载（HTTP Range，断点续传）
	mux.HandleFunc("/api/sftp/download", sftpDownloadHandler(pool, cfg))
}

func indexHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if len(cfg.IndexHTML) > 0 {
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(cfg.IndexHTML))
			return
		}
		http.ServeFile(w, r, cfg.IndexHTMLPath)
	}
}
