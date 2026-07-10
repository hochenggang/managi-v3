module managi

go 1.25.0

toolchain go1.26.5

require (
	github.com/getlantern/systray v1.2.2
	github.com/gorilla/websocket v1.5.3
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c
	github.com/pkg/sftp v1.13.6
	github.com/stretchr/testify v1.9.0
	golang.org/x/crypto v0.52.0
	golang.org/x/sync v0.22.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/getlantern/context v0.0.0-20190109183933-c447772a6520 // indirect
	github.com/getlantern/errors v0.0.0-20190325191628-abdb3e3e36f7 // indirect
	github.com/getlantern/golog v0.0.0-20190830074920-4ef2e798c2d7 // indirect
	github.com/getlantern/hex v0.0.0-20190417191902-c6586a6fe0b7 // indirect
	github.com/getlantern/hidden v0.0.0-20190325191715-f02dbb02be55 // indirect
	github.com/getlantern/ops v0.0.0-20190325191751-d70cb0d6f85f // indirect
	github.com/go-stack/stack v1.8.0 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/oxtoacart/bpool v0.0.0-20190530202638-03653db5a59c // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// TODO(design-v3 §4): 依赖将在实现阶段通过 go mod tidy 补全
// Web 框架: net/http (标准库) + gorilla/websocket
// SSH: golang.org/x/crypto/ssh
// SFTP: github.com/pkg/sftp
