# Managi v3 Dockerfile - 多阶段构建
# 设计见 design-v5.md §8.1
# 目标镜像体积 <25MB

# ===== 阶段1: 前端构建 =====
# Node 22 与 CI（.github/workflows/*.yml 的 node-version: '22'）对齐：
# Node 20 已于 2026-04 EOL，两边版本不一致时「CI 过了镜像构建挂了」很难联想。
FROM node:22-alpine AS frontend
WORKDIR /fe
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ===== 阶段2: 后端构建 =====
FROM golang:1.25-alpine AS backend
WORKDIR /be
COPY backend/go.mod backend/go.sum* ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /managi ./cmd/managi

# ===== 阶段3: 运行 =====
# 修复 B37：Alpine 3.19 已于 2025-11 EOL，升级到 3.21（持续维护至 2027-11）
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=backend /managi /app/managi
COPY --from=frontend /fe/dist/index.html /app/index.html
WORKDIR /app
# D1：确保 nobody 用户可读写 /app 目录（默认 root 所有）
RUN chown -R nobody:nobody /app
EXPOSE 18001
# 注意：HEALTHCHECK 固定探 18001（默认端口）。改了 MANAGI_PORT 需一并覆盖 healthcheck。
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://localhost:18001/health || exit 1
USER nobody
ENTRYPOINT ["/app/managi"]
# 不要在这里写 CMD ["-port", "..."]：命令行 flag 的优先级高于环境变量，
# 写死会让用户的 MANAGI_PORT 被静默忽略（表现为「改了环境变量端口却没变」）。
# 端口一律由 MANAGI_PORT 决定，需要追加参数时用 compose 的 command: 覆盖。
