#!/bin/sh
# Managi v3 一键部署脚本（Alpine / Debian / Ubuntu 跳板机）
# 设计见 ../design-v3.md §8.3
#
# 用法:
#   ./install.sh              # 安装/升级
#   ./install.sh uninstall    # 卸载
#   ./install.sh upgrade      # 升级二进制并重启
#
# 特性: 幂等、自动检测 OS、自动安装依赖、systemd/OpenRC 服务、自启动

set -e

INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/managi"
SERVICE_USER="managi"
GITHUB_REPO="${MANAGI_REPO:-hochenggang/managi-v3}"

# ===== 颜色 =====
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
info()  { printf "${GREEN}[INFO]${NC} %s\n" "$1"; }
warn()  { printf "${YELLOW}[WARN]${NC} %s\n" "$1"; }
error() { printf "${RED}[ERROR]${NC} %s\n" "$1"; exit 1; }

# ===== OS 检测 =====
detect_os() {
    if [ ! -f /etc/os-release ]; then
        error "无法检测操作系统：/etc/os-release 不存在"
    fi
    # 注意：. /etc/os-release 会设置 ID/VERSION_ID/PRETTY_NAME 等变量，
    # 但不会覆盖上方已定义的 MANAGI_VER（变量名不同）。
    . /etc/os-release
    OS_ID="$ID"
    case "$ID" in
        alpine)   OS_FAMILY="alpine"; VARIANT="-musl" ;;
        debian|ubuntu) OS_FAMILY="debian"; VARIANT="" ;;
        *) error "不支持的操作系统: $ID（仅支持 alpine/debian/ubuntu）" ;;
    esac
    info "检测到操作系统: $PRETTY_NAME (family=$OS_FAMILY)"
}

# ===== 架构检测 =====
detect_arch() {
    ARCH_RAW="$(uname -m)"
    case "$ARCH_RAW" in
        x86_64|amd64) ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *) error "不支持的架构: $ARCH_RAW" ;;
    esac
    info "检测到架构: $ARCH"
}

# ===== 依赖安装 =====
install_deps() {
    info "安装依赖..."
    case "$OS_FAMILY" in
        alpine)
            apk add --no-cache ca-certificates tzdata wget curl
            ;;
        debian)
            apt-get update -y
            apt-get install -y ca-certificates tzdata wget curl
            ;;
    esac
}

# ===== 下载二进制 =====
download_binary() {
    BINARY_NAME="managi-linux-${ARCH}${VARIANT}"
    info "下载 managi 二进制 (file=$BINARY_NAME)..."
    URL="https://github.com/${GITHUB_REPO}/releases/latest/download/${BINARY_NAME}"
    wget -qO "$INSTALL_DIR/managi" "$URL" || error "下载失败: $URL"
    chmod +x "$INSTALL_DIR/managi"
    info "二进制已安装到 $INSTALL_DIR/managi"
}

# ===== 下载前端 =====
download_frontend() {
    mkdir -p "$CONFIG_DIR"
    info "下载前端 index.html..."
    URL="https://github.com/${GITHUB_REPO}/releases/latest/download/index.html"
    wget -qO "$CONFIG_DIR/index.html" "$URL" || error "下载前端失败: $URL"
    info "前端已安装到 $CONFIG_DIR/index.html"
}

# ===== 配置初始化 =====
init_config() {
    mkdir -p "$CONFIG_DIR"
    if [ ! -f "$CONFIG_DIR/config.env" ]; then
        info "初始化配置（交互式）..."
        PORT="${MANAGI_PORT:-18001}"
        AUTH_ENABLED="${MANAGI_BASICAUTH_ENABLED:-false}"
        AUTH_USER="${MANAGI_BASICAUTH_USERNAME:-admin}"
        AUTH_PASS="${MANAGI_BASICAUTH_PASSWORD:-$(head -c 12 /dev/urandom | base64)}"
        cat > "$CONFIG_DIR/config.env" <<EOF
MANAGI_HOST=0.0.0.0
MANAGI_PORT=$PORT
MANAGI_BASICAUTH_ENABLED=$AUTH_ENABLED
MANAGI_BASICAUTH_USERNAME=$AUTH_USER
MANAGI_BASICAUTH_PASSWORD=$AUTH_PASS
MANAGI_SSH_TIMEOUT=15
MANAGI_KEEPALIVE=30
EOF
        chmod 600 "$CONFIG_DIR/config.env"
        info "配置已写入 $CONFIG_DIR/config.env"
    else
        info "配置已存在，保留 $CONFIG_DIR/config.env"
    fi
}

