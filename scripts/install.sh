#!/usr/bin/env bash
#═══════════════════════════════════════════════════════════════════════════════
#                     Phantom Server 一键管理脚本 v3.1
#═══════════════════════════════════════════════════════════════════════════════

#───────────────────────────────────────────────────────────────────────────────
# 配置
#───────────────────────────────────────────────────────────────────────────────
GITHUB_REPO="mrcgq/g2"
INSTALL_DIR="/opt/phantom"
CONFIG_DIR="/etc/phantom"
BINARY_NAME="phantom-server"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
SERVICE_NAME="phantom"

#───────────────────────────────────────────────────────────────────────────────
# 颜色
#───────────────────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
WHITE='\033[1;37m'
NC='\033[0m'

#───────────────────────────────────────────────────────────────────────────────
# 日志
#───────────────────────────────────────────────────────────────────────────────
info()  { echo -e "${GREEN}[✓]${NC} $1"; }
warn()  { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[✗]${NC} $1"; }
step()  { echo -e "${BLUE}[→]${NC} $1"; }
line()  { echo -e "${CYAN}────────────────────────────────────────────────────────────────${NC}"; }

#───────────────────────────────────────────────────────────────────────────────
# 工具函数
#───────────────────────────────────────────────────────────────────────────────
check_root() {
    [[ $EUID -ne 0 ]] && { error "请使用 root 权限运行"; exit 1; }
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)   ARCH="amd64" ;;
        aarch64|arm64)  ARCH="arm64" ;;
        armv7l|armv7)   ARCH="armv7" ;;
        *)              error "不支持的架构: $(uname -m)"; exit 1 ;;
    esac
}

