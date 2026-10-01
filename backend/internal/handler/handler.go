// Package handler 实现 HTTP 与 WebSocket 端点。
// 对应 v2 的 routers.py，但路由已换代：/ws/ssh + /ws/sftp 合成单条 /ws。
// 设计见 design-v5.md §4.1 与 §4.4。
package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"managi/internal/config"
	"managi/internal/sshpool"
)

// Register 注册全部路由到给定 mux。
// done 用于通知后台 goroutine（pool cleaner 等）退出。
// POST-only 端点统一用 postOnly 包装：方法校验只写一处。
// 不用 ServeMux 的 "POST /x" 模式——"/" 首页兜底匹配所有方法，会让方法不匹配的
// 请求绕过 405 逻辑，被当成首页返回 200。
func Register(mux *http.ServeMux, cfg *config.Config, done <-chan struct{}) *sshpool.Pool {
	pool := sshpool.New(cfg)
	pool.StartCleaner(done)

	// 静态首页（v2 GET /）
	mux.HandleFunc("/", indexHandler(cfg))

	// SSH 命令执行（「测试连接」已由终端/SFTP 通道本身承担，无需单独端点）
	mux.HandleFunc("/api/ssh/batch", postOnly(batchHandler(pool)))

	// WebSocket：一条连接承载全部通道（终端 = PTY 通道，文件管理 = SFTP 通道）
	mgr := newSessionManager(pool, cfg)
	mux.HandleFunc("/ws", wsHandler(mgr, pool, cfg))

	// v3 新增：SFTP 下载（HTTP Range，断点续传）
	mux.HandleFunc("/api/sftp/download", postOnly(sftpDownloadHandler(pool)))

	// 未注册的 /api/* 回 JSON 404（精确路径优先于该子树模式，不影响已注册端点）
	mux.HandleFunc("/api/", apiNotFoundHandler)

	return pool
}

// postOnly 方法白名单包装：非 POST 回 405 + Allow，凭据因此不可能经 URL 查询串传入。
func postOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed: use POST")
			return
		}
		h(w, r)
	}
}

// apiNotFoundHandler 处理未注册的 /api/ 路径：回 JSON 而不是首页 HTML。
func apiNotFoundHandler(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusNotFound, "unknown api path: "+r.Method+" "+r.URL.Path)
}

// indexHandler 返回前端首页。页面缺失时给出可操作的提示，而不是 ServeFile 的裸错误。
func indexHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(cfg.IndexHTML) > 0 {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(cfg.IndexHTML))
			return
		}
		if _, err := os.Stat(cfg.IndexHTMLPath); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("未找到前端页面: " + cfg.IndexHTMLPath +
				"\n请先构建前端（frontend: npm run build）或用 MANAGI_INDEX_HTML 指定 index.html 路径。"))
			return
		}
		http.ServeFile(w, r, cfg.IndexHTMLPath)
	}
}

// writeJSONError 输出统一的错误体 {"error": "..."}。
// 前端据此展示具体原因（如认证失败、目标不可达），而不是只剩一个状态码。
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// writeJSON 输出成功响应体。
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSONRequest 读取并解析 JSON 请求体：限幅防 OOM，失败已回 400 并返回 false。
func decodeJSONRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}
