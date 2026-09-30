#!/bin/sh
# Managi v3 一键部署脚本（Alpine / Debian / Ubuntu 跳板机）
# 设计见 ../design-v3.md §8.3
#
# 用法:
#   ./install.sh              # 启动交互式菜单
#
# 特性: 强制交互、sudo 权限检查、旧配置检测、BASICAUTH 配置、systemd/OpenRC 服务
# 覆盖安装/升级采用「临时文件 + 原子 rename」替换二进制：无需先停服务
# （直接写运行中的可执行文件会 ETXTBSY），下载失败也不会破坏旧文件。

set -e

INSTALL_DIR="/opt/managi"
CONFIG_DIR="/etc/managi"
SERVICE_USER="managi"
GITHUB_REPO="${MANAGI_REPO:-hochenggang/managi-v3}"
# 可选 SHA256 校验，用户可通过 MANAGI_SHA256 环境变量指定预期值
# 形如：MANAGI_SHA256=abc123... ./install.sh
EXPECTED_SHA256="${MANAGI_SHA256:-}"

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
# 值按行取最后一次出现，并脱掉写配置时加的单引号：
# 引号是给 OpenRC 的 `. config.env` 看的，回到脚本里就该是字面口令。
read_config_value() {
    grep "^$1=" "$CONFIG_DIR/config.env" 2>/dev/null | tail -n1 | cut -d= -f2- | sed "s/^'//; s/'\$//"
}

load_config_env() {
    if [ -f "$CONFIG_DIR/config.env" ]; then
        PORT="$(read_config_value MANAGI_PORT)"
        AUTH_ENABLED="$(read_config_value MANAGI_BASICAUTH_ENABLED)"
        AUTH_USER="$(read_config_value MANAGI_BASICAUTH_USERNAME)"
        AUTH_PASS="$(read_config_value MANAGI_BASICAUTH_PASSWORD)"
    fi
}

# ===== 写入配置 =====
write_config_env() {
    # 绝不再写 admin123 这类固定弱口令：配置文件一旦留在跳板机上就是公开的秘密。
    # 启用认证却没有口令属于安装流程出错，宁可中止也不留下可用的弱凭据；
    # 未启用认证则不写口令，磁盘上不留用不到的凭据。
    if [ "${AUTH_ENABLED:-false}" = "true" ] && [ -z "${AUTH_PASS:-}" ]; then
        error "已选择启用 BASICAUTH 但未取得密码，安装中止（不写入弱默认口令）"
    fi

    mkdir -p "$CONFIG_DIR"
    # 口令用单引号写入：systemd EnvironmentFile 与 OpenRC 的 `. config.env` 都按字面取值，
    # 不加引号则 $ 和 ` 会被 shell 二次展开，含这些字符的口令会变形。
    cat > "$CONFIG_DIR/config.env" <<EOF
MANAGI_HOST=0.0.0.0
MANAGI_PORT=${PORT:-18001}
MANAGI_INDEX_HTML=$INSTALL_DIR/index.html
MANAGI_BASICAUTH_ENABLED=${AUTH_ENABLED:-false}
MANAGI_BASICAUTH_USERNAME='${AUTH_USER:-admin}'
MANAGI_BASICAUTH_PASSWORD='${AUTH_PASS:-}'
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
    if command -v wget >/dev/null 2>&1 && command -v curl >/dev/null 2>&1; then
        info "wget/curl 已就绪，跳过依赖安装"
        return 0
    fi
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

# ===== 下载并原子落盘一个 release 资产 =====
# $1=资产名  $2=目标路径  $3=八进制权限
# 先写目标同目录的临时文件，校验通过后再 mv(rename) 替换：
#   1) 直接 wget -qO 覆盖正在运行的可执行文件会得到 ETXTBSY（旧版升级必失败的原因）；
#      rename 只改目录项，运行中的进程仍持有旧 inode，故无需停服务。
#   2) 临时文件必须与目标同目录（同一文件系统），否则 mv 退化为复制+unlink，又踩回 ETXTBSY。
#   3) 下载或校验半路失败时，线上文件保持原样，不会被半截数据毁掉。
install_artifact() {
    _ia_asset="$1"
    _ia_dst="$2"
    _ia_mode="$3"
    mkdir -p "$(dirname "$_ia_dst")"
    _ia_tmp="$(mktemp "${_ia_dst}.XXXXXX")" || error "无法创建临时文件: ${_ia_dst}.XXXXXX"
    _ia_url="https://github.com/${GITHUB_REPO}/releases/latest/download/${_ia_asset}"
    info "下载 $_ia_asset ..."
    if ! wget -qO "$_ia_tmp" "$_ia_url"; then
        rm -f "$_ia_tmp"
        error "下载失败: $_ia_url"
    fi
    if ! verify_checksum "$_ia_tmp" "$_ia_asset"; then
        rm -f "$_ia_tmp"
        return 1
    fi
    chmod "$_ia_mode" "$_ia_tmp"
    mv -f "$_ia_tmp" "$_ia_dst"
    info "已安装到 $_ia_dst"
}

# ===== 下载二进制 / 前端 =====
download_binary() {
    install_artifact "managi-linux-${ARCH}${VARIANT}" "$INSTALL_DIR/managi" 755
}

download_frontend() {
    install_artifact "index.html" "$INSTALL_DIR/index.html" 644
}

# 校验下载文件 SHA256
# 优先级：MANAGI_SHA256 环境变量 > GitHub Release 中的 <file>.sha256 sidecar > 跳过（告警）
# 返回值：0=校验通过或跳过；1=校验失败（已调用 error，调用方可据此 return）
verify_checksum() {
    _vc_file="$1"
    _vc_asset="$2"
    if [ -n "$EXPECTED_SHA256" ]; then
        # 用户显式指定了预期值
        _vc_actual="$(sha256sum "$_vc_file" | awk '{print $1}')"
        if [ "$_vc_actual" != "$EXPECTED_SHA256" ]; then
            rm -f "$_vc_file"
            error "SHA256 校验失败：expected=$EXPECTED_SHA256 actual=$_vc_actual"
            return 1
        fi
        info "SHA256 校验通过（环境变量）"
        return 0
    fi
    # 尝试下载 sidecar <asset>.sha256
    _vc_sidecar_url="https://github.com/${GITHUB_REPO}/releases/latest/download/${_vc_asset}.sha256"
    if wget -q --spider "$_vc_sidecar_url" 2>/dev/null; then
        _vc_sidecar="$(mktemp)"
        if wget -qO "$_vc_sidecar" "$_vc_sidecar_url" 2>/dev/null; then
            # sidecar 文件格式："<sha256>  <filename>" 或纯哈希
            _vc_expected="$(awk '{print $1}' "$_vc_sidecar" | tr -d '[:space:]')"
            rm -f "$_vc_sidecar"
            if [ -n "$_vc_expected" ]; then
                _vc_actual="$(sha256sum "$_vc_file" | awk '{print $1}')"
                if [ "$_vc_actual" != "$_vc_expected" ]; then
                    rm -f "$_vc_file"
                    error "SHA256 校验失败：expected=$_vc_expected actual=$_vc_actual"
                    return 1
                fi
                info "SHA256 校验通过（sidecar）"
                return 0
            fi
        fi
        rm -f "$_vc_sidecar" 2>/dev/null
    fi
    warn "未提供 SHA256，跳过校验。建议设置 MANAGI_SHA256 环境变量或发布时附带 .sha256 sidecar"
    return 0
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

# ===== 服务单元 =====
# 只负责写 unit 文件；安装与升级共用，升级时仅在缺失时补写，避免覆盖用户手工改过的服务定义
write_service_unit() {
    case "$OS_FAMILY" in
        alpine)
            mkdir -p /etc/init.d
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
            ;;
        debian)
            mkdir -p /etc/systemd/system
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
            ;;
    esac
}

