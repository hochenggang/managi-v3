# Managi v3 Makefile
# 统一开发/构建入口，详见 ../design-v3.md 第三章

BACKEND_DIR := backend
FRONTEND_DIR := frontend
DESKTOP_DIR := desktop
GO_BIN := $(BACKEND_DIR)/bin/managi
WINDOWS_APP_DIR := $(BACKEND_DIR)/cmd/windows-app

.PHONY: dev-backend dev-frontend build-backend build-frontend build docker build-windows-app clean test test-coverage type-check lint

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
build-windows-app: build-frontend
	cp $(FRONTEND_DIR)/dist/index.html $(WINDOWS_APP_DIR)/index.html
	cp $(DESKTOP_DIR)/icon.ico $(WINDOWS_APP_DIR)/icon.ico
	cd $(WINDOWS_APP_DIR) && go run github.com/akavel/rsrc@latest \
		-ico icon.ico -arch amd64 -o rsrc_windows_amd64.syso
	cd $(BACKEND_DIR) && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
		-ldflags="-H=windowsgui -s -w" -o ../$(DESKTOP_DIR)/windows-app.exe ./cmd/windows-app

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
	rm -rf $(BACKEND_DIR)/bin $(FRONTEND_DIR)/dist $(DESKTOP_DIR)/windows-app.exe \
		$(WINDOWS_APP_DIR)/index.html $(WINDOWS_APP_DIR)/icon.ico \
		$(WINDOWS_APP_DIR)/rsrc_windows_amd64.syso
