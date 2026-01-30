#!/usr/bin/env bash
#═══════════════════════════════════════════════════════════════════════════════
#                     Phantom Server 一键管理脚本 v3.0
#═══════════════════════════════════════════════════════════════════════════════

# 不要使用 set -e，因为会导致菜单意外退出
# set -e

#───────────────────────────────────────────────────────────────────────────────
# 配置区域
#───────────────────────────────────────────────────────────────────────────────
GITHUB_REPO="mrcgq/g2"
INSTALL_DIR="/opt/phantom"
CONFIG_DIR="/etc/phantom"
BINARY_NAME="phantom-server"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
SERVICE_NAME="phantom"

#───────────────────────────────────────────────────────────────────────────────
# 颜色定义
#───────────────────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
CYAN='\033[0;36m'
WHITE='\033[1;37m'
NC='\033[0m'

#───────────────────────────────────────────────────────────────────────────────
# 日志函数
#───────────────────────────────────────────────────────────────────────────────
log_info()    { echo -e "${GREEN}[✓]${NC} $1"; }
log_warn()    { echo -e "${YELLOW}[!]${NC} $1"; }
log_error()   { echo -e "${RED}[✗]${NC} $1"; }
log_step()    { echo -e "${BLUE}[→]${NC} $1"; }

print_line()  { echo -e "${CYAN}────────────────────────────────────────────────────────────────${NC}"; }

#───────────────────────────────────────────────────────────────────────────────
# 工具函数
#───────────────────────────────────────────────────────────────────────────────

check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "请使用 root 权限运行此脚本"
        echo -e "    ${YELLOW}sudo bash $0${NC}"
        exit 1
    fi
}

detect_arch() {
    local arch=$(uname -m)
    case "$arch" in
        x86_64|amd64)   ARCH="amd64" ;;
        aarch64|arm64)  ARCH="arm64" ;;
        armv7l|armv7)   ARCH="armv7" ;;
        armv6l)         ARCH="armv7" ;;
        *)              log_error "不支持的架构: $arch"; exit 1 ;;
    esac
}

detect_os() {
    if [[ -f /etc/os-release ]]; then
        . /etc/os-release
        OS=$ID
    elif [[ -f /etc/redhat-release ]]; then
        OS="centos"
    else
        OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    fi
}

check_dependencies() {
    local deps=("curl" "tar")
    local missing=()
    
    for dep in "${deps[@]}"; do
        if ! command -v "$dep" &>/dev/null; then
            missing+=("$dep")
        fi
    done
    
    if [[ ${#missing[@]} -gt 0 ]]; then
        log_step "安装依赖: ${missing[*]}"
        case "$OS" in
            ubuntu|debian)
                apt-get update -qq && apt-get install -y -qq "${missing[@]}" 2>/dev/null
                ;;
            centos|rhel|fedora|rocky|almalinux)
                yum install -y -q "${missing[@]}" 2>/dev/null || dnf install -y -q "${missing[@]}" 2>/dev/null
                ;;
            alpine)
                apk add --no-cache "${missing[@]}" 2>/dev/null
                ;;
        esac
    fi
}

