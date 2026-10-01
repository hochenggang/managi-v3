# Managi v3 Makefile
# 统一开发/构建入口，详见 design-v5.md 第三章

BACKEND_DIR := backend
FRONTEND_DIR := frontend
DESKTOP_DIR := desktop
GO_BIN := $(BACKEND_DIR)/bin/managi
# 桌面形态的内嵌资源目录（index.html / icon.ico 构建前复制进来，不入库）
DESKTOP_PKG := $(BACKEND_DIR)/internal/desktop
DESKTOP_EXE := $(DESKTOP_DIR)/managi-desktop-windows-amd64.exe

.PHONY: dev-backend dev-frontend build-backend build-frontend build docker build-desktop clean test test-coverage type-check lint

# ===== 开发 =====
dev-backend:
	cd $(BACKEND_DIR) && go run ./cmd/managi -port 18001

dev-frontend:
	cd $(FRONTEND_DIR) && npm run dev

# ===== 构建 =====
build-backend:
	cd $(BACKEND_DIR) && CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/managi ./cmd/managi

build-frontend:
	cd $(FRONTEND_DIR) && npm ci && npm run build

build: build-frontend build-backend

# ===== Docker =====
docker:
	docker build -t managi:v3 -f deploy/Dockerfile .

# ===== Windows 桌面客户端 =====
# 与服务器是同一个 main：cmd/managi 加 -tags desktop 编入内嵌资源，运行带 -tray
build-desktop: build-frontend
	cp $(FRONTEND_DIR)/dist/index.html $(DESKTOP_PKG)/index.html
	cp $(DESKTOP_DIR)/icon.ico $(DESKTOP_PKG)/icon.ico
	cd $(BACKEND_DIR)/cmd/managi && go run github.com/akavel/rsrc@latest \
		-ico ../../internal/desktop/icon.ico -arch amd64 -o rsrc_windows_amd64.syso
	cd $(BACKEND_DIR) && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags desktop \
		-ldflags="-H=windowsgui -s -w" -o ../$(DESKTOP_EXE) ./cmd/managi

# ===== 测试与检查 =====
test:
	cd $(BACKEND_DIR) && go test -cover ./...
	cd $(FRONTEND_DIR) && npm run test

type-check:
	cd $(FRONTEND_DIR) && npm run type-check

test-coverage:
	cd $(BACKEND_DIR) && go test -coverprofile=coverage.out ./...
	cd $(FRONTEND_DIR) && npm run test:coverage

lint:
	cd $(BACKEND_DIR) && golangci-lint run --timeout=3m

# ===== 清理 =====
clean:
	rm -rf $(BACKEND_DIR)/bin $(FRONTEND_DIR)/dist $(DESKTOP_EXE) \
		$(DESKTOP_PKG)/index.html $(DESKTOP_PKG)/icon.ico \
		$(BACKEND_DIR)/cmd/managi/rsrc_windows_amd64.syso
