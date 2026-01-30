#!/usr/bin/env bash
#═══════════════════════════════════════════════════════════════════════════════
#                     Phantom Server 一键管理脚本 v3.0
#═══════════════════════════════════════════════════════════════════════════════
#
# 使用方法:
#   bash <(curl -fsSL https://raw.githubusercontent.com/mrcgq/g2/main/scripts/install.sh)
#
# 或下载后执行:
#   chmod +x install.sh && ./install.sh
#
#═══════════════════════════════════════════════════════════════════════════════

set -e

#───────────────────────────────────────────────────────────────────────────────
# 配置区域
#───────────────────────────────────────────────────────────────────────────────
GITHUB_REPO="mrcgq/g2"
INSTALL_DIR="/opt/phantom"
CONFIG_DIR="/etc/phantom"
BINARY_NAME="phantom-server"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
SERVICE_NAME="phantom"
LOG_FILE="/var/log/phantom.log"

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
BOLD='\033[1m'

#───────────────────────────────────────────────────────────────────────────────
# 日志函数
#───────────────────────────────────────────────────────────────────────────────
log_info()    { echo -e "${GREEN}[✓]${NC} $1"; }
log_warn()    { echo -e "${YELLOW}[!]${NC} $1"; }
log_error()   { echo -e "${RED}[✗]${NC} $1"; }
log_step()    { echo -e "${BLUE}[→]${NC} $1"; }
log_debug()   { [[ "${DEBUG:-0}" == "1" ]] && echo -e "${PURPLE}[D]${NC} $1"; }

print_line()  { echo -e "${CYAN}────────────────────────────────────────────────────────────────${NC}"; }

#───────────────────────────────────────────────────────────────────────────────
# 工具函数
#───────────────────────────────────────────────────────────────────────────────

# 检查 root 权限
check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "请使用 root 权限运行此脚本"
        echo -e "    ${YELLOW}sudo bash $0${NC}"
        exit 1
    fi
}

# 检测系统架构
detect_arch() {
    local arch=$(uname -m)
    case "$arch" in
        x86_64|amd64)   ARCH="amd64" ;;
        aarch64|arm64)  ARCH="arm64" ;;
        armv7l|armv7)   ARCH="armv7" ;;
        armv6l)         ARCH="armv7" ;;  # 兼容
        i386|i686)      log_error "不支持 32 位系统"; exit 1 ;;
        *)              log_error "不支持的架构: $arch"; exit 1 ;;
    esac
    log_debug "检测到架构: $ARCH"
}

# 检测系统类型
detect_os() {
    if [[ -f /etc/os-release ]]; then
        . /etc/os-release
        OS=$ID
        OS_VERSION=$VERSION_ID
    elif [[ -f /etc/redhat-release ]]; then
        OS="centos"
    elif [[ -f /etc/debian_version ]]; then
        OS="debian"
    else
        OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    fi
    log_debug "检测到系统: $OS $OS_VERSION"
}

# 检查依赖
check_dependencies() {
    local deps=("curl" "tar" "openssl")
    local missing=()
    
    for dep in "${deps[@]}"; do
        if ! command -v "$dep" &>/dev/null; then
            missing+=("$dep")
        fi
    done
    
    if [[ ${#missing[@]} -gt 0 ]]; then
        log_step "安装缺失的依赖: ${missing[*]}"
        case "$OS" in
            ubuntu|debian)
                apt-get update -qq && apt-get install -y -qq "${missing[@]}"
                ;;
            centos|rhel|fedora|rocky|almalinux)
                yum install -y -q "${missing[@]}" || dnf install -y -q "${missing[@]}"
                ;;
            alpine)
                apk add --no-cache "${missing[@]}"
                ;;
            *)
                log_error "请手动安装: ${missing[*]}"
                exit 1
                ;;
        esac
    fi
}