get_public_ip() {
    local ip=""
    local services=(
        "https://api.ipify.org"
        "https://ifconfig.me/ip"
        "https://icanhazip.com"
        "https://ipecho.net/plain"
    )
    
    for service in "${services[@]}"; do
        ip=$(curl -s --connect-timeout 5 --max-time 10 "$service" 2>/dev/null | tr -d '\n\r ')
        if [[ -n "$ip" && "$ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            echo "$ip"
            return 0
        fi
    done
    return 1
}

get_latest_version() {
    local api_url="https://api.github.com/repos/${GITHUB_REPO}/releases/latest"
    local version
    
    version=$(curl -s --connect-timeout 10 --max-time 30 "$api_url" 2>/dev/null | \
              grep '"tag_name"' | head -1 | sed -E 's/.*"tag_name": *"v?([^"]+)".*/\1/')
    
    if [[ -z "$version" ]]; then
        return 1
    fi
    
    echo "$version"
}

get_current_version() {
    if [[ -f "${CONFIG_DIR}/.version" ]]; then
        cat "${CONFIG_DIR}/.version"
    else
        echo "未安装"
    fi
}

generate_psk() {
    if command -v openssl &>/dev/null; then
        openssl rand -base64 32 2>/dev/null
    else
        head -c 32 /dev/urandom | base64 2>/dev/null
    fi
}

is_installed() {
    [[ -f "${INSTALL_DIR}/${BINARY_NAME}" ]] && [[ -f "$CONFIG_FILE" ]]
}

is_running() {
    systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null
}

configure_firewall() {
    local port=$1
    
    # UFW
    if command -v ufw &>/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw allow "$port/udp" &>/dev/null
    fi
    
    # firewalld
    if command -v firewall-cmd &>/dev/null && systemctl is-active --quiet firewalld 2>/dev/null; then
        firewall-cmd --permanent --add-port="$port/udp" &>/dev/null
        firewall-cmd --reload &>/dev/null
    fi
    
    # iptables
    if command -v iptables &>/dev/null; then
        iptables -C INPUT -p udp --dport "$port" -j ACCEPT 2>/dev/null || \
        iptables -I INPUT -p udp --dport "$port" -j ACCEPT 2>/dev/null
    fi
}

generate_share_link() {
    local version=$1
    local server=$2
    local port=$3
    local psk=$4
    
    local json="{\"v\":\"${version}\",\"server\":\"${server}\",\"port\":${port},\"psk\":\"${psk}\"}"
    local encoded
    encoded=$(echo -n "$json" | base64 -w 0 2>/dev/null || echo -n "$json" | base64 2>/dev/null)
    echo "phantom://${encoded}"
}

#───────────────────────────────────────────────────────────────────────────────
# 主菜单
#───────────────────────────────────────────────────────────────────────────────
show_menu() {
    clear
    local current_ver=$(get_current_version)
    local status_text status_color
    
    if is_installed; then
        if is_running; then
            status_text="● 已安装 (v${current_ver}) - 运行中"
            status_color="${GREEN}"
        else
            status_text="○ 已安装 (v${current_ver}) - 已停止"
            status_color="${YELLOW}"
        fi
    else
        status_text="✗ 未安装"
        status_color="${RED}"
    fi
    
    echo -e "${CYAN}"
    cat << 'EOF'
╔═══════════════════════════════════════════════════════════════════╗
║                                                                   ║
║      ██████╗ ██╗  ██╗ █████╗ ███╗   ██╗████████╗ ██████╗ ███╗███╗ ║
║      ██╔══██╗██║  ██║██╔══██╗████╗  ██║╚══██╔══╝██╔═══██╗████████║║
║      ██████╔╝███████║███████║██╔██╗ ██║   ██║   ██║   ██║██╔██╔██║║
║      ██╔═══╝ ██╔══██║██╔══██║██║╚██╗██║   ██║   ██║   ██║██║╚═╝██║║
║      ██║     ██║  ██║██║  ██║██║ ╚████║   ██║   ╚██████╔╝██║   ██║║
║      ╚═╝     ╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═══╝   ╚═╝    ╚═════╝ ╚═╝   ╚═╝║
║                                                                   ║
║                 Phantom Server 管理脚本 v3.0                      ║
║                  极简 · 无状态 · 抗探测                           ║
╠═══════════════════════════════════════════════════════════════════╣
EOF
    echo -e "║  ${WHITE}当前状态:${NC}${CYAN} ${status_color}${status_text}${NC}${CYAN}                              ║"
    cat << 'EOF'
╠═══════════════════════════════════════════════════════════════════╣
║  安装与管理                                                       ║
║    1. 安装 Phantom Server                                         ║
║    2. 更新到最新版本                                              ║
║    3. 卸载 Phantom Server                                         ║
╠═══════════════════════════════════════════════════════════════════╣
║  服务控制                                                         ║
║    4. 启动服务                                                    ║
║    5. 停止服务                                                    ║
║    6. 重启服务                                                    ║
║    7. 查看服务状态                                                ║
║    8. 查看实时日志                                                ║
╠═══════════════════════════════════════════════════════════════════╣
║  配置管理                                                         ║
║    9. 查看当前配置                                                ║
║   10. 修改配置                                                    ║
║   11. 重新生成 PSK                                                ║
║   12. 显示分享链接                                                ║
╠═══════════════════════════════════════════════════════════════════╣
║  高级功能                                                         ║
║   13. 系统优化                                                    ║
║   14. 安装 BBR                                                    ║
║   15. 配置定时任务                                                ║
╠═══════════════════════════════════════════════════════════════════╣
║    0. 退出                                                        ║
╚═══════════════════════════════════════════════════════════════════╝
EOF
    echo -e "${NC}"
    
    read -rp "请选择 [0-15]: " choice
    
    case "$choice" in
        1)  do_install ;;
        2)  do_update ;;
        3)  do_uninstall ;;
        4)  do_start ;;
        5)  do_stop ;;
        6)  do_restart ;;
        7)  do_status ;;
        8)  do_logs ;;
        9)  do_show_config ;;
        10) do_modify_config ;;
        11) do_regenerate_psk ;;
        12) do_show_share_link ;;
        13) do_system_optimize ;;
        14) do_install_bbr ;;
        15) do_setup_cron ;;
        0)  echo "再见！"; exit 0 ;;
        *)  log_error "无效选项: $choice"; sleep 1; show_menu ;;
    esac
}

