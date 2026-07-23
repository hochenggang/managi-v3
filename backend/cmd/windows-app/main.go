// Package main 是 Managi v3 Windows 托盘客户端。
// 单一可执行文件，内嵌 HTTP/WebSocket 服务、前端单页与托盘图标。
// 启动后进入系统托盘并自动通过默认浏览器打开 http://127.0.0.1:18001。

//go:build windows

package main

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/getlantern/systray"
	"github.com/pkg/browser"

	"managi/internal/config"
	"managi/internal/handler"
)

const (
	host = "127.0.0.1"
	port = 18001
)

//go:embed index.html
var indexHTML []byte

//go:embed icon.ico
var iconICO []byte

var srv *http.Server
// done 用于通知后台 goroutine 退出（修复 B10）
var done = make(chan struct{})

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetIcon(iconICO)
	systray.SetTitle("Managi")
	systray.SetTooltip("Managi v3")

	mOpen := systray.AddMenuItem("打开 Managi", "打开 Managi")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "退出 Managi")

	go runServer()

	// 修复 B33：用 strconv.Itoa + 字符串拼接替代 fmt.Sprintf，减少反射开销
	url := "http://" + host + ":" + strconv.Itoa(port)
	if err := waitForHealth(); err != nil {
		slog.Error("server health check failed", "err", err)
		systray.Quit()
		return
	}

	if err := browser.OpenURL(url); err != nil {
		slog.Error("open browser failed", "err", err)
	}

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				if err := browser.OpenURL(url); err != nil {
					slog.Error("open browser failed", "err", err)
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func runServer() {
	cfg := config.Load()
	cfg.Host = host
	cfg.Port = port
	cfg.IndexHTML = indexHTML

	mux := http.NewServeMux()
	handler.Register(mux, cfg, done)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// H9：与服务器端入口一致，应用 BasicAuth 中间件（cfg.BasicAuthEnabled=false 时透传）
	finalHandler := handler.BasicAuthMiddleware(cfg, done)(mux)

	srv = &http.Server{
		// 修复 B20：用 strconv.Itoa 替代自实现的 itoa，去除冗余代码
		Addr:              net.JoinHostPort(host, strconv.Itoa(port)),
		Handler:           finalHandler,
		ReadHeaderTimeout: 10 * time.Second, // G112: 防 Slowloris 攻击
	}

	slog.Info("managi windows app starting", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func onExit() {
	// 修复 B10：通知后台 goroutine 退出
	select {
	case <-done:
		// already closed
	default:
		close(done)
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("server shutdown failed", "err", err)
		}
	}
}

func waitForHealth() error {
	client := http.Client{Timeout: 200 * time.Millisecond}
	// 修复 B33：用 strconv.Itoa + 字符串拼接替代 fmt.Sprintf
	url := "http://" + host + ":" + strconv.Itoa(port) + "/health"
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("health check timed out")
}
