// Package main 是 Managi v3 Windows 托盘客户端。
// 单一可执行文件，内嵌 HTTP/WebSocket 服务、前端单页与托盘图标。
// 启动后进入系统托盘并自动通过默认浏览器打开 http://127.0.0.1:18001。
// 默认端口被占用时自动顺延，并把失败原因显示在托盘上（不再静默退出）。

//go:build windows

package main

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/getlantern/systray"
	"github.com/pkg/browser"

	"managi/internal/config"
	"managi/internal/server"
	"managi/internal/sshpool"
)

const (
	host = "127.0.0.1"
	port = 18001
	// portAttempts 端口冲突时的顺延次数：桌面端最常见的是重复启动或旧实例未退净，
	// 与其弹一句错误退出，不如换端口起来。
	portAttempts = 5
	// tooltipMax Windows 托盘 tooltip 截断在 128 字符，超长会被系统吃掉尾巴
	tooltipMax = 100
)

//go:embed index.html
var indexHTML []byte

//go:embed icon.ico
var iconICO []byte

var srv *http.Server
var pool *sshpool.Pool

// done 用于通知后台 goroutine 退出
var done = make(chan struct{})

// activePort 是实际监听端口（默认端口被占用后会顺延）。
var activePort atomic.Int32

// startResult 是服务启动结果：成功带回监听端口，失败带回可直接展示给用户的错误。
type startResult struct {
	port int
	err  error
}

// srvEvents 由 runServer 投递；托盘状态只能在托盘侧修改，故用 channel 传出去。
var srvEvents = make(chan startResult, 2)

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
	go watchStartup()

	for {
		select {
		case <-mOpen.ClickedCh:
			if err := browser.OpenURL(appURL(int(activePort.Load()))); err != nil {
				slog.Error("open browser failed", "err", err)
			}
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// watchStartup 等启动结果：成功则等服务就绪后开浏览器，失败或中途停服则显示在托盘上。
func watchStartup() {
	res := <-srvEvents
	if res.err != nil {
		showTrayError(res.err)
		return
	}
	if err := openInBrowser(res.port); err != nil {
		slog.Error("server health check failed", "err", err)
		showTrayError(err)
	}
	// 运行期 Serve 失败（端口被抢、监听器异常）同样可见，而不是留下一个死图标
	for ev := range srvEvents {
		if ev.err != nil {
			showTrayError(ev.err)
		}
	}
}

// openInBrowser 等待 /health 就绪后通过默认浏览器打开首页。
func openInBrowser(p int) error {
	if err := waitForHealth(p); err != nil {
		return err
	}
	return browser.OpenURL(appURL(p))
}

// runServer 绑定端口并提供服务。失败一律投递到 srvEvents：
// 此前这里 os.Exit(1)，图标一闪就没，用户完全不知道发生了什么。
func runServer() {
	ln, p, err := bindFirstFree()
	if err != nil {
		srvEvents <- startResult{err: err}
		return
	}
	//nolint:gosec // G115: p 取自 port..port+portAttempts 的小区间，转 int32 不会溢出
	activePort.Store(int32(p))

	cfg := config.Load()
	cfg.Host = host
	cfg.Port = p
	cfg.IndexHTML = indexHTML

	// 与服务器端入口共用同一套装配（路由 / 探活 / 鉴权 / 安全头 / 超时）
	srv, pool = server.New(cfg, done)
	srvEvents <- startResult{port: p}

	slog.Info("managi windows app starting", "addr", ln.Addr().String())
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		slog.Error("server failed", "err", err)
		srvEvents <- startResult{err: fmt.Errorf("服务已停止: %w", err)}
	}
}

// bindFirstFree 从默认端口起依次顺延，返回首个可用监听器及其端口。
func bindFirstFree() (net.Listener, int, error) {
	var lastErr error
	for p := port; p < port+portAttempts; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			if p != port {
				slog.Warn("default port busy, listening on fallback port", "wanted", port, "actual", p)
			}
			return ln, p, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("端口 %d-%d 均被占用，请退出其它 Managi 实例后重试: %w",
		port, port+portAttempts-1, lastErr)
}

// showTrayError 把失败原因显示在托盘上：图标 tooltip 概览，菜单项给出完整信息。
func showTrayError(err error) {
	msg := err.Error()
	slog.Error("startup failed", "err", err)
	systray.SetTooltip("Managi 启动失败: " + truncate(msg, tooltipMax))
	item := systray.AddMenuItem("启动失败", msg)
	item.Disable() // 置灰只作展示，点击无意义
}

// appURL 返回首页地址。strconv.Itoa 替代 fmt.Sprintf，省掉反射开销。
func appURL(p int) string {
	return "http://" + host + ":" + strconv.Itoa(p)
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func onExit() {
	// 通知后台 goroutine 退出
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
	if pool != nil {
		pool.CloseAll()
	}
}

// waitForHealth 轮询 /health 直到服务就绪或超时。
func waitForHealth(p int) error {
	client := http.Client{Timeout: 200 * time.Millisecond}
	url := appURL(p) + "/health"
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
	return fmt.Errorf("服务健康检查超时: %s", url)
}