# 获取公网 IP
get_public_ip() {
    local ip=""
    local services=(
        "https://api.ipify.org"
        "https://ifconfig.me/ip"
        "https://icanhazip.com"
        "https://ipecho.net/plain"
        "https://api.ip.sb/ip"
    )
    
    for service in "${services[@]}"; do
        ip=$(curl -s --connect-timeout 5 --max-time 10 "$service" 2>/dev/null | tr -d '\n')
        if [[ -n "$ip" && "$ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            echo "$ip"
            return 0
        fi
    done
    
    # 尝试获取 IPv6
    for service in "${services[@]}"; do
        ip=$(curl -6 -s --connect-timeout 5 --max-time 10 "$service" 2>/dev/null | tr -d '\n')
        if [[ -n "$ip" ]]; then
            echo "$ip"
            return 0
        fi
    done
    
    return 1
}

# 获取最新版本
get_latest_version() {
    local api_url="https://api.github.com/repos/${GITHUB_REPO}/releases/latest"
    local version
    
    version=$(curl -s --connect-timeout 10 --max-time 30 "$api_url" 2>/dev/null | \
              grep '"tag_name"' | sed -E 's/.*"tag_name": *"v?([^"]+)".*/\1/')
    
    if [[ -z "$version" ]]; then
        log_error "无法获取最新版本，请检查网络连接"
        log_info "仓库地址: https://github.com/${GITHUB_REPO}"
        return 1
    fi
    
    echo "$version"
}

# 获取当前安装版本
get_current_version() {
    if [[ -f "${CONFIG_DIR}/.version" ]]; then
        cat "${CONFIG_DIR}/.version"
    else
        echo "未安装"
    fi
}

# 生成 PSK
generate_psk() {
    openssl rand -base64 32 2>/dev/null || \
    head -c 32 /dev/urandom | base64 2>/dev/null || \
    cat /proc/sys/kernel/random/uuid | md5sum | head -c 32 | base64
}

# 验证端口
validate_port() {
    local port=$1
    if [[ ! "$port" =~ ^[0-9]+$ ]] || [[ "$port" -lt 1 ]] || [[ "$port" -gt 65535 ]]; then
        return 1
    fi
    return 0
}

# 检查端口是否被占用
check_port_available() {
    local port=$1
    if ss -tuln 2>/dev/null | grep -q ":${port} " || \
       netstat -tuln 2>/dev/null | grep -q ":${port} "; then
        return 1
    fi
    return 0
}

# 检查是否已安装
is_installed() {
    [[ -f "${INSTALL_DIR}/${BINARY_NAME}" ]] && [[ -f "$CONFIG_FILE" ]]
}

# 检查服务是否运行
is_running() {
    systemctl is-active --quiet "$SERVICE_NAME" 2>/dev/null
}

# 配置防火墙
configure_firewall() {
    local port=$1
    
    # UFW (Ubuntu/Debian)
    if command -v ufw &>/dev/null; then
        if ufw status | grep -q "Status: active"; then
            ufw allow "$port/udp" &>/dev/null && \
            log_info "UFW: 已开放 UDP 端口 $port"
        fi
    fi
    
    # firewalld (CentOS/RHEL/Fedora)
    if command -v firewall-cmd &>/dev/null; then
        if systemctl is-active --quiet firewalld; then
            firewall-cmd --permanent --add-port="$port/udp" &>/dev/null && \
            firewall-cmd --reload &>/dev/null && \
            log_info "Firewalld: 已开放 UDP 端口 $port"
        fi
    fi
    
    # iptables
    if command -v iptables &>/dev/null; then
        if ! iptables -C INPUT -p udp --dport "$port" -j ACCEPT 2>/dev/null; then
            iptables -I INPUT -p udp --dport "$port" -j ACCEPT 2>/dev/null && \
            log_debug "iptables: 已开放 UDP 端口 $port"
            
            # 保存规则
            if command -v iptables-save &>/dev/null; then
                if [[ -d /etc/iptables ]]; then
                    iptables-save > /etc/iptables/rules.v4 2>/dev/null
                elif [[ -f /etc/sysconfig/iptables ]]; then
                    iptables-save > /etc/sysconfig/iptables 2>/dev/null
                fi
            fi
        fi
    fi
}

# 生成分享链接
generate_share_link() {
    local version=$1
    local server=$2
    local port=$3
    local psk=$4
    
    local json="{\"v\":\"${version}\",\"server\":\"${server}\",\"port\":${port},\"psk\":\"${psk}\"}"
    local encoded=$(echo -n "$json" | base64 -w 0 2>/dev/null || echo -n "$json" | base64 2>/dev/null)
    echo "phantom://${encoded}"
}

#───────────────────────────────────────────────────────────────────────────────
# 主菜单
#───────────────────────────────────────────────────────────────────────────────
show_menu() {
    clear
    echo -e "${CYAN}"
    echo "╔═══════════════════════════════════════════════════════════════════════╗"
    echo "║                                                                       ║"
    echo "║              ██████╗ ██╗  ██╗ █████╗ ███╗   ██╗████████╗ ██████╗ ███╗ ███╗║"
    echo "║              ██╔══██╗██║  ██║██╔══██╗████╗  ██║╚══██╔══╝██╔═══██╗████╗████║║"
    echo "║              ██████╔╝███████║███████║██╔██╗ ██║   ██║   ██║   ██║██╔████╔██║║"
    echo "║              ██╔═══╝ ██╔══██║██╔══██║██║╚██╗██║   ██║   ██║   ██║██║╚██╔╝██║║"
    echo "║              ██║     ██║  ██║██║  ██║██║ ╚████║   ██║   ╚██████╔╝██║ ╚═╝ ██║║"
    echo "║              ╚═╝     ╚═╝  ╚═╝╚═╝  ╚═╝╚═╝  ╚═══╝   ╚═╝    ╚═════╝ ╚═╝     ╚═╝║"
    echo "║                                                                       ║"
    echo "║                    Phantom Server 管理脚本 v3.0                       ║"
    echo "║                     极简 · 无状态 · 抗探测                            ║"
    echo "╠═══════════════════════════════════════════════════════════════════════╣"
    echo -e "║  ${WHITE}当前状态:${NC}${CYAN}                                                          ║"
    
    local current_ver=$(get_current_version)
    if is_installed; then
        if is_running; then
            echo -e "║    ${GREEN}● 已安装 (v${current_ver}) - 运行中${NC}${CYAN}                                  ║"
        else
            echo -e "║    ${YELLOW}○ 已安装 (v${current_ver}) - 已停止${NC}${CYAN}                                  ║"
        fi
    else
        echo -e "║    ${RED}✗ 未安装${NC}${CYAN}                                                       ║"
    fi
    
    echo "╠═══════════════════════════════════════════════════════════════════════╣"
    echo -e "║  ${WHITE}安装与管理${NC}${CYAN}                                                        ║"
    echo "║    1. 安装 Phantom Server                                             ║"
    echo "║    2. 更新到最新版本                                                  ║"
    echo "║    3. 卸载 Phantom Server                                             ║"
    echo "╠═══════════════════════════════════════════════════════════════════════╣"
    echo -e "║  ${WHITE}服务控制${NC}${CYAN}                                                          ║"
    echo "║    4. 启动服务                                                        ║"
    echo "║    5. 停止服务                                                        ║"
    echo "║    6. 重启服务                                                        ║"
    echo "║    7. 查看服务状态                                                    ║"
    echo "║    8. 查看实时日志                                                    ║"
    echo "╠═══════════════════════════════════════════════════════════════════════╣"
    echo -e "║  ${WHITE}配置管理${NC}${CYAN}                                                          ║"
    echo "║    9. 查看当前配置                                                    ║"
    echo "║   10. 修改配置                                                        ║"
    echo "║   11. 重新生成 PSK                                                    ║"
    echo "║   12. 显示分享链接                                                    ║"
    echo "╠═══════════════════════════════════════════════════════════════════════╣"
    echo -e "║  ${WHITE}高级功能${NC}${CYAN}                                                          ║"
    echo "║   13. 系统优化                                                        ║"
    echo "║   14. 安装 BBR                                                        ║"
    echo "║   15. 配置定时任务                                                    ║"
    echo "╠═══════════════════════════════════════════════════════════════════════╣"
    echo "║    0. 退出                                                            ║"
    echo "╚═══════════════════════════════════════════════════════════════════════╝"
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
        0)  exit 0 ;;
        *)  log_error "无效选项"; sleep 1; show_menu ;;
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
    
    print_line
    log_step "开始安装 Phantom Server..."
    print_line
    
    # 检查是否已安装
    if is_installed; then
        log_warn "Phantom Server 已安装"
        read -rp "是否覆盖安装? [y/N]: " confirm
        [[ ! "$confirm" =~ ^[Yy]$ ]] && { show_menu; return; }
        do_stop 2>/dev/null || true
    fi
    
    # 获取版本
    log_step "获取最新版本..."
    VERSION=$(get_latest_version) || { show_menu; return; }
    log_info "最新版本: v${VERSION}"
    
    # 获取服务器 IP
    log_step "获取服务器 IP..."
    SERVER_IP=$(get_public_ip)
    if [[ -z "$SERVER_IP" ]]; then
        read -rp "无法自动获取 IP，请手动输入: " SERVER_IP
        [[ -z "$SERVER_IP" ]] && { log_error "IP 不能为空"; show_menu; return; }
    fi
    log_info "服务器 IP: $SERVER_IP"
    
    # 配置端口
    echo ""
    read -rp "UDP 监听端口 [54321]: " PORT
    PORT=${PORT:-54321}
    
    if ! validate_port "$PORT"; then
        log_error "端口无效: $PORT"
        show_menu
        return
    fi
    
    if ! check_port_available "$PORT"; then
        log_warn "端口 $PORT 已被占用"
        read -rp "是否继续? [y/N]: " confirm
        [[ ! "$confirm" =~ ^[Yy]$ ]] && { show_menu; return; }
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
    print_line
    log_step "下载程序..."
    
    mkdir -p "$INSTALL_DIR" "$CONFIG_DIR"
    
    URL="https://github.com/${GITHUB_REPO}/releases/download/v${VERSION}/${BINARY_NAME}-linux-${ARCH}.tar.gz"
    log_debug "下载地址: $URL"
    
    TMP_FILE=$(mktemp)
    if ! curl -fSL --progress-bar -o "$TMP_FILE" "$URL"; then
        rm -f "$TMP_FILE"
        log_error "下载失败，请检查网络连接"
        show_menu
        return
    fi
    
    # 解压
    log_step "解压安装..."
    tar -xzf "$TMP_FILE" -C "$INSTALL_DIR"
    rm -f "$TMP_FILE"
    
    # 处理可能的目录结构
    if [[ -d "${INSTALL_DIR}/package" ]]; then
        mv "${INSTALL_DIR}/package/"* "${INSTALL_DIR}/" 2>/dev/null || true
        rmdir "${INSTALL_DIR}/package" 2>/dev/null || true
    fi
    
    chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
    ln -sf "${INSTALL_DIR}/${BINARY_NAME}" "/usr/local/bin/${BINARY_NAME}"
    
    # 生成配置
    log_step "生成配置..."
    PSK=$(generate_psk)
    
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
Description=Phantom Server - Minimalist UDP Proxy
Documentation=https://github.com/${GITHUB_REPO}
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=${INSTALL_DIR}/${BINARY_NAME} -c ${CONFIG_FILE}
Restart=always
RestartSec=3
LimitNOFILE=1048576
LimitNPROC=512000
StandardOutput=journal
StandardError=journal
SyslogIdentifier=phantom

# 安全加固
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=${CONFIG_DIR}

[Install]
WantedBy=multi-user.target
EOF
    
    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME" --quiet
    
    # 配置防火墙
    log_step "配置防火墙..."
    configure_firewall "$PORT"
    
    # 启动服务
    log_step "启动服务..."
    systemctl start "$SERVICE_NAME"
    
    sleep 2
    
    # 显示结果
    print_line
    if is_running; then
        SHARE_LINK=$(generate_share_link "$VERSION" "$SERVER_IP" "$PORT" "$PSK")
        
        echo ""
        echo -e "${GREEN}╔═══════════════════════════════════════════════════════════════════════╗${NC}"
        echo -e "${GREEN}║                        安装成功！                                     ║${NC}"
        echo -e "${GREEN}╚═══════════════════════════════════════════════════════════════════════╝${NC}"
        echo ""
        echo -e "  ${WHITE}服务器信息${NC}"
        echo -e "  ─────────────────────────────────────────────"
        echo -e "  版本:     ${CYAN}v${VERSION}${NC}"
        echo -e "  地址:     ${CYAN}${SERVER_IP}:${PORT}${NC} (UDP)"
        echo -e "  日志级别: ${CYAN}${LOG_LEVEL}${NC}"
        echo ""
        echo -e "  ${WHITE}认证信息${NC}"
        echo -e "  ─────────────────────────────────────────────"
        echo -e "  PSK: ${YELLOW}${PSK}${NC}"
        echo ""
        echo -e "  ${WHITE}分享链接${NC}"
        echo -e "  ─────────────────────────────────────────────"
        echo -e "  ${GREEN}${SHARE_LINK}${NC}"
        echo ""
        echo -e "  ${WHITE}管理命令${NC}"
        echo -e "  ─────────────────────────────────────────────"
        echo -e "  启动: ${CYAN}systemctl start ${SERVICE_NAME}${NC}"
        echo -e "  停止: ${CYAN}systemctl stop ${SERVICE_NAME}${NC}"
        echo -e "  重启: ${CYAN}systemctl restart ${SERVICE_NAME}${NC}"
        echo -e "  日志: ${CYAN}journalctl -u ${SERVICE_NAME} -f${NC}"
        echo ""
        print_line
        
        # 保存信息到文件
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
        log_error "服务启动失败"
        echo ""
        echo "请检查日志:"
        echo "  journalctl -u ${SERVICE_NAME} -n 50 --no-pager"
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
        log_error "Phantom Server 未安装"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    print_line
    log_step "检查更新..."
    
    CURRENT=$(get_current_version)
    LATEST=$(get_latest_version) || { show_menu; return; }
    
    echo ""
    echo -e "  当前版本: ${YELLOW}v${CURRENT}${NC}"
    echo -e "  最新版本: ${GREEN}v${LATEST}${NC}"
    echo ""
    
    if [[ "$CURRENT" == "$LATEST" ]]; then
        log_info "已是最新版本"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    read -rp "是否更新到 v${LATEST}? [Y/n]: " confirm
    [[ "$confirm" =~ ^[Nn]$ ]] && { show_menu; return; }
    
    log_step "下载新版本..."
    
    URL="https://github.com/${GITHUB_REPO}/releases/download/v${LATEST}/${BINARY_NAME}-linux-${ARCH}.tar.gz"
    TMP_FILE=$(mktemp)
    
    if ! curl -fSL --progress-bar -o "$TMP_FILE" "$URL"; then
        rm -f "$TMP_FILE"
        log_error "下载失败"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    log_step "停止服务..."
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    
    log_step "安装新版本..."
    
    # 备份旧版本
    [[ -f "${INSTALL_DIR}/${BINARY_NAME}" ]] && \
        mv "${INSTALL_DIR}/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}.bak"
    
    tar -xzf "$TMP_FILE" -C "$INSTALL_DIR"
    rm -f "$TMP_FILE"
    
    if [[ -d "${INSTALL_DIR}/package" ]]; then
        mv "${INSTALL_DIR}/package/${BINARY_NAME}" "${INSTALL_DIR}/" 2>/dev/null || true
        rm -rf "${INSTALL_DIR}/package"
    fi
    
    chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
    echo "$LATEST" > "${CONFIG_DIR}/.version"
    
    # 删除备份
    rm -f "${INSTALL_DIR}/${BINARY_NAME}.bak"
    
    log_step "启动服务..."
    systemctl start "$SERVICE_NAME"
    
    sleep 2
    
    if is_running; then
        log_info "更新成功: v${CURRENT} -> v${LATEST}"
    else
        log_error "服务启动失败，正在回滚..."
        if [[ -f "${INSTALL_DIR}/${BINARY_NAME}.bak" ]]; then
            mv "${INSTALL_DIR}/${BINARY_NAME}.bak" "${INSTALL_DIR}/${BINARY_NAME}"
            echo "$CURRENT" > "${CONFIG_DIR}/.version"
            systemctl start "$SERVICE_NAME"
        fi
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
    
    print_line
    echo -e "${RED}警告: 此操作将完全卸载 Phantom Server${NC}"
    echo ""
    read -rp "确定要卸载吗? [y/N]: " confirm
    [[ ! "$confirm" =~ ^[Yy]$ ]] && { show_menu; return; }
    
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
    
    print_line
    log_info "卸载完成"
    
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 服务控制
#───────────────────────────────────────────────────────────────────────────────
do_start() {
    check_root
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
            log_error "启动失败"
        fi
    fi
    read -rp "按回车键返回菜单..." _
    show_menu
}

do_stop() {
    check_root
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
    if ! is_installed; then
        log_error "Phantom Server 未安装"
    else
        systemctl restart "$SERVICE_NAME"
        sleep 1
        if is_running; then
            log_info "服务已重启"
        else
            log_error "重启失败"
        fi
    fi
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 状态查看
#───────────────────────────────────────────────────────────────────────────────
do_status() {
    print_line
    echo -e "${WHITE}Phantom Server 状态${NC}"
    print_line
    
    if is_installed; then
        local version=$(get_current_version)
        echo -e "安装状态: ${GREEN}已安装${NC}"
        echo -e "版本:     ${CYAN}v${version}${NC}"
        echo -e "安装目录: ${CYAN}${INSTALL_DIR}${NC}"
        echo -e "配置文件: ${CYAN}${CONFIG_FILE}${NC}"
        echo ""
        
        if is_running; then
            echo -e "运行状态: ${GREEN}● 运行中${NC}"
            
            local pid=$(systemctl show -p MainPID --value "$SERVICE_NAME")
            local uptime=$(ps -o etime= -p "$pid" 2>/dev/null | tr -d ' ')
            local mem=$(ps -o rss= -p "$pid" 2>/dev/null | awk '{printf "%.1f MB", $1/1024}')
            
            echo -e "进程 PID: ${CYAN}${pid}${NC}"
            echo -e "运行时间: ${CYAN}${uptime:-未知}${NC}"
            echo -e "内存占用: ${CYAN}${mem:-未知}${NC}"
            
            # 读取配置显示端口
            if [[ -f "$CONFIG_FILE" ]]; then
                local port=$(grep -E '^listen:' "$CONFIG_FILE" | sed 's/.*:\([0-9]*\).*/\1/')
                echo -e "监听端口: ${CYAN}${port:-未知}${NC} (UDP)"
            fi
        else
            echo -e "运行状态: ${RED}○ 已停止${NC}"
        fi
    else
        echo -e "安装状态: ${RED}未安装${NC}"
    fi
    
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
    echo "日志查看选项:"
    echo "  1. 实时日志 (Ctrl+C 退出)"
    echo "  2. 最近 50 条日志"
    echo "  3. 最近 200 条日志"
    echo "  4. 今日日志"
    echo "  5. 错误日志"
    echo "  0. 返回"
    echo ""
    read -rp "选择 [1]: " choice
    
    case "$choice" in
        2) journalctl -u "$SERVICE_NAME" -n 50 --no-pager ;;
        3) journalctl -u "$SERVICE_NAME" -n 200 --no-pager ;;
        4) journalctl -u "$SERVICE_NAME" --since today --no-pager ;;
        5) journalctl -u "$SERVICE_NAME" -p err --no-pager ;;
        0) show_menu; return ;;
        *) journalctl -u "$SERVICE_NAME" -f ;;
    esac
    
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 查看配置
#───────────────────────────────────────────────────────────────────────────────
do_show_config() {
    if ! is_installed; then
        log_error "Phantom Server 未安装"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    print_line
    echo -e "${WHITE}当前配置${NC}"
    print_line
    
    if [[ -f "$CONFIG_FILE" ]]; then
        # 解析并显示配置
        local listen=$(grep -E '^listen:' "$CONFIG_FILE" | awk '{print $2}' | tr -d '"')
        local psk=$(grep -E '^psk:' "$CONFIG_FILE" | awk '{print $2}' | tr -d '"')
        local time_window=$(grep -E '^time_window:' "$CONFIG_FILE" | awk '{print $2}')
        local log_level=$(grep -E '^log_level:' "$CONFIG_FILE" | awk '{print $2}' | tr -d '"')
        
        echo -e "监听地址:   ${CYAN}${listen}${NC}"
        echo -e "PSK:        ${YELLOW}${psk}${NC}"
        echo -e "时间窗口:   ${CYAN}${time_window} 秒${NC}"
        echo -e "日志级别:   ${CYAN}${log_level}${NC}"
        echo ""
        print_line
        echo -e "${WHITE}原始配置文件:${NC}"
        print_line
        cat "$CONFIG_FILE"
    else
        log_error "配置文件不存在"
    fi
    
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
            local current_port=$(grep -E '^listen:' "$CONFIG_FILE" | sed 's/.*:\([0-9]*\).*/\1/')
            echo ""
            read -rp "新端口 [当前: ${current_port}]: " new_port
            if [[ -n "$new_port" ]] && validate_port "$new_port"; then
                sed -i "s/^listen:.*/listen: \":${new_port}\"/" "$CONFIG_FILE"
                configure_firewall "$new_port"
                log_info "端口已修改为 $new_port"
                systemctl restart "$SERVICE_NAME"
            else
                log_error "端口无效"
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
            systemctl restart "$SERVICE_NAME"
            ;;
        3)
            local current_window=$(grep -E '^time_window:' "$CONFIG_FILE" | awk '{print $2}')
            echo ""
            read -rp "新时间窗口 (1-300秒) [当前: ${current_window}]: " new_window
            if [[ "$new_window" =~ ^[0-9]+$ ]] && [[ "$new_window" -ge 1 ]] && [[ "$new_window" -le 300 ]]; then
                sed -i "s/^time_window:.*/time_window: ${new_window}/" "$CONFIG_FILE"
                log_info "时间窗口已修改为 ${new_window} 秒"
                systemctl restart "$SERVICE_NAME"
            else
                log_error "无效的时间窗口"
            fi
            ;;
        4)
            if command -v nano &>/dev/null; then
                nano "$CONFIG_FILE"
            elif command -v vim &>/dev/null; then
                vim "$CONFIG_FILE"
            elif command -v vi &>/dev/null; then
                vi "$CONFIG_FILE"
            else
                log_error "未找到可用的编辑器"
            fi
            echo ""
            read -rp "是否重启服务以应用更改? [Y/n]: " restart
            [[ ! "$restart" =~ ^[Nn]$ ]] && systemctl restart "$SERVICE_NAME"
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
    
    print_line
    echo -e "${YELLOW}警告: 重新生成 PSK 后，所有客户端需要重新配置${NC}"
    echo ""
    read -rp "确定要重新生成 PSK 吗? [y/N]: " confirm
    
    if [[ "$confirm" =~ ^[Yy]$ ]]; then
        local new_psk=$(generate_psk)
        sed -i "s/^psk:.*/psk: \"${new_psk}\"/" "$CONFIG_FILE"
        
        systemctl restart "$SERVICE_NAME"
        
        log_info "PSK 已更新"
        echo ""
        echo -e "新 PSK: ${YELLOW}${new_psk}${NC}"
        echo ""
        
        # 更新客户端配置文件
        if [[ -f "${CONFIG_DIR}/client.txt" ]]; then
            local server_ip=$(get_public_ip)
            local port=$(grep -E '^listen:' "$CONFIG_FILE" | sed 's/.*:\([0-9]*\).*/\1/')
            local version=$(get_current_version)
            local share_link=$(generate_share_link "$version" "$server_ip" "$port" "$new_psk")
            
            cat > "${CONFIG_DIR}/client.txt" << EOF
