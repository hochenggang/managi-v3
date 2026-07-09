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

	url := fmt.Sprintf("http://%s:%d", host, port)
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
	handler.Register(mux, cfg)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv = &http.Server{
		Addr:              net.JoinHostPort(host, itoa(port)),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // G112: 防 Slowloris 攻击
	}

	slog.Info("managi windows app starting", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func onExit() {
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
	url := fmt.Sprintf("http://%s:%d/health", host, port)
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

// itoa 将非负整数转为字符串。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [16]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
