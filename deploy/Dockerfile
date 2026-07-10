# Managi v3 Dockerfile - 多阶段构建
# 设计见 ../design-v3.md §8.1
# 目标镜像体积 <25MB

# ===== 阶段1: 前端构建 =====
FROM node:20-alpine AS frontend
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
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=backend /managi /app/managi
COPY --from=frontend /fe/dist/index.html /app/index.html
WORKDIR /app
# D1：确保 nobody 用户可读写 /app 目录（默认 root 所有）
RUN chown -R nobody:nobody /app
EXPOSE 18001
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://localhost:18001/health || exit 1
USER nobody
ENTRYPOINT ["/app/managi"]
CMD ["-port", "18001"]
