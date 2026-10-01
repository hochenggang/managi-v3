// Package handler - HTTP 访问日志中间件。
//
// 动机：此前只有 SSH/WS 的 slog 事件，运维看不到「谁在打哪个接口、多久、返回什么」，
// 排查慢请求与异常流量只能靠猜。放在 BasicAuth 之内、SecurityHeaders 之外：
// 未鉴权请求（含 401 暴力尝试）也要留下痕迹。
//
// 隐私红线：只记 path，不记 RawQuery，更不记任何 Header —— 本项目的节点凭据
// 一律走 POST 请求体，日志里出现 auth 相关字段就等于把密码抄进了 journald。
package handler

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// AccessLog 记录每个请求的一行访问日志（方法、路径、状态、字节数、耗时、来源 IP）。
// remote 与 BasicAuth 限流同源（都取真实连接地址），排查时对得上号。
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /health 由容器健康检查按秒级轮询，记下来只会淹没真实访问
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		rec := &loggedResponse{ResponseWriter: w, start: time.Now()}
		next.ServeHTTP(rec, r)
		slog.Info("http access",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.statusCode(),
			"bytes", rec.written,
			"duration_ms", time.Since(rec.start).Milliseconds(),
			"remote", clientIP(r),
		)
	})
}

// loggedResponse 记录状态码与写出字节数，并透传 Flusher / Hijacker：
// WS 升级依赖 Hijack，流式下载依赖 Flush，包一层就把这两个功能弄坏了。
type loggedResponse struct {
	http.ResponseWriter
	status   int
	written  int
	hijacked bool
	start    time.Time
}

func (l *loggedResponse) WriteHeader(status int) {
	if l.status == 0 {
		l.status = status
	}
	l.ResponseWriter.WriteHeader(status)
}

func (l *loggedResponse) Write(b []byte) (int, error) {
	if l.status == 0 {
		l.status = http.StatusOK // 与 net/http 一致：隐式 200
	}
	n, err := l.ResponseWriter.Write(b)
	l.written += n
	return n, err
}

func (l *loggedResponse) Flush() {
	if f, ok := l.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (l *loggedResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := l.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("access log: underlying ResponseWriter is not a Hijacker")
	}
	conn, buf, err := h.Hijack()
	if err == nil {
		l.hijacked = true
	}
	return conn, buf, err
}

// statusCode 返回最终状态码；WS 升级后由裸连接继续通信，此处按 101 记。
func (l *loggedResponse) statusCode() int {
	switch {
	case l.status != 0:
		return l.status
	case l.hijacked:
		return http.StatusSwitchingProtocols
	default:
		return http.StatusOK
	}
}