#───────────────────────────────────────────────────────────────────────────────
# 安装
#───────────────────────────────────────────────────────────────────────────────
do_install() {
    check_root
    detect_arch
    detect_os
    check_dependencies
    
    echo ""
    print_line
    log_step "开始安装 Phantom Server..."
    print_line
    echo ""
    
    # 检查是否已安装
    if is_installed; then
        log_warn "Phantom Server 已安装"
        read -rp "是否覆盖安装? [y/N]: " confirm
        if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
            read -rp "按回车键返回菜单..." _
            show_menu
            return
        fi
        systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    fi
    
    # 获取版本
    log_step "获取最新版本..."
    VERSION=$(get_latest_version)
    if [[ -z "$VERSION" ]]; then
        log_error "无法获取版本信息，请检查网络连接"
        log_info "仓库地址: https://github.com/${GITHUB_REPO}/releases"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    log_info "最新版本: v${VERSION}"
    
    # 获取服务器 IP
    log_step "获取服务器 IP..."
    SERVER_IP=$(get_public_ip)
    if [[ -z "$SERVER_IP" ]]; then
        echo ""
        read -rp "无法自动获取 IP，请手动输入: " SERVER_IP
        if [[ -z "$SERVER_IP" ]]; then
            log_error "IP 不能为空"
            read -rp "按回车键返回菜单..." _
            show_menu
            return
        fi
    fi
    log_info "服务器 IP: $SERVER_IP"
    
    # 配置端口
    echo ""
    read -rp "UDP 监听端口 [54321]: " PORT
    PORT=${PORT:-54321}
    
    if [[ ! "$PORT" =~ ^[0-9]+$ ]] || [[ "$PORT" -lt 1 ]] || [[ "$PORT" -gt 65535 ]]; then
        log_error "端口无效: $PORT"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    # 日志级别
    echo ""
    echo "日志级别:"
    echo "  1. error  - 仅错误"
    echo "  2. info   - 常规信息 (推荐)"
    echo "  3. debug  - 调试信息"
    read -rp "选择 [2]: " LOG_CHOICE
    case "$LOG_CHOICE" in
        1) LOG_LEVEL="error" ;;
        3) LOG_LEVEL="debug" ;;
        *) LOG_LEVEL="info" ;;
    esac
    
    # 下载
    echo ""
    print_line
    log_step "下载程序..."
    
    mkdir -p "$INSTALL_DIR" "$CONFIG_DIR"
    
    DOWNLOAD_URL="https://github.com/${GITHUB_REPO}/releases/download/v${VERSION}/${BINARY_NAME}-linux-${ARCH}.tar.gz"
    log_info "下载地址: $DOWNLOAD_URL"
    
    TMP_FILE="/tmp/phantom-${VERSION}.tar.gz"
    
    if ! curl -fSL --progress-bar -o "$TMP_FILE" "$DOWNLOAD_URL"; then
        log_error "下载失败！"
        echo ""
        echo "可能的原因:"
        echo "  1. 网络连接问题"
        echo "  2. Release 尚未发布"
        echo "  3. 架构不匹配 (当前: ${ARCH})"
        echo ""
        echo "请访问: https://github.com/${GITHUB_REPO}/releases"
        rm -f "$TMP_FILE"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    # 解压
    log_step "解压安装..."
    
    if ! tar -xzf "$TMP_FILE" -C "$INSTALL_DIR" 2>/dev/null; then
        log_error "解压失败"
        rm -f "$TMP_FILE"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    rm -f "$TMP_FILE"
    
    # 查找二进制文件
    if [[ ! -f "${INSTALL_DIR}/${BINARY_NAME}" ]]; then
        # 可能在子目录中
        local found_binary=$(find "$INSTALL_DIR" -name "$BINARY_NAME" -type f 2>/dev/null | head -1)
        if [[ -n "$found_binary" ]]; then
            mv "$found_binary" "${INSTALL_DIR}/${BINARY_NAME}"
            # 清理可能的子目录
            find "$INSTALL_DIR" -mindepth 1 -type d -exec rm -rf {} + 2>/dev/null || true
        else
            log_error "未找到二进制文件"
            read -rp "按回车键返回菜单..." _
            show_menu
            return
        fi
    fi
    
    chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
    ln -sf "${INSTALL_DIR}/${BINARY_NAME}" "/usr/local/bin/${BINARY_NAME}"
    
    # 生成 PSK
    log_step "生成配置..."
    PSK=$(generate_psk)
    
    if [[ -z "$PSK" ]]; then
        log_error "PSK 生成失败"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    # 写入配置文件
    cat > "$CONFIG_FILE" << EOF
# Phantom Server 配置文件
# 生成时间: $(date '+%Y-%m-%d %H:%M:%S')

# UDP 监听地址
listen: ":${PORT}"

# 预共享密钥 (Base64)
psk: "${PSK}"

# 时间窗口 (秒)
time_window: 30

# 日志级别: debug, info, error
log_level: "${LOG_LEVEL}"
EOF
    
    chmod 600 "$CONFIG_FILE"
    echo "$VERSION" > "${CONFIG_DIR}/.version"
    
    # 创建 systemd 服务
    log_step "配置系统服务..."
    
    cat > "/etc/systemd/system/${SERVICE_NAME}.service" << EOF