# ===== 创建用户 =====
create_user() {
    if [ "$OS_FAMILY" = "alpine" ]; then
        id "$SERVICE_USER" 2>/dev/null || addgroup -S "$SERVICE_USER" && adduser -S -G "$SERVICE_USER" "$SERVICE_USER"
    else
        id "$SERVICE_USER" 2>/dev/null || useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
    fi
}

# ===== 服务安装 =====
install_service() {
    case "$OS_FAMILY" in
        alpine)
            # OpenRC service
            cat > /etc/init.d/managi <<'EOF'
#!/sbin/openrc-run
name="managi"
description="Managi v3 SSH management"
command="/usr/local/bin/managi"
command_args="-port ${MANAGI_PORT:-18001}"
command_background=true
pidfile="/run/managi.pid"
output_log="/var/log/managi.log"
error_log="/var/log/managi.log"

depend() {
    need net
    after firewall
}
EOF
            chmod +x /etc/init.d/managi
            rc-update add managi default 2>/dev/null || true
            rc-service managi start || true
            ;;
        debian)
            # systemd unit
            cat > /etc/systemd/system/managi.service <<EOF
[Unit]
Description=Managi v3 SSH Management
After=network.target

[Service]
Type=simple
User=$SERVICE_USER
EnvironmentFile=$CONFIG_DIR/config.env
ExecStart=$INSTALL_DIR/managi -port \${MANAGI_PORT}
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
            systemctl daemon-reload
            systemctl enable managi
            systemctl restart managi
            ;;
    esac
    info "服务已安装并启动"
}

# ===== 健康检查 =====
health_check() {
    info "健康检查..."
    PORT="$(grep MANAGI_PORT "$CONFIG_DIR/config.env" 2>/dev/null | cut -d= -f2 || echo 18001)"
    for i in 1 2 3 4 5; do
        if curl -sf "http://localhost:${PORT}/health" >/dev/null 2>&1; then
            info "服务就绪: http://localhost:${PORT}"
            return 0
        fi
        sleep 2
    done
    warn "健康检查未通过，请检查日志: journalctl -u managi 或 /var/log/managi.log"
}

# ===== 安装 =====
install() {
    info "开始安装 Managi v3..."
    detect_os
    detect_arch
    install_deps
    create_user
    download_binary
    init_config
    install_service
    health_check
    info "安装完成。访问 http://<本机IP>:${PORT:-18001}"
}

# ===== 卸载 =====
uninstall() {
    info "卸载 Managi v3..."
    case "$OS_FAMILY" in
        alpine) rc-service managi stop 2>/dev/null || true; rc-update del managi 2>/dev/null || true; rm -f /etc/init.d/managi ;;
        debian) systemctl stop managi 2>/dev/null || true; systemctl disable managi 2>/dev/null || true; rm -f /etc/systemd/system/managi.service; systemctl daemon-reload ;;
    esac
    rm -f "$INSTALL_DIR/managi"
    warn "配置目录 $CONFIG_DIR 已保留，手动删除: rm -rf $CONFIG_DIR"
    info "卸载完成"
}

# ===== 升级 =====
upgrade() {
    info "升级 Managi v3..."
    detect_os
    detect_arch
    download_binary
    download_frontend
    case "$OS_FAMILY" in
        alpine) rc-service managi restart ;;
        debian) systemctl restart managi ;;
    esac
    health_check
}

# ===== 入口 =====
case "${1:-install}" in
    install)   install ;;
    uninstall) detect_os; uninstall ;;
    upgrade)   upgrade ;;
    *) echo "用法: $0 [install|uninstall|upgrade]"; exit 1 ;;
esac