service_unit_exists() {
    case "$OS_FAMILY" in
        alpine) [ -f /etc/init.d/managi ] ;;
        debian) [ -f /etc/systemd/system/managi.service ] ;;
    esac
}

enable_service() {
    case "$OS_FAMILY" in
        alpine) rc-update add managi default 2>/dev/null || true ;;
        debian)
            systemctl daemon-reload
            systemctl enable managi
            ;;
    esac
}

# OpenRC 下服务未运行时 restart 会失败，退化为 start
restart_service() {
    case "$OS_FAMILY" in
        alpine) rc-service managi restart 2>/dev/null || rc-service managi start ;;
        debian) systemctl restart managi ;;
    esac
}

install_service() {
    write_service_unit
    enable_service
    restart_service || warn "服务未能启动，请检查日志"
    info "服务已安装并启动"
}

# ===== 健康检查 =====
# 用私有变量读端口：原先直接覆盖全局 PORT，且 grep 失败时管道退出码来自 cut，
# 导致 `|| echo 18001` 永不生效、探测打到 http://localhost:/health
health_check() {
    info "健康检查..."
    _hc_port="$(read_config_value MANAGI_PORT)"
    _hc_port="${_hc_port:-18001}"
    for _ in 1 2 3 4 5; do
        if curl -sf "http://localhost:${_hc_port}/health" >/dev/null 2>&1; then
            info "服务就绪: http://localhost:${_hc_port}"
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
    AUTH_PASS="${MANAGI_BASICAUTH_PASSWORD:-${AUTH_PASS:-}}"

    if read_yes_no "是否启用 BASICAUTH（HTTP 基本认证）"; then
        AUTH_ENABLED="true"
        AUTH_USER="$(read_value "请输入用户名")"
        # 留空交给服务端会随机生成口令，但每次重启都会变化、等于无法登录，
        # 因此交互安装要求一个固定口令（环境变量已提供时不再追问）。
        if [ -z "$AUTH_PASS" ]; then
            AUTH_PASS="$(read_password "请输入密码")"
        fi
    else
        AUTH_ENABLED="false"
        # 不启用认证就不把用不到的凭据写进磁盘
        AUTH_PASS=""
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
    install_deps
    download_binary
    download_frontend
    # 旧安装可能没有 unit（或被手工删除）：缺失才补写，存在则原样保留
    if ! service_unit_exists; then
        warn "未找到服务定义，补写 unit 并设为开机自启"
        write_service_unit
        enable_service
    fi
    restart_service || warn "服务重启失败，请检查日志"
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