[Unit]
Description=Phantom Server - UDP Proxy
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=${INSTALL_DIR}/${BINARY_NAME} -c ${CONFIG_FILE}
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
    
    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME" --quiet 2>/dev/null
    
    # 配置防火墙
    log_step "配置防火墙..."
    configure_firewall "$PORT"
    
    # 启动服务
    log_step "启动服务..."
    systemctl start "$SERVICE_NAME"
    
    sleep 2
    
    # 显示结果
    echo ""
    print_line
    
    if is_running; then
        SHARE_LINK=$(generate_share_link "$VERSION" "$SERVER_IP" "$PORT" "$PSK")
        
        echo -e "${GREEN}"
        echo "╔═══════════════════════════════════════════════════════════════════╗"
        echo "║                        ✓ 安装成功！                               ║"
        echo "╚═══════════════════════════════════════════════════════════════════╝"
        echo -e "${NC}"
        echo ""
        echo -e "  ${WHITE}服务器信息${NC}"
        echo "  ─────────────────────────────────────────────"
        echo -e "  版本:     ${CYAN}v${VERSION}${NC}"
        echo -e "  地址:     ${CYAN}${SERVER_IP}:${PORT}${NC} (UDP)"
        echo -e "  日志级别: ${CYAN}${LOG_LEVEL}${NC}"
        echo ""
        echo -e "  ${WHITE}认证信息${NC}"
        echo "  ─────────────────────────────────────────────"
        echo -e "  PSK: ${YELLOW}${PSK}${NC}"
        echo ""
        echo -e "  ${WHITE}分享链接 (复制到客户端)${NC}"
        echo "  ─────────────────────────────────────────────"
        echo -e "  ${GREEN}${SHARE_LINK}${NC}"
        echo ""
        echo -e "  ${WHITE}管理命令${NC}"
        echo "  ─────────────────────────────────────────────"
        echo -e "  启动: ${CYAN}systemctl start ${SERVICE_NAME}${NC}"
        echo -e "  停止: ${CYAN}systemctl stop ${SERVICE_NAME}${NC}"
        echo -e "  重启: ${CYAN}systemctl restart ${SERVICE_NAME}${NC}"
        echo -e "  日志: ${CYAN}journalctl -u ${SERVICE_NAME} -f${NC}"
        echo ""
        print_line
        
        # 保存客户端信息
        cat > "${CONFIG_DIR}/client.txt" << EOF
# Phantom Server 客户端配置
# 生成时间: $(date '+%Y-%m-%d %H:%M:%S')

服务器: ${SERVER_IP}
端口: ${PORT}
PSK: ${PSK}

