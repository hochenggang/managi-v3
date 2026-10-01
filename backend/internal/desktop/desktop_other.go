//go:build !(windows && desktop)

package desktop

import (
	"errors"

	"managi/internal/config"
)

// DefaultEnabled 为 false：非 Windows 桌面构建（服务器形态）默认直接起 HTTP 服务。
const DefaultEnabled = false

// Run 在非桌面构建下不可用：托盘依赖 Windows 与内嵌资源，
// 二者都不在本构建形态内（见 desktop_windows.go 的构建标签）。
func Run(cfg *config.Config) error {
	return errors.New("托盘模式仅 Windows 桌面构建支持：GOOS=windows go build -tags desktop ./cmd/managi")
}
