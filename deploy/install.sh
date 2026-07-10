#!/bin/sh
# Managi v3 一键部署脚本（Alpine / Debian / Ubuntu 跳板机）
# 设计见 ../design-v3.md §8.3
#
# 用法:
#   ./install.sh              # 启动交互式菜单
#
# 特性: 强制交互、sudo 权限检查、旧配置检测、BASICAUTH 配置、systemd/OpenRC 服务

set -e

INSTALL_DIR="/opt/managi"
CONFIG_DIR="/etc/managi"
SERVICE_USER="managi"
GITHUB_REPO="${MANAGI_REPO:-hochenggang/managi-v3}"

# ===== 颜色 =====
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
info()  { printf "${GREEN}[INFO]${NC} %s\n" "$1"; }
warn()  { printf "${YELLOW}[WARN]${NC} %s\n" "$1"; }
error() { printf "${RED}[ERROR]${NC} %s\n" "$1"; exit 1; }

# ===== 交互式输入辅助函数 =====
read_yes_no() {
    printf "%s [y/N]: " "$1"
    read -r _ry_answer
    case "$_ry_answer" in
        [Yy]|[Yy][Ee][Ss]) return 0 ;;
        *) return 1 ;;
    esac
}

read_choice() {
    _rc_prompt="$1"
    while true; do
        printf "%s: " "$_rc_prompt" >&2
        read -r _rc_value
        case "$_rc_value" in
            1|2|3) break ;;
            *) warn "无效选择，请重新输入" ;;
        esac
    done
}

read_value() {
    _rv_prompt="$1"
    _rv_value=""
    while [ -z "$_rv_value" ]; do
        printf "%s: " "$_rv_prompt" >&2
        read -r _rv_value
        if [ -z "$_rv_value" ]; then
            warn "输入不能为空"
        fi
    done
    printf "%s" "$_rv_value"
}

read_password() {
    _rp_prompt="$1"
    _rp_value=""
    _rp_restore() { stty echo 2>/dev/null || true; }
    while [ -z "$_rp_value" ]; do
        printf "%s: " "$_rp_prompt" >&2
        _rp_restore
        trap _rp_restore INT TERM EXIT
        stty -echo 2>/dev/null || true
        read -r _rp_value
        _rp_restore
        trap - INT TERM EXIT
        printf "\n" >&2
        if [ -z "$_rp_value" ]; then
            warn "输入不能为空"
        fi
    done
    printf "%s" "$_rp_value"
}

# ===== 运行环境校验 =====
ensure_tty() {
    if [ ! -t 0 ]; then
        error "本脚本需要在交互式终端中运行"
    fi
}

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        error "请使用 sudo 或以 root 身份运行本脚本"
    fi
}

# ===== 安装状态检测 =====
is_installed() {
    [ -f "$INSTALL_DIR/managi" ] || [ -f "$CONFIG_DIR/config.env" ]
}

# ===== 加载旧配置（不覆盖环境变量） =====
load_config_env() {
    if [ -f "$CONFIG_DIR/config.env" ]; then
        PORT="$(grep '^MANAGI_PORT=' "$CONFIG_DIR/config.env" | cut -d= -f2- | tail -n1)"
        AUTH_ENABLED="$(grep '^MANAGI_BASICAUTH_ENABLED=' "$CONFIG_DIR/config.env" | cut -d= -f2- | tail -n1)"
        AUTH_USER="$(grep '^MANAGI_BASICAUTH_USERNAME=' "$CONFIG_DIR/config.env" | cut -d= -f2- | tail -n1)"
        AUTH_PASS="$(grep '^MANAGI_BASICAUTH_PASSWORD=' "$CONFIG_DIR/config.env" | cut -d= -f2- | tail -n1)"
    fi
}

# ===== 写入配置 =====
write_config_env() {
    mkdir -p "$CONFIG_DIR"
    cat > "$CONFIG_DIR/config.env" <<EOF
MANAGI_HOST=0.0.0.0
MANAGI_PORT=${PORT:-18001}
MANAGI_INDEX_HTML=$INSTALL_DIR/index.html
MANAGI_BASICAUTH_ENABLED=${AUTH_ENABLED:-false}
MANAGI_BASICAUTH_USERNAME=${AUTH_USER:-admin}
MANAGI_BASICAUTH_PASSWORD=${AUTH_PASS:-admin123}
MANAGI_SSH_TIMEOUT=15
MANAGI_KEEPALIVE=30
EOF
    chmod 600 "$CONFIG_DIR/config.env"
    info "配置已写入 $CONFIG_DIR/config.env"
}

# ===== OS 检测 =====
detect_os() {
    if [ ! -f /etc/os-release ]; then
        error "无法检测操作系统：/etc/os-release 不存在"
    fi
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
    mkdir -p "$INSTALL_DIR"
    BINARY_NAME="managi-linux-${ARCH}${VARIANT}"
    info "下载 managi 二进制 (file=$BINARY_NAME)..."
    URL="https://github.com/${GITHUB_REPO}/releases/latest/download/${BINARY_NAME}"
    wget -qO "$INSTALL_DIR/managi" "$URL" || error "下载失败: $URL"
    chmod +x "$INSTALL_DIR/managi"
    info "二进制已安装到 $INSTALL_DIR/managi"
}

# ===== 下载前端 =====
download_frontend() {
    mkdir -p "$INSTALL_DIR"
    info "下载前端 index.html..."
    URL="https://github.com/${GITHUB_REPO}/releases/latest/download/index.html"
    wget -qO "$INSTALL_DIR/index.html" "$URL" || error "下载前端失败: $URL"
    info "前端已安装到 $INSTALL_DIR/index.html"
}