# Phantom Server 客户端配置
# 更新时间: $(date '+%Y-%m-%d %H:%M:%S')

服务器: ${server_ip}
端口: ${port}
PSK: ${new_psk}

分享链接:
${share_link}
EOF
            
            echo -e "分享链接: ${GREEN}${share_link}${NC}"
        fi
    fi
    
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
    
    print_line
    echo -e "${WHITE}分享信息${NC}"
    print_line
    
    local server_ip=$(get_public_ip)
    local port=$(grep -E '^listen:' "$CONFIG_FILE" | sed 's/.*:\([0-9]*\).*/\1/')
    local psk=$(grep -E '^psk:' "$CONFIG_FILE" | awk '{print $2}' | tr -d '"')
    local version=$(get_current_version)
    
    echo ""
    echo -e "服务器: ${CYAN}${server_ip}${NC}"
    echo -e "端口:   ${CYAN}${port}${NC}"
    echo -e "PSK:    ${YELLOW}${psk}${NC}"
    echo ""
    
    local share_link=$(generate_share_link "$version" "$server_ip" "$port" "$psk")
    
    print_line
    echo -e "${WHITE}分享链接:${NC}"
    echo ""
    echo -e "${GREEN}${share_link}${NC}"
    echo ""
    print_line
    
    # 生成二维码 (如果有 qrencode)
    if command -v qrencode &>/dev/null; then
        echo ""
        echo -e "${WHITE}二维码:${NC}"
        qrencode -t ANSIUTF8 "$share_link"
    fi
    
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 系统优化
#───────────────────────────────────────────────────────────────────────────────
do_system_optimize() {
    check_root
    
    print_line
    echo -e "${WHITE}系统优化${NC}"
    print_line
    echo ""
    echo "将进行以下优化:"
    echo "  1. 增加文件描述符限制"
    echo "  2. 优化网络参数"
    echo "  3. 优化内存参数"
    echo ""
    read -rp "是否继续? [Y/n]: " confirm
    [[ "$confirm" =~ ^[Nn]$ ]] && { show_menu; return; }
    
    log_step "优化系统参数..."
    
    # 增加文件描述符限制
    cat > /etc/security/limits.d/99-phantom.conf << EOF
* soft nofile 1048576
* hard nofile 1048576
* soft nproc 512000
* hard nproc 512000
root soft nofile 1048576
root hard nofile 1048576
EOF
    
    # 优化网络参数
    cat > /etc/sysctl.d/99-phantom.conf << EOF
# Phantom Server 网络优化
# 生成时间: $(date '+%Y-%m-%d %H:%M:%S')

# 网络核心
net.core.somaxconn = 65535
net.core.netdev_max_backlog = 65535
net.core.rmem_default = 26214400
net.core.wmem_default = 26214400
net.core.rmem_max = 67108864
net.core.wmem_max = 67108864
net.core.optmem_max = 25165824

# UDP 优化
net.ipv4.udp_mem = 65536 131072 262144
net.ipv4.udp_rmem_min = 16384
net.ipv4.udp_wmem_min = 16384

# TCP 优化 (备用)
net.ipv4.tcp_rmem = 4096 87380 67108864
net.ipv4.tcp_wmem = 4096 65536 67108864
net.ipv4.tcp_mem = 65536 131072 262144
net.ipv4.tcp_fastopen = 3
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.tcp_no_metrics_save = 1
net.ipv4.tcp_fin_timeout = 15
net.ipv4.tcp_keepalive_time = 300
net.ipv4.tcp_keepalive_probes = 3
net.ipv4.tcp_keepalive_intvl = 30
net.ipv4.tcp_max_syn_backlog = 65535
net.ipv4.tcp_max_tw_buckets = 65535
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_syncookies = 1
net.ipv4.ip_local_port_range = 1024 65535

# 文件系统
fs.file-max = 1048576
fs.inotify.max_user_instances = 8192
fs.inotify.max_user_watches = 524288

# 内核
kernel.pid_max = 65535
vm.swappiness = 10
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
    
    print_line
    echo -e "${WHITE}BBR 状态${NC}"
    print_line
    
    # 检查当前状态
    local current_cc=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null)
    local available_cc=$(sysctl -n net.ipv4.tcp_available_congestion_control 2>/dev/null)
    
    echo ""
    echo -e "当前拥塞控制: ${CYAN}${current_cc}${NC}"
    echo -e "可用算法:     ${CYAN}${available_cc}${NC}"
    echo ""
    
    if [[ "$current_cc" == "bbr" ]]; then
        log_info "BBR 已启用"
        read -rp "按回车键返回菜单..." _
        show_menu
        return
    fi
    
    if [[ "$available_cc" == *"bbr"* ]]; then
        log_step "启用 BBR..."
        
        cat >> /etc/sysctl.d/99-bbr.conf << EOF
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
EOF
        
        sysctl -p /etc/sysctl.d/99-bbr.conf &>/dev/null
        
        local new_cc=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null)
        if [[ "$new_cc" == "bbr" ]]; then
            log_info "BBR 启用成功"
        else
            log_error "BBR 启用失败"
        fi
    else
        log_warn "当前内核不支持 BBR"
        echo ""
        echo "请升级到 Linux 4.9+ 内核"
        
        local kernel_version=$(uname -r)
        echo -e "当前内核: ${CYAN}${kernel_version}${NC}"
    fi
    
    read -rp "按回车键返回菜单..." _
    show_menu
}

