#!/usr/bin/env bash
#═══════════════════════════════════════════════════════════════════════════════
#                     Phantom Server 管理脚本
#═══════════════════════════════════════════════════════════════════════════════

set -e

# --- 配置部分 ---
GITHUB_REPO="mrcgq/g2"           # 你的仓库地址
# --- 配置结束 ---

INSTALL_DIR="/opt/phantom"
CONFIG_DIR="/etc/phantom"
BINARY_NAME="phantom-server"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
SERVICE_NAME="phantom"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}[✓]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[!]${NC} $1"; }
log_error() { echo -e "${RED}[✗]${NC} $1"; }
log_step()  { echo -e "${BLUE}[→]${NC} $1"; }

check_root() {
    [[ $EUID -ne 0 ]] && { log_error "请使用 root 权限运行"; exit 1; }
}

detect_arch() {
    case $(uname -m) in
        x86_64|amd64)  ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        armv7l)        ARCH="armv7" ;;
        *) log_error "不支持的架构: $(uname -m)"; exit 1 ;;
    esac
}

get_public_ip() {
    curl -s --connect-timeout 5 https://api.ipify.org 2>/dev/null || \
    curl -s --connect-timeout 5 https://ifconfig.me/ip 2>/dev/null || echo ""
}

generate_psk() {
    openssl rand -base64 32
}

# 自动获取最新版本
get_latest_version() {
    local api_url="https://api.github.com/repos/${GITHUB_REPO}/releases/latest"
    local version
    
    version=$(curl -s --connect-timeout 10 "$api_url" | grep '"tag_name"' | sed -E 's/.*"tag_name": *"v?([^"]+)".*/\1/')
    
    if [[ -z "$version" ]]; then
        log_error "无法获取最新版本，请检查网络或仓库地址"
        exit 1
    fi
    
    echo "$version"
}

is_installed() {
    [[ -f "${INSTALL_DIR}/${BINARY_NAME}" ]] && [[ -f "$CONFIG_FILE" ]]
}

is_running() {
    systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null
}

do_install() {
    check_root
    detect_arch
    
    log_step "获取最新版本..."
    VERSION=$(get_latest_version)
    log_info "最新版本: v${VERSION}"
    
    echo -e "${CYAN}"
    echo "╔═══════════════════════════════════════════════════════════════════╗"
    echo "║            Phantom Server v${VERSION} 安装向导                      ║"
    echo "╚═══════════════════════════════════════════════════════════════════╝"
    echo -e "${NC}"
    
    # 获取 IP
    log_step "获取服务器 IP..."
    SERVER_IP=$(get_public_ip)
    [[ -z "$SERVER_IP" ]] && { read -rp "请输入服务器 IP: " SERVER_IP; }
    log_info "服务器 IP: $SERVER_IP"
    
    # 端口
    read -rp "UDP 端口 [54321]: " PORT
    PORT=${PORT:-54321}
    
    # 日志级别
    read -rp "日志级别 (debug/info/error) [info]: " LOG_LEVEL
    LOG_LEVEL=${LOG_LEVEL:-info}
    
    log_step "下载程序..."
    mkdir -p "$INSTALL_DIR" "$CONFIG_DIR"
    
    # 构建下载链接
    URL="https://github.com/${GITHUB_REPO}/releases/download/v${VERSION}/${BINARY_NAME}-linux-${ARCH}.tar.gz"
    
    log_info "下载地址: $URL"
    
    if curl -fSL --progress-bar -o /tmp/phantom.tar.gz "$URL"; then
        tar -xzf /tmp/phantom.tar.gz -C "$INSTALL_DIR"
        rm /tmp/phantom.tar.gz
    else
        log_error "下载失败！请检查网络连接。"
        exit 1
    fi
    
    # 适配解压后的文件结构
    if [[ -d "${INSTALL_DIR}/package" ]]; then
        mv "${INSTALL_DIR}/package/"* "${INSTALL_DIR}/"
        rmdir "${INSTALL_DIR}/package"
    fi

    chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
    ln -sf "${INSTALL_DIR}/${BINARY_NAME}" "/usr/local/bin/${BINARY_NAME}"
    
    log_step "生成配置..."
    PSK=$(generate_psk)
    
    cat > "$CONFIG_FILE" << EOF
listen: ":${PORT}"
psk: "${PSK}"
time_window: 30
log_level: "${LOG_LEVEL}"
EOF
    chmod 600 "$CONFIG_FILE"
    
    # 保存版本信息
    echo "$VERSION" > "${CONFIG_DIR}/.version"
    
    log_step "安装服务..."
    cat > "/etc/systemd/system/${SERVICE_NAME}.service" << EOF
[Unit]
Description=Phantom Server
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/${BINARY_NAME} -c ${CONFIG_FILE}
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
    
    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME" --quiet
    systemctl start "$SERVICE_NAME"
    
    # 防火墙
    command -v ufw &>/dev/null && ufw allow "$PORT/udp" &>/dev/null
    command -v firewall-cmd &>/dev/null && {
        firewall-cmd --permanent --add-port="$PORT/udp" &>/dev/null
        firewall-cmd --reload &>/dev/null
    }
    
    sleep 2
    
    if is_running; then
        LINK="phantom://$(echo -n "{\"v\":\"${VERSION}\",\"server\":\"${SERVER_IP}\",\"port\":${PORT},\"psk\":\"${PSK}\"}" | base64 -w 0)"
        
        echo ""
        echo -e "${GREEN}╔═══════════════════════════════════════════════════════════════════╗${NC}"
        echo -e "${GREEN}║                     安装完成！                                    ║${NC}"
        echo -e "${GREEN}╚═══════════════════════════════════════════════════════════════════╝${NC}"
        echo ""
        echo -e "  版本:   ${CYAN}v${VERSION}${NC}"
        echo -e "  服务器: ${CYAN}${SERVER_IP}:${PORT}${NC}"
        echo -e "  PSK:    ${YELLOW}${PSK}${NC}"
        echo ""
        echo -e "  分享链接:"
        echo -e "  ${GREEN}${LINK}${NC}"
        echo ""
        echo -e "  管理命令:"
        echo -e "    systemctl start/stop/restart ${SERVICE_NAME}"
        echo -e "    journalctl -u ${SERVICE_NAME} -f"
        echo ""
    else
        log_error "启动失败，请检查: journalctl -u ${SERVICE_NAME}"
    fi
}