# ===== 创建用户 =====
create_user() {
    if id "$SERVICE_USER" >/dev/null 2>&1; then
        return 0
    fi
    if [ "$OS_FAMILY" = "alpine" ]; then
        addgroup -S "$SERVICE_USER"
        adduser -S -G "$SERVICE_USER" "$SERVICE_USER"
    else
        useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
    fi
    info "服务用户 $SERVICE_USER 已创建"
}

# ===== 服务安装 =====
install_service() {
    case "$OS_FAMILY" in
        alpine)
            cat > /etc/init.d/managi <<EOF
#!/sbin/openrc-run
name="managi"
description="Managi v3 SSH management"
command="$INSTALL_DIR/managi"
command_args="-port \${MANAGI_PORT:-18001}"
command_background=true
pidfile="/run/managi.pid"
output_log="/var/log/managi.log"
error_log="/var/log/managi.log"

depend() {
    need net
    after firewall
}

start_pre() {
    if [ -f "$CONFIG_DIR/config.env" ]; then
        set -a
        . "$CONFIG_DIR/config.env"
        set +a
    fi
}
EOF
            chmod +x /etc/init.d/managi
            rc-update add managi default 2>/dev/null || true
            rc-service managi start || true
            ;;
        debian)
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
    PORT="$(grep MANAGI_PORT "$CONFIG_DIR/config.env" 2>/dev/null | cut -d= -f2- || echo 18001)"
    for i in 1 2 3 4 5; do
        if curl -sf "http://localhost:${PORT}/health" >/dev/null 2>&1; then
            info "服务就绪: http://localhost:${PORT}"
            return 0
        fi
        sleep 2
    done
    warn "健康检查未通过，请检查日志: journalctl -u managi 或 /var/log/managi.log"
}

# ===== 清理旧服务（不删除配置） =====
stop_and_remove_service() {
    case "$OS_FAMILY" in
        alpine)
            rc-service managi stop 2>/dev/null || true
            rc-update del managi 2>/dev/null || true
            rm -f /etc/init.d/managi
            ;;
        debian)
            systemctl stop managi 2>/dev/null || true
            systemctl disable managi 2>/dev/null || true
            rm -f /etc/systemd/system/managi.service
            systemctl daemon-reload
            ;;
    esac
}

# ===== 安装 =====
do_install() {
    info "开始安装 Managi v3..."
    detect_os
    detect_arch

    if is_installed; then
        info "检测到已存在的 Managi 安装/配置"
        if read_yes_no "是否使用旧配置继续"; then
            KEEP_OLD_CONFIG=1
            load_config_env
        else
            KEEP_OLD_CONFIG=0
            info "清理旧配置与服务..."
            stop_and_remove_service
            rm -rf "$CONFIG_DIR"
        fi
    else
        KEEP_OLD_CONFIG=0
    fi

    PORT="${MANAGI_PORT:-${PORT:-18001}}"
    AUTH_ENABLED="${MANAGI_BASICAUTH_ENABLED:-${AUTH_ENABLED:-false}}"
    AUTH_USER="${MANAGI_BASICAUTH_USERNAME:-${AUTH_USER:-admin}}"
    # D3：16 字节 hex（32 字符），纯字母数字，比 base64 更安全且易复制
    AUTH_PASS="${MANAGI_BASICAUTH_PASSWORD:-${AUTH_PASS:-$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')}}"

    if read_yes_no "是否启用 BASICAUTH（HTTP 基本认证）"; then
        AUTH_ENABLED="true"
        AUTH_USER="$(read_value "请输入用户名")"
        AUTH_PASS="$(read_password "请输入密码")"
    else
        AUTH_ENABLED="false"
    fi

    install_deps
    create_user
    download_binary
    download_frontend
    write_config_env
    install_service
    health_check
    info "安装完成。访问 http://<本机IP>:${PORT:-18001}"
}

# ===== 卸载 =====
do_uninstall() {
    detect_os
    if read_yes_no "确定要卸载 Managi 吗？（将删除二进制、前端和服务，但保留配置目录）"; then
        stop_and_remove_service
        rm -f "$INSTALL_DIR/managi" "$INSTALL_DIR/index.html"
        rmdir "$INSTALL_DIR" 2>/dev/null || true
        warn "配置目录 $CONFIG_DIR 已保留，手动删除: rm -rf $CONFIG_DIR"
        info "卸载完成"
    else
        info "已取消卸载"
    fi
}

# ===== 升级 =====
do_upgrade() {
    if ! is_installed; then
        error "尚未检测到 Managi 安装，请先选择“安装”"
    fi
    info "开始升级 Managi v3..."
    detect_os
    detect_arch
    download_binary
    download_frontend
    case "$OS_FAMILY" in
        alpine) rc-service managi restart ;;
        debian) systemctl restart managi ;;
    esac
    health_check
    info "升级完成"
}

# ===== 主菜单 =====
main_menu() {
    while true; do
        echo ""
        info "请选择操作："
        echo "  1) 安装"
        echo "  2) 卸载"
        echo "  3) 升级"
        read_choice "请输入选项 [1-3]"
        case "$_rc_value" in
            1) do_install; break ;;
            2) do_uninstall; break ;;
            3) do_upgrade; break ;;
        esac
    done
}

# ===== 入口 =====
ensure_tty
require_root
main_menu
