# Managi v3 Makefile
# 统一开发/构建入口，详见 ../design-v3.md 第三章

BACKEND_DIR := backend
FRONTEND_DIR := frontend
DESKTOP_DIR := desktop
GO_BIN := $(BACKEND_DIR)/bin/managi

.PHONY: dev-backend dev-frontend build-backend build-frontend build docker desktop-dev desktop-build clean test test-coverage type-check lint

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

# ===== 桌面客户端 =====
desktop-dev:
	cd $(DESKTOP_DIR)/src-tauri && cargo tauri dev

desktop-build:
	cd $(DESKTOP_DIR)/src-tauri && cargo tauri build

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
	cd $(BACKEND_DIR) && go vet ./...

# ===== 清理 =====
clean:
	rm -rf $(BACKEND_DIR)/bin $(FRONTEND_DIR)/dist $(DESKTOP_DIR)/src-tauri/target