get_public_ip() {
    local ip
    for url in "https://api.ipify.org" "https://ifconfig.me/ip" "https://icanhazip.com"; do
        ip=$(curl -s --connect-timeout 5 "$url" 2>/dev/null | tr -d '\n\r ')
        [[ "$ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] && { echo "$ip"; return 0; }
    done
    return 1
}

get_latest_version() {
    curl -s "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null | \
        grep '"tag_name"' | head -1 | sed -E 's/.*"v?([^"]+)".*/\1/'
}

get_current_version() {
    [[ -f "${CONFIG_DIR}/.version" ]] && cat "${CONFIG_DIR}/.version" || echo ""
}

is_installed() { [[ -f "${INSTALL_DIR}/${BINARY_NAME}" ]]; }
is_running()   { systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null; }

generate_share_link() {
    local json="{\"v\":\"$1\",\"server\":\"$2\",\"port\":$3,\"psk\":\"$4\"}"
    echo "phantom://$(echo -n "$json" | base64 -w 0 2>/dev/null || echo -n "$json" | base64)"
}

#───────────────────────────────────────────────────────────────────────────────
# 安装
#───────────────────────────────────────────────────────────────────────────────
cmd_install() {
    check_root
    detect_arch
    
    echo ""
    line
    step "开始安装 Phantom Server"
    line
    
    # 检查已安装
    if is_installed; then
        warn "已安装，将覆盖安装"
        systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    fi
    
    # 获取版本
    step "获取最新版本..."
    VERSION=$(get_latest_version)
    [[ -z "$VERSION" ]] && { error "无法获取版本信息"; return 1; }
    info "版本: v${VERSION}"
    
    # 获取 IP
    step "获取服务器 IP..."
    SERVER_IP=$(get_public_ip)
    if [[ -z "$SERVER_IP" ]]; then
        read -rp "请输入服务器 IP: " SERVER_IP
        [[ -z "$SERVER_IP" ]] && { error "IP 不能为空"; return 1; }
    fi
    info "IP: $SERVER_IP"
    
    # 端口
    read -rp "UDP 端口 [54321]: " PORT
    PORT=${PORT:-54321}
    
    # 日志级别
    echo "日志级别: 1=error 2=info 3=debug"
    read -rp "选择 [2]: " LOG_CHOICE
    case "$LOG_CHOICE" in
        1) LOG_LEVEL="error" ;;
        3) LOG_LEVEL="debug" ;;
        *) LOG_LEVEL="info" ;;
    esac
    
    # 下载
    step "下载程序..."
    mkdir -p "$INSTALL_DIR" "$CONFIG_DIR"
    
    DOWNLOAD_URL="https://github.com/${GITHUB_REPO}/releases/download/v${VERSION}/${BINARY_NAME}-linux-${ARCH}.tar.gz"
    TMP_FILE="/tmp/phantom-${VERSION}.tar.gz"
    
    if ! curl -fSL --progress-bar -o "$TMP_FILE" "$DOWNLOAD_URL"; then
        error "下载失败: $DOWNLOAD_URL"
        return 1
    fi
    
    # 解压
    step "解压安装..."
    tar -xzf "$TMP_FILE" -C "$INSTALL_DIR" 2>/dev/null
    rm -f "$TMP_FILE"
    
    # 查找二进制文件（修复：处理 phantom-server-linux-amd64 这种名称）
    if [[ ! -f "${INSTALL_DIR}/${BINARY_NAME}" ]]; then
        local found=$(find "$INSTALL_DIR" -maxdepth 1 -name "${BINARY_NAME}*" -type f ! -name "*.yaml" ! -name "*.md" 2>/dev/null | head -1)
        if [[ -n "$found" ]]; then
            mv "$found" "${INSTALL_DIR}/${BINARY_NAME}"
        else
            error "未找到二进制文件"
            return 1
        fi
    fi
    
    chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
    ln -sf "${INSTALL_DIR}/${BINARY_NAME}" "/usr/local/bin/${BINARY_NAME}"
    
    # 生成 PSK
    step "生成配置..."
    PSK=$("${INSTALL_DIR}/${BINARY_NAME}" -gen-psk 2>/dev/null)
    [[ -z "$PSK" ]] && PSK=$(openssl rand -base64 32 2>/dev/null)
    [[ -z "$PSK" ]] && { error "PSK 生成失败"; return 1; }
    
    # 写配置
    cat > "$CONFIG_FILE" << EOF
listen: ":${PORT}"
psk: "${PSK}"
time_window: 30
log_level: "${LOG_LEVEL}"
EOF
    chmod 600 "$CONFIG_FILE"
    echo "$VERSION" > "${CONFIG_DIR}/.version"
    
    # 创建服务
    step "配置服务..."
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
    systemctl enable "$SERVICE_NAME" --quiet 2>/dev/null
    
    # 防火墙
    command -v ufw &>/dev/null && ufw allow "$PORT/udp" &>/dev/null
    command -v firewall-cmd &>/dev/null && {
        firewall-cmd --permanent --add-port="$PORT/udp" &>/dev/null
        firewall-cmd --reload &>/dev/null
    }
    
    # 启动
    step "启动服务..."
    systemctl start "$SERVICE_NAME"
    sleep 2
    
    # 结果
    echo ""
    line
    if is_running; then
        SHARE_LINK=$(generate_share_link "$VERSION" "$SERVER_IP" "$PORT" "$PSK")
        
        echo -e "${GREEN}✓ 安装成功！${NC}"
        echo ""
        echo -e "  服务器: ${CYAN}${SERVER_IP}:${PORT}${NC} (UDP)"
        echo -e "  PSK:    ${YELLOW}${PSK}${NC}"
        echo ""
        echo -e "  分享链接:"
        echo -e "  ${GREEN}${SHARE_LINK}${NC}"
        echo ""
        
        # 保存信息
        cat > "${CONFIG_DIR}/client.txt" << EOF
服务器: ${SERVER_IP}:${PORT}
PSK: ${PSK}
分享链接: ${SHARE_LINK}
EOF
    else
        error "服务启动失败"
        echo "查看日志: journalctl -u ${SERVICE_NAME} -n 20"
        return 1
    fi
    line
}

#───────────────────────────────────────────────────────────────────────────────
# 更新
#───────────────────────────────────────────────────────────────────────────────
cmd_update() {
    check_root
    detect_arch
    
    is_installed || { error "未安装"; return 1; }
    
    step "检查更新..."
    CURRENT=$(get_current_version)
    LATEST=$(get_latest_version)
    [[ -z "$LATEST" ]] && { error "无法获取版本"; return 1; }
    
    echo "  当前: v${CURRENT:-未知}"
    echo "  最新: v${LATEST}"
    
    [[ "$CURRENT" == "$LATEST" ]] && { info "已是最新版本"; return 0; }
    
    step "下载 v${LATEST}..."
    TMP_FILE="/tmp/phantom-${LATEST}.tar.gz"
    DOWNLOAD_URL="https://github.com/${GITHUB_REPO}/releases/download/v${LATEST}/${BINARY_NAME}-linux-${ARCH}.tar.gz"
    
    curl -fSL --progress-bar -o "$TMP_FILE" "$DOWNLOAD_URL" || { error "下载失败"; return 1; }
    
    systemctl stop "$SERVICE_NAME" 2>/dev/null
    
    tar -xzf "$TMP_FILE" -C "$INSTALL_DIR" 2>/dev/null
    rm -f "$TMP_FILE"
    
    # 处理文件名
    local found=$(find "$INSTALL_DIR" -maxdepth 1 -name "${BINARY_NAME}*" -type f ! -name "*.yaml" ! -name "*.md" 2>/dev/null | head -1)
    [[ -n "$found" && "$found" != "${INSTALL_DIR}/${BINARY_NAME}" ]] && mv "$found" "${INSTALL_DIR}/${BINARY_NAME}"
    
    chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
    echo "$LATEST" > "${CONFIG_DIR}/.version"
    
    systemctl start "$SERVICE_NAME"
    sleep 1
    
    is_running && info "更新成功: v${CURRENT} → v${LATEST}" || error "启动失败"
}

#───────────────────────────────────────────────────────────────────────────────
# 卸载
#───────────────────────────────────────────────────────────────────────────────
cmd_uninstall() {
    check_root
    is_installed || { warn "未安装"; return 0; }
    
    echo -e "${RED}确定要卸载吗？${NC}"
    read -rp "[y/N]: " confirm
    [[ ! "$confirm" =~ ^[Yy]$ ]] && return 0
    
    systemctl stop "$SERVICE_NAME" 2>/dev/null
    systemctl disable "$SERVICE_NAME" 2>/dev/null
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload
    
    rm -rf "$INSTALL_DIR"
    rm -f "/usr/local/bin/${BINARY_NAME}"
    
    read -rp "删除配置文件? [y/N]: " del_conf
    [[ "$del_conf" =~ ^[Yy]$ ]] && rm -rf "$CONFIG_DIR"
    
    info "卸载完成"
}

#───────────────────────────────────────────────────────────────────────────────
# 服务控制
#───────────────────────────────────────────────────────────────────────────────
cmd_start()   { check_root; systemctl start "$SERVICE_NAME"   && info "已启动"; }
cmd_stop()    { check_root; systemctl stop "$SERVICE_NAME"    && info "已停止"; }
cmd_restart() { check_root; systemctl restart "$SERVICE_NAME" && info "已重启"; }
cmd_status()  { systemctl status "$SERVICE_NAME" --no-pager; }
cmd_logs()    { journalctl -u "$SERVICE_NAME" -f; }

#───────────────────────────────────────────────────────────────────────────────
# 配置
#───────────────────────────────────────────────────────────────────────────────
cmd_config() {
    [[ -f "$CONFIG_FILE" ]] && cat "$CONFIG_FILE" || error "配置不存在"
}

cmd_link() {
    is_installed || { error "未安装"; return 1; }
    
    local ip=$(get_public_ip)
    local port=$(grep -E '^listen:' "$CONFIG_FILE" 2>/dev/null | sed 's/.*:\([0-9]*\).*/\1/')
    local psk=$(grep -E '^psk:' "$CONFIG_FILE" 2>/dev/null | awk '{print $2}' | tr -d '"')
    local ver=$(get_current_version)
    
    echo ""
    echo -e "  服务器: ${CYAN}${ip}:${port}${NC}"
    echo -e "  PSK:    ${YELLOW}${psk}${NC}"
    echo ""
    echo -e "  分享链接:"
    echo -e "  ${GREEN}$(generate_share_link "$ver" "$ip" "$port" "$psk")${NC}"
    echo ""
}

cmd_newpsk() {
    check_root
    is_installed || { error "未安装"; return 1; }
    
    local new_psk=$("${INSTALL_DIR}/${BINARY_NAME}" -gen-psk 2>/dev/null)
    [[ -z "$new_psk" ]] && new_psk=$(openssl rand -base64 32)
    
    sed -i "s/^psk:.*/psk: \"${new_psk}\"/" "$CONFIG_FILE"
    systemctl restart "$SERVICE_NAME"
    
    info "PSK 已更新"
    echo -e "  新 PSK: ${YELLOW}${new_psk}${NC}"
    cmd_link
}

#───────────────────────────────────────────────────────────────────────────────
# 菜单
#───────────────────────────────────────────────────────────────────────────────
show_menu() {
    while true; do
        clear
        local status_text status_color
        if is_installed; then
            local ver=$(get_current_version)
            if is_running; then
                status_text="● 运行中 (v${ver})"
                status_color="${GREEN}"
            else
                status_text="○ 已停止 (v${ver})"
                status_color="${YELLOW}"
            fi
        else
            status_text="✗ 未安装"
            status_color="${RED}"
        fi
        
        echo -e "${CYAN}"
        cat << 'BANNER'
  ██████╗ ██╗  ██╗ █████╗ ███╗   ██╗████████╗ ██████╗ ███╗   ███╗
  ██╔══██╗██║  ██║██╔══██╗████╗  ██║╚══██╔══╝██╔═══██╗████╗ ████║
  ██████╔╝███████║███████║██╔██╗ ██║   ██║   ██║   ██║██╔████╔██║
  ██╔═══╝ ██╔══██║██╔══██║██║╚██╗██║   ██║   ██║   ██║██║╚██╔╝██║
  ██║     ██║  ██║██║  ██║██║ ╚████║   ██║   ╚██████╔╝██║ ╚═╝ ██║
  ╚═╝     ╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═══╝   ╚═╝    ╚═════╝ ╚═╝     ╚═╝
BANNER
        echo -e "${NC}"
        echo -e "  状态: ${status_color}${status_text}${NC}"
        echo ""
        echo "  1) 安装     2) 更新     3) 卸载"
        echo "  4) 启动     5) 停止     6) 重启     7) 状态"
        echo "  8) 日志     9) 配置    10) 链接    11) 新PSK"
        echo "  0) 退出"
        echo ""
        read -rp "  选择: " choice
        
        case "$choice" in
            1)  cmd_install ;;
            2)  cmd_update ;;
            3)  cmd_uninstall ;;
            4)  cmd_start ;;
            5)  cmd_stop ;;
            6)  cmd_restart ;;
            7)  cmd_status; echo ""; read -rp "按回车继续..." _ ;;
            8)  echo "按 Ctrl+C 退出日志"; sleep 1; cmd_logs ;;
            9)  cmd_config; echo ""; read -rp "按回车继续..." _ ;;
            10) cmd_link; read -rp "按回车继续..." _ ;;
            11) cmd_newpsk; read -rp "按回车继续..." _ ;;
            0)  echo "再见！"; exit 0 ;;
            *)  warn "无效选项" ;;
        esac
    done
}

#───────────────────────────────────────────────────────────────────────────────
# 帮助
#───────────────────────────────────────────────────────────────────────────────
show_help() {
    cat << EOF
Phantom Server 管理脚本 v3.1

用法: $0 [命令]

命令:
  install   安装
  update    更新
  uninstall 卸载
  start     启动
  stop      停止
  restart   重启
  status    状态
  logs      日志
  config    配置
  link      分享链接
  newpsk    新PSK
  menu      菜单 (默认)

示例:
  $0 install
  $0 logs
EOF
}

#───────────────────────────────────────────────────────────────────────────────
# 主入口
#───────────────────────────────────────────────────────────────────────────────
main() {
    case "${1:-menu}" in
        install)   cmd_install ;;
        update)    cmd_update ;;
        uninstall) cmd_uninstall ;;
        start)     cmd_start ;;
        stop)      cmd_stop ;;
        restart)   cmd_restart ;;
        status)    cmd_status ;;
        logs)      cmd_logs ;;
        config)    cmd_config ;;
        link)      cmd_link ;;
        newpsk)    cmd_newpsk ;;
        menu|"")   show_menu ;;
        -h|--help|help) show_help ;;
        *)         error "未知命令: $1"; show_help; exit 1 ;;
    esac
}

main "$@"