do_update() {
    check_root
    detect_arch
    
    log_step "检查更新..."
    LATEST=$(get_latest_version)
    CURRENT=$(cat "${CONFIG_DIR}/.version" 2>/dev/null || echo "unknown")
    
    log_info "当前版本: v${CURRENT}"
    log_info "最新版本: v${LATEST}"
    
    if [[ "$CURRENT" == "$LATEST" ]]; then
        log_info "已是最新版本"
        return 0
    fi
    
    read -rp "是否更新到 v${LATEST}? [y/N]: " confirm
    [[ ! "$confirm" =~ ^[Yy]$ ]] && { log_info "取消更新"; return 0; }
    
    log_step "下载新版本..."
    URL="https://github.com/${GITHUB_REPO}/releases/download/v${LATEST}/${BINARY_NAME}-linux-${ARCH}.tar.gz"
    
    if curl -fSL --progress-bar -o /tmp/phantom.tar.gz "$URL"; then
        systemctl stop "$SERVICE_NAME" 2>/dev/null || true
        tar -xzf /tmp/phantom.tar.gz -C "$INSTALL_DIR"
        rm /tmp/phantom.tar.gz
        
        if [[ -d "${INSTALL_DIR}/package" ]]; then
            mv "${INSTALL_DIR}/package/"* "${INSTALL_DIR}/"
            rmdir "${INSTALL_DIR}/package"
        fi
        
        chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
        echo "$LATEST" > "${CONFIG_DIR}/.version"
        
        systemctl start "$SERVICE_NAME"
        log_info "更新完成: v${CURRENT} -> v${LATEST}"
    else
        log_error "下载失败"
        exit 1
    fi
}

do_uninstall() {
    check_root
    
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    systemctl disable "$SERVICE_NAME" 2>/dev/null || true
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload
    rm -rf "$INSTALL_DIR"
    rm -f "/usr/local/bin/${BINARY_NAME}"
    
    read -rp "删除配置文件? [y/N]: " r
    [[ "$r" =~ ^[Yy]$ ]] && rm -rf "$CONFIG_DIR"
    
    log_info "卸载完成"
}

do_status() {
    if is_installed; then
        CURRENT=$(cat "${CONFIG_DIR}/.version" 2>/dev/null || echo "unknown")
        echo -e "安装: ${GREEN}是${NC} (v${CURRENT})"
        if is_running; then
            echo -e "状态: ${GREEN}运行中${NC}"
            PID=$(systemctl show -p MainPID --value "$SERVICE_NAME")
            echo -e "PID:  ${CYAN}${PID}${NC}"
        else
            echo -e "状态: ${RED}已停止${NC}"
        fi
    else
        echo -e "安装: ${RED}否${NC}"
    fi
}

show_help() {
    LATEST=$(get_latest_version 2>/dev/null || echo "unknown")
    echo "Phantom Server (最新: v${LATEST})"
    echo ""
    echo "用法: bash install.sh <命令>"
    echo ""
    echo "命令:"
    echo "  install    安装 (自动获取最新版本)"
    echo "  update     检查并更新到最新版本"
    echo "  uninstall  卸载"
    echo "  status     状态"
    echo "  start      启动"
    echo "  stop       停止"
    echo "  restart    重启"
    echo "  logs       日志"
}

case "${1:-}" in
    install)   do_install ;;
    update)    do_update ;;
    uninstall) do_uninstall ;;
    status)    do_status ;;
    start)     systemctl start "$SERVICE_NAME" && log_info "已启动" ;;
    stop)      systemctl stop "$SERVICE_NAME" && log_info "已停止" ;;
    restart)   systemctl restart "$SERVICE_NAME" && log_info "已重启" ;;
    logs)      journalctl -u "$SERVICE_NAME" -f ;;
    *)         show_help ;;
esac
