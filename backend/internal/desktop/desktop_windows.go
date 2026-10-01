//go:build windows && desktop

package desktop

import (
	"context"
	_ "embed"
	"errors"
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
	// loopbackHost 桌面形态只监听回环：这是本机管理端，不应对外提供服务。
	loopbackHost = "127.0.0.1"
	// portAttempts 端口冲突时的顺延次数：桌面端最常见的是重复启动或旧实例未退净，
	// 与其弹一句错误退出，不如换端口起来。
	portAttempts = 5
	// tooltipMax Windows 托盘 tooltip 截断在 128 字符，超长会被系统吃掉尾巴
	tooltipMax = 100
)

// DefaultEnabled 表示该构建形态默认值就是托盘模式：桌面 exe 用 -H=windowsgui
// 链接，双击没有控制台，让它默认起服务会看不见也停不掉；-tray=false 可显式改回服务器形态。
const DefaultEnabled = true

// 内嵌资源由构建前复制进本目录（见 Makefile 与 .github/workflows），不入库。
var (
	//go:embed index.html
	indexHTML []byte

	//go:embed icon.ico
	iconICO []byte
)

// startResult 是服务启动结果：成功带回监听端口，失败带回可直接展示给用户的错误。
type startResult struct {
	port int
	err  error
}

// trayApp 托盘应用的生命周期状态。服务端跑在独立 goroutine 上，
// 只能通过 events 把结果交给托盘侧——托盘项只能在托盘所属的循环里增删。
type trayApp struct {
	cfg    *config.Config
	done   chan struct{}
	events chan startResult

	// activePort 是实际监听端口（默认端口被占用后会顺延）
	activePort atomic.Int32

	srv  *http.Server
	pool *sshpool.Pool
}

// Run 启动托盘应用并阻塞到用户选择退出。cfg.Port 作为首选端口。
func Run(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("desktop: nil config")
	}
	cfg.Host = loopbackHost
	cfg.IndexHTML = indexHTML

	a := &trayApp{cfg: cfg, done: make(chan struct{}), events: make(chan startResult, 2)}
	systray.Run(a.onReady, a.onExit)
	return nil
}

func (a *trayApp) onReady() {
	systray.SetIcon(iconICO)
	systray.SetTitle("Managi")
	systray.SetTooltip("Managi v3")

	mOpen := systray.AddMenuItem("打开 Managi", "打开 Managi")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "退出 Managi")

	go a.runServer()
	go a.watchStartup()

	for {
		select {
		case <-mOpen.ClickedCh:
			if err := browser.OpenURL(appURL(int(a.activePort.Load()))); err != nil {
				slog.Error("open browser failed", "err", err)
			}
		case <-mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// watchStartup 等启动结果：成功则等服务就绪后开浏览器，失败或中途停服则显示在托盘上。
func (a *trayApp) watchStartup() {
	res := <-a.events
	if res.err != nil {
		showTrayError(res.err)
		return
	}
	if err := openInBrowser(res.port); err != nil {
		slog.Error("server health check failed", "err", err)
		showTrayError(err)
	}
	// 运行期 Serve 失败（端口被抢、监听器异常）同样可见，而不是留下一个死图标
	for ev := range a.events {
		if ev.err != nil {
			showTrayError(ev.err)
		}
	}
}

// runServer 绑定端口并提供服务。失败一律投递到 events：
// 此前这里 os.Exit(1)，图标一闪就没，用户完全不知道发生了什么。
func (a *trayApp) runServer() {
	ln, p, err := bindFirstFree(a.cfg.Port)
	if err != nil {
		a.events <- startResult{err: err}
		return
	}
	//nolint:gosec // G115: p 取自首选端口起的小区间，转 int32 不会溢出
	a.activePort.Store(int32(p))
	a.cfg.Port = p

	// 与服务器入口共用同一套装配（路由 / 探活 / 鉴权 / 安全头 / 超时）
	a.srv, a.pool = server.New(a.cfg, a.done)
	a.events <- startResult{port: p}

	slog.Info("managi desktop starting", "addr", ln.Addr().String())
	if err := a.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "err", err)
		a.events <- startResult{err: fmt.Errorf("服务已停止: %w", err)}
	}
}

// bindFirstFree 从首选端口起依次顺延，返回首个可用监听器及其端口。
func bindFirstFree(preferred int) (net.Listener, int, error) {
	var lastErr error
	for p := preferred; p < preferred+portAttempts; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(loopbackHost, strconv.Itoa(p)))
		if err == nil {
			if p != preferred {
				slog.Warn("default port busy, listening on fallback port", "wanted", preferred, "actual", p)
			}
			return ln, p, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("端口 %d-%d 均被占用，请退出其它 Managi 实例后重试: %w",
		preferred, preferred+portAttempts-1, lastErr)
}

// showTrayError 把失败原因显示在托盘上：图标 tooltip 概览，菜单项给出完整信息。
func showTrayError(err error) {
	msg := err.Error()
	slog.Error("startup failed", "err", err)
	systray.SetTooltip("Managi 启动失败: " + truncate(msg, tooltipMax))
	item := systray.AddMenuItem("启动失败", msg)
	item.Disable() // 置灰只作展示，点击无意义
}

// openInBrowser 等待 /health 就绪后通过默认浏览器打开首页。
func openInBrowser(p int) error {
	if err := waitForHealth(p); err != nil {
		return err
	}
	return browser.OpenURL(appURL(p))
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

func appURL(p int) string {
	return "http://" + loopbackHost + ":" + strconv.Itoa(p)
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// onExit 是 systray 退出回调：先通知后台 goroutine，再优雅关闭服务与连接池。
func (a *trayApp) onExit() {
	select {
	case <-a.done:
		// already closed
	default:
		close(a.done)
	}
	if a.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.srv.Shutdown(ctx); err != nil {
			slog.Error("server shutdown failed", "err", err)
		}
	}
	if a.pool != nil {
		a.pool.CloseAll()
	}
}