#───────────────────────────────────────────────────────────────────────────────
# 配置定时任务
#───────────────────────────────────────────────────────────────────────────────
do_setup_cron() {
    check_root
    
    print_line
    echo -e "${WHITE}定时任务${NC}"
    print_line
    echo ""
    echo "  1. 添加自动更新检查 (每天凌晨 3 点)"
    echo "  2. 添加自动重启 (每周日凌晨 4 点)"
    echo "  3. 添加日志清理 (每周清理 7 天前的日志)"
    echo "  4. 查看当前定时任务"
    echo "  5. 删除所有 Phantom 定时任务"
    echo "  0. 返回"
    echo ""
    read -rp "选择: " choice
    
    case "$choice" in
        1)
            # 创建更新检查脚本
            cat > /usr/local/bin/phantom-update-check.sh << 'EOF'
#!/bin/bash
CURRENT=$(cat /etc/phantom/.version 2>/dev/null)
LATEST=$(curl -s https://api.github.com/repos/mrcgq/g2/releases/latest | grep '"tag_name"' | sed -E 's/.*"v?([^"]+)".*/\1/')
if [[ -n "$LATEST" && "$CURRENT" != "$LATEST" ]]; then
    echo "[$(date)] 发现新版本: $LATEST (当前: $CURRENT)" >> /var/log/phantom-update.log
fi
EOF
            chmod +x /usr/local/bin/phantom-update-check.sh
            
            (crontab -l 2>/dev/null | grep -v "phantom-update-check"; echo "0 3 * * * /usr/local/bin/phantom-update-check.sh") | crontab -
            log_info "已添加自动更新检查任务"
            ;;
        2)
            (crontab -l 2>/dev/null | grep -v "phantom.*restart"; echo "0 4 * * 0 systemctl restart phantom") | crontab -
            log_info "已添加自动重启任务"
            ;;
        3)
            (crontab -l 2>/dev/null | grep -v "journalctl.*phantom"; echo "0 5 * * 0 journalctl --vacuum-time=7d") | crontab -
            log_info "已添加日志清理任务"
            ;;
        4)
            echo ""
            echo -e "${WHITE}当前定时任务:${NC}"
            crontab -l 2>/dev/null | grep -E "(phantom|journalctl)" || echo "  (无 Phantom 相关任务)"
            ;;
        5)
            crontab -l 2>/dev/null | grep -v -E "(phantom|journalctl.*phantom)" | crontab -
            rm -f /usr/local/bin/phantom-update-check.sh
            log_info "已删除所有 Phantom 定时任务"
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
# 命令行模式
#───────────────────────────────────────────────────────────────────────────────
show_help() {
    echo "Phantom Server 管理脚本"
    echo ""
    echo "用法: bash install.sh [命令]"
    echo ""
    echo "命令:"
    echo "  install     安装 Phantom Server"
    echo "  update      更新到最新版本"
    echo "  uninstall   卸载 Phantom Server"
    echo "  start       启动服务"
    echo "  stop        停止服务"
    echo "  restart     重启服务"
    echo "  status      查看状态"
    echo "  logs        查看日志"
    echo "  config      查看配置"
    echo "  link        显示分享链接"
    echo "  menu        显示交互菜单"
    echo ""
    echo "示例:"
    echo "  bash install.sh install    # 安装"
    echo "  bash install.sh status     # 查看状态"
    echo "  bash install.sh            # 进入菜单"
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
            do_update
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
                local port=$(grep -E '^listen:' "$CONFIG_FILE" | sed 's/.*:\([0-9]*\).*/\1/')
                local psk=$(grep -E '^psk:' "$CONFIG_FILE" | awk '{print $2}' | tr -d '"')
                local version=$(get_current_version)
                generate_share_link "$version" "$server_ip" "$port" "$psk"
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
            show_help
            exit 1
            ;;
    esac
}

main "$@"
