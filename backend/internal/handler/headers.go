// Package handler - 安全响应头中间件。
// 统一为所有响应附加基础安全头，防御 MIME 嗅探、点击劫持与 Referer 泄漏。
//
// 刻意不设置 Content-Security-Policy：前端经 vite-plugin-singlefile 构建，
// JS/CSS 全部内联进 index.html，CSP 必须放行 'unsafe-inline' 才能运行，
// 一旦收紧即整页白屏，收益不抵风险，故只保留下列无副作用的头。
package handler

import "net/http"

// SecurityHeaders 返回为所有响应附加基础安全头的中间件。
// 对 /health 等内部探活端点同样无害，无需豁免。
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff") // 禁止浏览器按内容嗅探改写 Content-Type
		h.Set("X-Frame-Options", "DENY")           // 禁止被任意页面以 iframe 嵌入（防点击劫持）
		h.Set("Referrer-Policy", "no-referrer")    // 跳转外部时不携带来源（避免泄漏内部地址）
		next.ServeHTTP(w, r)
	})
}
