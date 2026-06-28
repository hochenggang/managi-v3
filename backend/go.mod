module managi

go 1.22

require (
	github.com/gorilla/websocket v1.5.3
	github.com/pkg/sftp v1.13.6
	github.com/stretchr/testify v1.9.0
	golang.org/x/crypto v0.24.0
	golang.org/x/sync v0.7.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/sys v0.21.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// TODO(design-v3 §4): 依赖将在实现阶段通过 go mod tidy 补全
// Web 框架: net/http (标准库) + gorilla/websocket
// SSH: golang.org/x/crypto/ssh
// SFTP: github.com/pkg/sftp