分享链接:
${SHARE_LINK}
EOF
        chmod 600 "${CONFIG_DIR}/client.txt"
        log_info "客户端信息已保存到: ${CONFIG_DIR}/client.txt"
    else
        log_error "服务启动失败！"
        echo ""
        echo "请查看日志排查问题:"
        echo "  journalctl -u ${SERVICE_NAME} -n 50 --no-pager"
        echo ""
        echo "常见问题:"
        echo "  1. 端口被占用"
        echo "  2. 配置文件格式错误"
        echo "  3. 权限问题"
    fi
    
    echo ""
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 更新
#───────────────────────────────────────────────────────────────────────────────
do_update() {
    check_root
    detect_arch
    
    if ! is_installed; then
        log_error "Phantom Server 未安装，请先安装"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    echo ""
    print_line
    log_step "检查更新..."
    
    CURRENT=$(get_current_version)
    LATEST=$(get_latest_version)
    
    if [[ -z "$LATEST" ]]; then
        log_error "无法获取最新版本"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    echo ""
    echo -e "  当前版本: ${YELLOW}v${CURRENT}${NC}"
    echo -e "  最新版本: ${GREEN}v${LATEST}${NC}"
    echo ""
    
    if [[ "$CURRENT" == "$LATEST" ]]; then
        log_info "已是最新版本！"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    read -rp "是否更新到 v${LATEST}? [Y/n]: " confirm
    if [[ "$confirm" =~ ^[Nn]$ ]]; then
        show_menu
        return
    fi
    
    log_step "下载新版本..."
    
    DOWNLOAD_URL="https://github.com/${GITHUB_REPO}/releases/download/v${LATEST}/${BINARY_NAME}-linux-${ARCH}.tar.gz"
    TMP_FILE="/tmp/phantom-${LATEST}.tar.gz"
    
    if ! curl -fSL --progress-bar -o "$TMP_FILE" "$DOWNLOAD_URL"; then
        log_error "下载失败"
        rm -f "$TMP_FILE"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    log_step "停止服务..."
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    
    log_step "备份旧版本..."
    cp "${INSTALL_DIR}/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}.bak" 2>/dev/null || true
    
    log_step "安装新版本..."
    tar -xzf "$TMP_FILE" -C "$INSTALL_DIR" 2>/dev/null
    rm -f "$TMP_FILE"
    
    # 查找并移动二进制
    if [[ ! -f "${INSTALL_DIR}/${BINARY_NAME}" ]]; then
        local found=$(find "$INSTALL_DIR" -name "$BINARY_NAME" -type f 2>/dev/null | head -1)
        if [[ -n "$found" ]]; then
            mv "$found" "${INSTALL_DIR}/${BINARY_NAME}"
            find "$INSTALL_DIR" -mindepth 1 -type d -exec rm -rf {} + 2>/dev/null || true
        fi
    fi
    
    chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
    echo "$LATEST" > "${CONFIG_DIR}/.version"
    rm -f "${INSTALL_DIR}/${BINARY_NAME}.bak"
    
    log_step "启动服务..."
    systemctl start "$SERVICE_NAME"
    
    sleep 2
    
    if is_running; then
        log_info "更新成功: v${CURRENT} → v${LATEST}"
    else
        log_error "服务启动失败，请检查日志"
    fi
    
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 卸载
#───────────────────────────────────────────────────────────────────────────────
do_uninstall() {
    check_root
    
    if ! is_installed; then
        log_warn "Phantom Server 未安装"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    echo ""
    print_line
    echo -e "${RED}警告: 此操作将完全卸载 Phantom Server${NC}"
    echo ""
    read -rp "确定要卸载吗? [y/N]: " confirm
    
    if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
        show_menu
        return
    fi
    
    log_step "停止服务..."
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    systemctl disable "$SERVICE_NAME" 2>/dev/null || true
    
    log_step "删除服务..."
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload
    
    log_step "删除程序..."
    rm -rf "$INSTALL_DIR"
    rm -f "/usr/local/bin/${BINARY_NAME}"
    
    echo ""
    read -rp "是否删除配置文件? [y/N]: " del_config
    if [[ "$del_config" =~ ^[Yy]$ ]]; then
        rm -rf "$CONFIG_DIR"
        log_info "配置文件已删除"
    else
        log_info "配置文件已保留: $CONFIG_DIR"
    fi
    
    echo ""
    log_info "卸载完成！"
    
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 服务控制
#───────────────────────────────────────────────────────────────────────────────
do_start() {
    check_root
    echo ""
    if ! is_installed; then
        log_error "Phantom Server 未安装"
    elif is_running; then
        log_warn "服务已在运行中"
    else
        systemctl start "$SERVICE_NAME"
        sleep 1
        if is_running; then
            log_info "服务已启动"
        else
            log_error "启动失败，请查看日志"
        fi
    fi
    read -rp "按回车键返回菜单..." _
    show_menu
}

do_stop() {
    check_root
    echo ""
    if ! is_installed; then
        log_error "Phantom Server 未安装"
    elif ! is_running; then
        log_warn "服务未运行"
    else
        systemctl stop "$SERVICE_NAME"
        log_info "服务已停止"
    fi
    read -rp "按回车键返回菜单..." _
    show_menu
}

do_restart() {
    check_root
    echo ""
    if ! is_installed; then
        log_error "Phantom Server 未安装"
    else
        systemctl restart "$SERVICE_NAME"
        sleep 1
        if is_running; then
            log_info "服务已重启"
        else
            log_error "重启失败，请查看日志"
        fi
    fi
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 状态查看
#───────────────────────────────────────────────────────────────────────────────
do_status() {
    echo ""
    print_line
    echo -e "${WHITE}Phantom Server 状态${NC}"
    print_line
    echo ""
    
    if is_installed; then
        local version=$(get_current_version)
        echo -e "  安装状态: ${GREEN}已安装${NC}"
        echo -e "  版本:     ${CYAN}v${version}${NC}"
        echo -e "  安装目录: ${CYAN}${INSTALL_DIR}${NC}"
        echo -e "  配置文件: ${CYAN}${CONFIG_FILE}${NC}"
        echo ""
        
        if is_running; then
            echo -e "  运行状态: ${GREEN}● 运行中${NC}"
            
            local pid=$(systemctl show -p MainPID --value "$SERVICE_NAME" 2>/dev/null)
            if [[ -n "$pid" && "$pid" != "0" ]]; then
                local uptime=$(ps -o etime= -p "$pid" 2>/dev/null | tr -d ' ')
                local mem=$(ps -o rss= -p "$pid" 2>/dev/null | awk '{printf "%.1f MB", $1/1024}')
                echo -e "  进程 PID: ${CYAN}${pid}${NC}"
                echo -e "  运行时间: ${CYAN}${uptime:-未知}${NC}"
                echo -e "  内存占用: ${CYAN}${mem:-未知}${NC}"
            fi
            
            if [[ -f "$CONFIG_FILE" ]]; then
                local port=$(grep -E '^listen:' "$CONFIG_FILE" 2>/dev/null | sed 's/.*:\([0-9]*\).*/\1/')
                echo -e "  监听端口: ${CYAN}${port:-未知}${NC} (UDP)"
            fi
        else
            echo -e "  运行状态: ${RED}○ 已停止${NC}"
        fi
    else
        echo -e "  安装状态: ${RED}未安装${NC}"
    fi
    
    echo ""
    print_line
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 查看日志
#───────────────────────────────────────────────────────────────────────────────
do_logs() {
    if ! is_installed; then
        log_error "Phantom Server 未安装"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    echo ""
    echo "日志选项:"
    echo "  1. 实时日志 (Ctrl+C 退出)"
    echo "  2. 最近 50 条"
    echo "  3. 最近 200 条"
    echo "  4. 今日日志"
    echo "  0. 返回"
    echo ""
    read -rp "选择 [1]: " choice
    
    case "$choice" in
        2) journalctl -u "$SERVICE_NAME" -n 50 --no-pager ;;
        3) journalctl -u "$SERVICE_NAME" -n 200 --no-pager ;;
        4) journalctl -u "$SERVICE_NAME" --since today --no-pager ;;
        0) show_menu; return ;;
        *) 
            echo ""
            echo "按 Ctrl+C 退出日志查看..."
            sleep 1
            journalctl -u "$SERVICE_NAME" -f
            ;;
    esac
    
    echo ""
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 查看配置
#───────────────────────────────────────────────────────────────────────────────
do_show_config() {
    echo ""
    if ! is_installed; then
        log_error "Phantom Server 未安装"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    print_line
    echo -e "${WHITE}当前配置${NC}"
    print_line
    echo ""
    
    if [[ -f "$CONFIG_FILE" ]]; then
        cat "$CONFIG_FILE"
    else
        log_error "配置文件不存在"
    fi
    
    echo ""
    print_line
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 修改配置
#───────────────────────────────────────────────────────────────────────────────
do_modify_config() {
    check_root
    
    if ! is_installed; then
        log_error "Phantom Server 未安装"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    echo ""
    print_line
    echo -e "${WHITE}修改配置${NC}"
    print_line
    echo ""
    echo "  1. 修改监听端口"
    echo "  2. 修改日志级别"
    echo "  3. 修改时间窗口"
    echo "  4. 手动编辑配置文件"
    echo "  0. 返回"
    echo ""
    read -rp "选择: " choice
    
    case "$choice" in
        1)
            local current_port=$(grep -E '^listen:' "$CONFIG_FILE" 2>/dev/null | sed 's/.*:\([0-9]*\).*/\1/')
            echo ""
            read -rp "新端口 [当前: ${current_port}]: " new_port
            if [[ -n "$new_port" && "$new_port" =~ ^[0-9]+$ ]]; then
                sed -i "s/^listen:.*/listen: \":${new_port}\"/" "$CONFIG_FILE"
                configure_firewall "$new_port"
                log_info "端口已修改为 $new_port"
                systemctl restart "$SERVICE_NAME" && log_info "服务已重启"
            else
                log_warn "未修改"
            fi
            ;;
        2)
            echo ""
            echo "  1. error"
            echo "  2. info"  
            echo "  3. debug"
            read -rp "选择: " level_choice
            case "$level_choice" in
                1) new_level="error" ;;
                3) new_level="debug" ;;
                *) new_level="info" ;;
            esac
            sed -i "s/^log_level:.*/log_level: \"${new_level}\"/" "$CONFIG_FILE"
            log_info "日志级别已修改为 $new_level"
            systemctl restart "$SERVICE_NAME" && log_info "服务已重启"
            ;;
        3)
            local current_window=$(grep -E '^time_window:' "$CONFIG_FILE" 2>/dev/null | awk '{print $2}')
            echo ""
            read -rp "新时间窗口 (1-300秒) [当前: ${current_window}]: " new_window
            if [[ "$new_window" =~ ^[0-9]+$ && "$new_window" -ge 1 && "$new_window" -le 300 ]]; then
                sed -i "s/^time_window:.*/time_window: ${new_window}/" "$CONFIG_FILE"
                log_info "时间窗口已修改为 ${new_window} 秒"
                systemctl restart "$SERVICE_NAME" && log_info "服务已重启"
            else
                log_warn "未修改"
            fi
            ;;
        4)
            local editor=""
            for e in nano vim vi; do
                if command -v "$e" &>/dev/null; then
                    editor="$e"
                    break
                fi
            done
            if [[ -n "$editor" ]]; then
                "$editor" "$CONFIG_FILE"
                echo ""
                read -rp "是否重启服务? [Y/n]: " restart
                [[ ! "$restart" =~ ^[Nn]$ ]] && systemctl restart "$SERVICE_NAME"
            else
                log_error "未找到可用的编辑器"
            fi
            ;;
        0)
            show_menu
            return
            ;;
    esac
    
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 重新生成 PSK
#───────────────────────────────────────────────────────────────────────────────
do_regenerate_psk() {
    check_root
    
    if ! is_installed; then
        log_error "Phantom Server 未安装"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    echo ""
    print_line
    echo -e "${YELLOW}警告: 重新生成 PSK 后，所有客户端需要重新配置！${NC}"
    echo ""
    read -rp "确定要重新生成 PSK 吗? [y/N]: " confirm
    
    if [[ "$confirm" =~ ^[Yy]$ ]]; then
        local new_psk=$(generate_psk)
        sed -i "s/^psk:.*/psk: \"${new_psk}\"/" "$CONFIG_FILE"
        
        systemctl restart "$SERVICE_NAME"
        
        log_info "PSK 已更新"
        echo ""
        echo -e "  新 PSK: ${YELLOW}${new_psk}${NC}"
        
        # 更新分享链接
        local server_ip=$(get_public_ip)
        local port=$(grep -E '^listen:' "$CONFIG_FILE" 2>/dev/null | sed 's/.*:\([0-9]*\).*/\1/')
        local version=$(get_current_version)
        
        if [[ -n "$server_ip" && -n "$port" ]]; then
            local share_link=$(generate_share_link "$version" "$server_ip" "$port" "$new_psk")
            echo ""
            echo -e "  新分享链接:"
            echo -e "  ${GREEN}${share_link}${NC}"
            
            # 更新保存的客户端信息
            cat > "${CONFIG_DIR}/client.txt" << EOF
# Phantom Server 客户端配置
# 更新时间: $(date '+%Y-%m-%d %H:%M:%S')

服务器: ${server_ip}
端口: ${port}
PSK: ${new_psk}

分享链接:
${share_link}
EOF
        fi
    fi
    
    echo ""
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 显示分享链接
#───────────────────────────────────────────────────────────────────────────────
do_show_share_link() {
    if ! is_installed; then
        log_error "Phantom Server 未安装"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    echo ""
    print_line
    echo -e "${WHITE}分享信息${NC}"
    print_line
    echo ""
    
    local server_ip=$(get_public_ip)
    local port=$(grep -E '^listen:' "$CONFIG_FILE" 2>/dev/null | sed 's/.*:\([0-9]*\).*/\1/')
    local psk=$(grep -E '^psk:' "$CONFIG_FILE" 2>/dev/null | awk '{print $2}' | tr -d '"')
    local version=$(get_current_version)
    
    echo -e "  服务器: ${CYAN}${server_ip:-未知}${NC}"
    echo -e "  端口:   ${CYAN}${port:-未知}${NC}"
    echo -e "  PSK:    ${YELLOW}${psk:-未知}${NC}"
    echo ""
    
    if [[ -n "$server_ip" && -n "$port" && -n "$psk" ]]; then
        local share_link=$(generate_share_link "$version" "$server_ip" "$port" "$psk")
        
        print_line
        echo -e "${WHITE}分享链接 (复制到客户端):${NC}"
        echo ""
        echo -e "${GREEN}${share_link}${NC}"
        echo ""
        print_line
        
        # 如果有 qrencode，显示二维码
        if command -v qrencode &>/dev/null; then
            echo ""
            echo -e "${WHITE}二维码:${NC}"
            qrencode -t ANSIUTF8 "$share_link" 2>/dev/null
        fi
    else
        log_error "无法生成分享链接，配置信息不完整"
    fi
    
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 系统优化
#───────────────────────────────────────────────────────────────────────────────
do_system_optimize() {
    check_root
    
    echo ""
    print_line
    echo -e "${WHITE}系统优化${NC}"
    print_line
    echo ""
    echo "将进行以下优化:"
    echo "  1. 增加文件描述符限制"
    echo "  2. 优化网络参数"
    echo "  3. 优化 UDP 缓冲区"
    echo ""
    read -rp "是否继续? [Y/n]: " confirm
    
    if [[ "$confirm" =~ ^[Nn]$ ]]; then
        show_menu
        return
    fi
    
    log_step "优化系统参数..."
    
    # 文件描述符
    cat > /etc/security/limits.d/99-phantom.conf << 'EOF'
* soft nofile 1048576
* hard nofile 1048576
root soft nofile 1048576
root hard nofile 1048576
EOF
    
    # 网络参数
    cat > /etc/sysctl.d/99-phantom.conf << 'EOF'
# Phantom Server 网络优化

# 核心网络
net.core.somaxconn = 65535
net.core.netdev_max_backlog = 65535
net.core.rmem_default = 26214400
net.core.wmem_default = 26214400
net.core.rmem_max = 67108864
net.core.wmem_max = 67108864

# UDP 优化
net.ipv4.udp_mem = 65536 131072 262144
net.ipv4.udp_rmem_min = 16384
net.ipv4.udp_wmem_min = 16384

# TCP 优化
net.ipv4.tcp_rmem = 4096 87380 67108864
net.ipv4.tcp_wmem = 4096 65536 67108864
net.ipv4.tcp_fastopen = 3
net.ipv4.tcp_fin_timeout = 15
net.ipv4.tcp_max_syn_backlog = 65535
net.ipv4.tcp_tw_reuse = 1
net.ipv4.ip_local_port_range = 1024 65535

# 文件系统
fs.file-max = 1048576
EOF
    
    sysctl -p /etc/sysctl.d/99-phantom.conf &>/dev/null
    
    log_info "系统优化完成"
    log_info "部分参数需要重启系统后生效"
    
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 安装 BBR
#───────────────────────────────────────────────────────────────────────────────
do_install_bbr() {
    check_root
    
    echo ""
    print_line
    echo -e "${WHITE}BBR 状态${NC}"
    print_line
    echo ""
    
    local current_cc=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null)
    local available_cc=$(sysctl -n net.ipv4.tcp_available_congestion_control 2>/dev/null)
    
    echo -e "  当前拥塞控制: ${CYAN}${current_cc}${NC}"
    echo -e "  可用算法:     ${CYAN}${available_cc}${NC}"
    echo ""
    
    if [[ "$current_cc" == "bbr" ]]; then
        log_info "BBR 已启用！"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    if [[ "$available_cc" == *"bbr"* ]]; then
        read -rp "是否启用 BBR? [Y/n]: " confirm
        if [[ ! "$confirm" =~ ^[Nn]$ ]]; then
            cat >> /etc/sysctl.d/99-bbr.conf << 'EOF'
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
EOF
            sysctl -p /etc/sysctl.d/99-bbr.conf &>/dev/null
            
            local new_cc=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null)
            if [[ "$new_cc" == "bbr" ]]; then
                log_info "BBR 启用成功！"
            else
                log_error "BBR 启用失败"
            fi
        fi
    else
        log_warn "当前内核不支持 BBR"
        echo ""
        echo "  内核版本: $(uname -r)"
        echo "  BBR 需要 Linux 4.9+ 内核"
    fi
    
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 配置定时任务
#───────────────────────────────────────────────────────────────────────────────
do_setup_cron() {
    check_root
    
    echo ""
    print_line
    echo -e "${WHITE}定时任务${NC}"
    print_line
    echo ""
    echo "  1. 添加自动重启 (每周日凌晨 4 点)"
    echo "  2. 添加日志清理 (每周清理 7 天前日志)"
    echo "  3. 查看当前定时任务"
    echo "  4. 删除 Phantom 定时任务"
    echo "  0. 返回"
    echo ""
    read -rp "选择: " choice
    
    case "$choice" in
        1)
            (crontab -l 2>/dev/null | grep -v "phantom.*restart"; echo "0 4 * * 0 systemctl restart phantom") | crontab -
            log_info "已添加自动重启任务"
            ;;
        2)
            (crontab -l 2>/dev/null | grep -v "journalctl.*vacuum"; echo "0 5 * * 0 journalctl --vacuum-time=7d") | crontab -
            log_info "已添加日志清理任务"
            ;;
        3)
            echo ""
            echo -e "${WHITE}当前定时任务:${NC}"
            crontab -l 2>/dev/null | grep -E "(phantom|journalctl)" || echo "  (无相关任务)"
            ;;
        4)
            crontab -l 2>/dev/null | grep -v -E "(phantom|journalctl.*vacuum)" | crontab - 2>/dev/null
            log_info "已删除 Phantom 定时任务"
            ;;
        0)
            show_menu
            return
            ;;
    esac
    
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 命令行帮助
#───────────────────────────────────────────────────────────────────────────────
show_help() {
    echo "Phantom Server 管理脚本 v3.0"
    echo ""
    echo "用法: bash $0 [命令]"
    echo ""
    echo "命令:"
    echo "  install     安装 Phantom Server"
    echo "  update      更新到最新版本"
    echo "  uninstall   卸载"
    echo "  start       启动服务"
    echo "  stop        停止服务"
    echo "  restart     重启服务"
    echo "  status      查看状态"
    echo "  logs        查看日志"
    echo "  config      查看配置"
    echo "  link        显示分享链接"
    echo "  menu        显示交互菜单 (默认)"
    echo ""
    echo "示例:"
    echo "  bash $0 install"
    echo "  bash $0 status"
    echo "  bash $0"
}

