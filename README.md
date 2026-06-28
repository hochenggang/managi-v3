# Managi v3

轻量级、无代理的 Web 端 SSH 批量管理工具 v3 架构。

## 目录结构

```
managi-v3/
├── backend/      # Golang 后端
├── frontend/     # Vue3 前端(保留 v2 CSS)
├── desktop/      # Tauri 跨平台客户端
├── deploy/       # Docker / docker-compose / install.sh
└── .github/      # CI/CD workflows
```

## 开发命令

```bash
# 后端开发
make dev-backend        # 启动 Go 后端 (端口 18001)

# 前端开发
make dev-frontend       # 启动 Vite dev server

# 构建
make build-backend      # 编译 Go 二进制到 backend/bin/
make build-frontend     # 构建前端到 frontend/dist/

# Docker
make docker             # 构建镜像 managi:v3

# 桌面客户端
make desktop-dev        # Tauri 开发模式
make desktop-build      # Tauri 打包
```

## 设计文档

完整设计目标见根目录 [../design-v3.md](../design-v3.md)。

旧版（v2）代码保留在 `../managi-backend-python/` 与 `../managi-frontend-vue3/` 作为对照基线，未修改。