#───────────────────────────────────────────────────────────────────────────────
# 主入口
#───────────────────────────────────────────────────────────────────────────────
main() {
    case "${1:-menu}" in
        install)
            do_install
            ;;
        update)
            check_root
            detect_arch
            echo ""
            CURRENT=$(get_current_version)
            LATEST=$(get_latest_version)
            if [[ -z "$LATEST" ]]; then
                log_error "无法获取版本"
                exit 1
            fi
            echo "当前: v${CURRENT}, 最新: v${LATEST}"
            if [[ "$CURRENT" == "$LATEST" ]]; then
                log_info "已是最新版本"
            else
                do_update
            fi
            ;;
        uninstall)
            do_uninstall
            ;;
        start)
            check_root
            if is_installed; then
                systemctl start "$SERVICE_NAME" && log_info "服务已启动"
            else
                log_error "未安装"
            fi
            ;;
        stop)
            check_root
            systemctl stop "$SERVICE_NAME" 2>/dev/null && log_info "服务已停止"
            ;;
        restart)
            check_root
            systemctl restart "$SERVICE_NAME" && log_info "服务已重启"
            ;;
        status)
            if is_installed; then
                systemctl status "$SERVICE_NAME" --no-pager
            else
                log_error "未安装"
            fi
            ;;
        logs)
            journalctl -u "$SERVICE_NAME" -f
            ;;
        config)
            if [[ -f "$CONFIG_FILE" ]]; then
                cat "$CONFIG_FILE"
            else
                log_error "配置文件不存在"
            fi
            ;;
        link)
            if is_installed; then
                local server_ip=$(get_public_ip)
                local port=$(grep -E '^listen:' "$CONFIG_FILE" 2>/dev/null | sed 's/.*:\([0-9]*\).*/\1/')
                local psk=$(grep -E '^psk:' "$CONFIG_FILE" 2>/dev/null | awk '{print $2}' | tr -d '"')
                local version=$(get_current_version)
                if [[ -n "$server_ip" && -n "$port" && -n "$psk" ]]; then
                    generate_share_link "$version" "$server_ip" "$port" "$psk"
                else
                    log_error "配置信息不完整"
                fi
            else
                log_error "未安装"
            fi
            ;;
        menu|"")
            show_menu
            ;;
        -h|--help|help)
            show_help
            ;;
        *)
            log_error "未知命令: $1"
            echo ""
            show_help
            exit 1
            ;;
    esac
}

# 执行
main "$@"
