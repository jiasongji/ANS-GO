#!/usr/bin/env bash
# =============================================================================
# ANS-GO 一键部署脚本 (install.sh)
#   交互式：bash install.sh          （逐项提问，带默认值）
#   带参数：bash install.sh --domain ... --dynu-key ... --non-interactive
#
# 所有资源取自本仓库 GitHub：
#   脚本/源码 -> raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/
#   二进制    -> github.com/jiasongji/ANS-GO/releases/download/v1.0.0/
#   面板镜像  -> ghcr.io/jiasongji/ansgo-panel（--docker 模式）
#
# 适用于 Debian 11/12（含 LXC），需 root。
# =============================================================================
set -uo pipefail

REPO="jiasongji/ANS-GO"
RAW="https://raw.githubusercontent.com/${REPO}/main/deploy"
REL="https://github.com/${REPO}/releases/download"
VER="v1.2.0"
ARCH_MAP=( [x86_64]=amd64 [aarch64]=arm64 [arm64]=arm64 )
AARCH="${ARCH_MAP[$(uname -m)]:-amd64}"

# ---- 默认值 ----
DOMAIN="" DYNU_KEY="" DYNU_CID="" DYNU_SECRET="" EMAIL=""
SS_PORT=23456 ANYTLS_PORT=8443 NAIVE_PORT=443 PANEL_PORT=15608
PANEL_USER="admin" DISGUISE_PANEL="proxy:https://example.com" DISGUISE_NAIVE="proxy:https://example.com"
NONINT=0 DOCKER=0 FORCE_BIN=0

# ---- 颜色/日志 ----
if [ -t 1 ]; then C_G='\033[32m';C_Y='\033[33m';C_R='\033[31m';C_B='\033[36m';C_0='\033[0m'; else C_G='';C_Y='';C_R='';C_B='';C_0=''; fi
log(){ printf "${C_G}[*]${C_0} %s\n" "$*"; }
inf(){ printf "${C_B}[i]${C_0} %s\n" "$*"; }
warn(){ printf "${C_Y}[!]${C_0} %s\n" "$*" >&2; }
err(){ printf "${C_R}[X]${C_0} %s\n" "$*" >&2; }
hr(){ printf "\n${C_B}═══ %s ═══${C_0}\n" "$*"; }

# ---- 参数解析 ----
usage(){ cat <<EOF
用法: bash install.sh [选项]
  --domain DOMAIN          你的域名（必填）
  --dynu-key KEY           Dynu API Key（路径A，推荐）
  --dynu-client-id ID      Dynu OAuth Client ID（路径B，与 --dynu-secret 同用）
  --dynu-secret SECRET     Dynu OAuth Secret（路径B）
  --email EMAIL            ACME 注册邮箱
  --ss-port N              Shadowsocks 端口（默认 23456）
  --anytls-port N          AnyTLS 端口（默认 8443）
  --naive-port N           NaiveProxy 端口（默认 443）
  --panel-port N           面板端口（默认 15608）
  --panel-user USER        面板管理员用户名（默认 admin）
  --disguise-panel VAL     :443 直访伪装站点（proxy:<URL> 反代 / page 默认页，默认 proxy:https://example.com）
  --disguise-naive VAL     NaiveProxy 端口的伪装站点（同上格式，默认 proxy:https://example.com）
  --docker                 用 ghcr.io 镜像跑面板（否则裸金属）
  --force-bin              强制从 Releases 重装 sing-box/caddy（已装则跳过）
  --non-interactive        不交互，缺项报错退出
  -h, --help
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --domain) DOMAIN="$2"; shift 2;;
    --dynu-key) DYNU_KEY="$2"; shift 2;;
    --dynu-client-id) DYNU_CID="$2"; shift 2;;
    --dynu-secret) DYNU_SECRET="$2"; shift 2;;
    --email) EMAIL="$2"; shift 2;;
    --ss-port) SS_PORT="$2"; shift 2;;
    --anytls-port) ANYTLS_PORT="$2"; shift 2;;
    --naive-port) NAIVE_PORT="$2"; shift 2;;
    --panel-port) PANEL_PORT="$2"; shift 2;;
    --panel-user) PANEL_USER="$2"; shift 2;;
    --disguise-panel) DISGUISE_PANEL="$2"; shift 2;;
    --disguise-naive) DISGUISE_NAIVE="$2"; shift 2;;
    --docker) DOCKER=1; shift;;
    --force-bin) FORCE_BIN=1; shift;;
    --non-interactive) NONINT=1; shift;;
    -h|--help) usage; exit 0;;
    *) err "未知参数: $1"; usage; exit 1;;
  esac
done

# ---- landing 子命令：在落地机一键部署 ss-server ----
if [ "${1:-}" = "--landing" ]; then
  . /etc/ansgo-deploy/ansgo-landing.sh 2>/dev/null || true
  exit 0
fi

# ---- 交互收集 ----
ask(){ # prompt default -> 读入到 ASK_VAL
  local p="$1" d="${2:-}" v
  if [ "${NONINT:-0}" = 1 ]; then ASK_VAL="$d"; return; fi
  if [ -n "$d" ]; then printf "%s [%s]: " "$p" "$d" >&2
  else printf "%s: " "$p" >&2; fi
  read -r v
  ASK_VAL="${v:-$d}"
}
ask_req(){ # prompt ; NONINT 下为空则报错
  ask "$1" "${2:-}"; [ -n "$ASK_VAL" ] || { err "$1 必填（非交互模式请用参数传入）"; exit 2; }
}

hr "ANS-GO 一键部署"

# ---- 环境校验 ----
[ "$(id -u)" = 0 ] || { err "需 root 运行"; exit 1; }
if ! grep -qiE 'debian|ubuntu' /etc/os-release 2>/dev/null; then warn "未检测到 Debian/Ubuntu，继续但可能需手动装依赖"; fi
command -v curl >/dev/null || apt-get update && apt-get install -y curl ca-certificates
command -v python3 >/dev/null || apt-get install -y python3
command -v openssl >/dev/null || apt-get install -y openssl

# ---- stage1: 系统调优（任意服务器都做，幂等）----
hr "0/8 系统调优（stage1，幂等）"
mkdir -p /etc/sysctl.d /etc/security/limits.d /etc/systemd/journald.conf.d
# 网络内核调优（BBR/TFO/MTU/TIME_WAIT），LXC 内只读项会被自动忽略
cat > /etc/sysctl.d/99-ansgo-tune.conf <<'SYS'
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.ipv4.tcp_fastopen = 3
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.tcp_mtu_probing = 1
net.ipv4.tcp_max_tw_buckets = 1048576
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 15
net.ipv4.tcp_keepalive_time = 300
net.ipv4.tcp_keepalive_intvl = 30
net.ipv4.tcp_keepalive_probes = 3
net.ipv4.ip_local_port_range = 10000 65535
SYS
sysctl --system >/dev/null 2>&1 || warn "部分 sysctl 在 LXC 内为宿主只读，已忽略"
# 文件描述符上限
cat > /etc/security/limits.d/99-ansgo.conf <<'LIM'
* soft nofile 1048576
* hard nofile 1048576
root soft nofile 1048576
root hard nofile 1048576
LIM
# journald 限 50M
cat > /etc/systemd/journald.conf.d/size.conf <<'JOU'
[Journal]
SystemMaxUse=50M
JOU
systemctl restart systemd-journald 2>/dev/null || true
log "系统调优完成"

# ---- 交互式收集缺失项 ----
if [ -z "$DOMAIN" ]; then ask_req "域名（已解析到本机，DNS 托管在 Dynu）"; DOMAIN="$ASK_VAL"; fi
[ -n "$DOMAIN" ] || { err "域名必填"; exit 2; }
EMAIL="${EMAIL:-admin@${DOMAIN}}"
if [ -z "$DYNU_KEY" ] && [ -z "$DYNU_CID" ]; then
  inf "Dynu 凭证双保险：路径A(API Key，推荐) 或 路径B(OAuth Client ID+Secret)"
  ask "Dynu API Key（路径A，留空则用路径B）"; DYNU_KEY="$ASK_VAL"
  if [ -z "$DYNU_KEY" ]; then
    ask_req "Dynu OAuth Client ID（路径B）"; DYNU_CID="$ASK_VAL"
    ask_req "Dynu OAuth Secret（路径B）"; DYNU_SECRET="$ASK_VAL"
  fi
fi
if [ "$NONINT" = 0 ]; then
  ask "Shadowsocks 端口" "$SS_PORT"; SS_PORT="${ASK_VAL:-$SS_PORT}"
  ask "AnyTLS 端口" "$ANYTLS_PORT"; ANYTLS_PORT="${ASK_VAL:-$ANYTLS_PORT}"
  ask "NaiveProxy 端口" "$NAIVE_PORT"; NAIVE_PORT="${ASK_VAL:-$NAIVE_PORT}"
  ask "面板端口" "$PANEL_PORT"; PANEL_PORT="${ASK_VAL:-$PANEL_PORT}"
  ask "面板管理员用户名" "$PANEL_USER"; PANEL_USER="${ASK_VAL:-$PANEL_USER}"
  ask "直访伪装 :443 (proxy:<URL> 或 page)" "$DISGUISE_PANEL"; DISGUISE_PANEL="${ASK_VAL:-$DISGUISE_PANEL}"
  ask "Naive伪装 (代理端口, proxy:<URL> 或 page)" "$DISGUISE_NAIVE"; DISGUISE_NAIVE="${ASK_VAL:-$DISGUISE_NAIVE}"
fi
echo "----------------------------------------"
printf "  域名        : %s\n" "$DOMAIN"
printf "  端口        : ss=%s anytls=%s naive=%s panel=%s\n" "$SS_PORT" "$ANYTLS_PORT" "$NAIVE_PORT" "$PANEL_PORT"
printf "  直访伪装   : %s\n" "$DISGUISE_PANEL"
printf "  Naive伪装  : %s\n" "$DISGUISE_NAIVE"
printf "  Dynu 路径   : %s\n" "${DYNU_KEY:+A(API Key)}${DYNU_KEY:-${DYNU_CID:+B(OAuth)}}"
printf "  面板模式    : %s\n" "$([ "$DOCKER" = 1 ] && echo Docker || echo 裸金属)"
echo "----------------------------------------"
if [ "$NONINT" = 0 ]; then
  read -r -p "确认开始部署？[Y/n] " c; [ "${c:-Y}" = "Y" ] || [ "${c:-Y}" = "y" ] || { warn "已取消"; exit 0; }
fi

# ---- 下载助手（优先 curl）----
dl(){ # URL DEST
  if command -v curl >/dev/null; then curl -fsSL "$1" -o "$2"
  else wget -qO "$2" "$1"; fi
}
dl_or_exit(){ dl "$1" "$2" || { err "下载失败: $1"; exit 3; }; }

hr "1/8 下载部署脚本（本仓库 raw）"
mkdir -p /etc/ansgo /etc/ansgo-deploy
for f in ansgo-admin ansgo-genconf ansgo-cert-reload ansgo-cert-issue.sh dns_dynukey.sh; do
  log "下载 $f"; dl_or_exit "$RAW/$f" "/etc/ansgo-deploy/$f"
done
install -m 0755 /etc/ansgo-deploy/ansgo-admin       /usr/local/bin/ansgo-admin
install -m 0755 /etc/ansgo-deploy/ansgo-genconf     /usr/local/bin/ansgo-genconf
install -m 0755 /etc/ansgo-deploy/ansgo-cert-reload /usr/local/bin/ansgo-cert-reload

hr "2/8 确保 sing-box / caddy(naive) 就位"
# 已装且未强制则跳过；否则从本仓库 Releases 拉 vendored 二进制
if [ "$FORCE_BIN" = 1 ] || ! command -v sing-box >/dev/null; then
  log "安装 sing-box (from release, arch=$AARCH)"
  dl_or_exit "$REL/$VER/sing-box-linux-${AARCH}.tar.gz" /tmp/sb.tgz
  tar xzf /tmp/sb.tgz -C /tmp && install -m 0755 /tmp/sing-box-*/sing-box /usr/local/bin/sing-box
fi
if [ "$FORCE_BIN" = 1 ] || ! command -v caddy >/dev/null || ! caddy list-modules 2>/dev/null | grep -q forwardproxy; then
  log "安装 caddy (naive 分支, from release)"
  dl_or_exit "$REL/$VER/caddy-naive-linux-${AARCH}" /usr/local/bin/caddy && chmod 0755 /usr/local/bin/caddy
fi

hr "3/8 生成协议密钥 + 面板配置"
# secrets.env（如已存在则保留，避免覆盖现网密钥）
if [ ! -f /etc/ansgo/secrets.env ]; then
  cat > /etc/ansgo/secrets.env <<EOF
SS_METHOD=2022-blake3-aes-128-gcm
SS_KEY=$(openssl rand -base64 16)
ANYTLS_PASS=$(openssl rand -hex 16)
ANYTLS_UUID=$(cat /proc/sys/kernel/random/uuid)
NAIVE_USER=$(openssl rand -hex 6)
NAIVE_PASS=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)
EOF
  log "已生成 /etc/ansgo/secrets.env"
else warn "/etc/ansgo/secrets.env 已存在，保留现有密钥"; fi
chmod 600 /etc/ansgo/secrets.env

# panel.json（端口/设置/域名/伪装）
URLPATH="/$(openssl rand -hex 4)/"
if [ ! -f /etc/ansgo/panel.json ]; then
  cat > /etc/ansgo/panel.json <<EOF
{
  "domain": "${DOMAIN}",
  "panel_port": ${PANEL_PORT},
  "url_path": "${URLPATH}",
  "admin_user": "${PANEL_USER}",
  "admin_pass_hash": "PLACEHOLDER",
  "session_hours": 8,
  "login_lock_threshold": 5,
  "login_lock_minutes": 10,
  "ss_port": ${SS_PORT},
  "ss_method": "2022-blake3-aes-128-gcm",
  "anytls_port": ${ANYTLS_PORT},
  "naive_port": ${NAIVE_PORT},
  "disguise_panel": "${DISGUISE_PANEL}",
  "disguise_naive": "${DISGUISE_NAIVE}",
  "cert_dir": "/etc/ssl/ansgo",
  "db_path": "/etc/ansgo/sessions.db"
}
EOF
  log "已生成 /etc/ansgo/panel.json (url_path=${URLPATH})"
else warn "/etc/ansgo/panel.json 已存在，保留（端口/伪装以现有为准）"; fi
chmod 600 /etc/ansgo/panel.json

hr "4/8 签发 Let's Encrypt 证书（DNS-01，A 默认 / B 降级）"
mkdir -p /etc/ssl/ansgo
# acme.sh 来源：优先本仓库 vendored 快照，回退官方
ACME_TARBALL=""
if dl "$REL/$VER/acme.sh-master.tar.gz" /tmp/acme.tar.gz 2>/dev/null; then ACME_TARBALL=/tmp/acme.tar.gz; log "使用本仓库 vendored acme.sh"; fi
export DOMAIN EMAIL ACME_TARBALL
export DYNU_API_KEY="$DYNU_KEY" DYNU_CLIENT_ID="$DYNU_CID" DYNU_SECRET="$DYNU_SECRET"
log "后台签发中…（日志 /root/ansgo-cert-issue.log）"
nohup bash /etc/ansgo-deploy/ansgo-cert-issue.sh > /root/ansgo-cert-issue.log 2>&1 </dev/null &
CERT_PID=$!
# 轮询等签发（最长 ~180s）
for i in $(seq 1 60); do
  sleep 3
  [ -f /etc/ansgo-cert.status ] && break
done
wait "$CERT_PID" 2>/dev/null || true
if grep -q '^SUCCESS' /etc/ansgo-cert.status 2>/dev/null; then
  log "✅ 证书签发成功 ($(head -1 /etc/ansgo-cert.status))"
else
  err "证书签发失败，详见 /root/ansgo-cert-issue.log（已保留，服务暂用占位/旧证书）"
  warn "可稍后手动重试：DOMAIN=$DOMAIN bash /etc/ansgo-deploy/ansgo-cert-issue.sh"
fi

hr "5/8 生成服务配置并校验"
ansgo-genconf all
ansgo-genconf validate || { err "配置校验失败，请检查 /etc/ansgo/ 与 /etc/ansgo/secrets.env"; exit 4; }

hr "6/8 部署 systemd unit（sing-box / caddy）"
# sing-box / caddy 的 unit 由本仓库提供（若不存在）
for s in sing-box caddy; do
  if [ ! -f /etc/systemd/system/$s.service ]; then
    dl "$RAW/systemd/$s.service" /etc/systemd/system/$s.service 2>/dev/null && log "已安装 $s.service"
  fi
done

hr "7/8 启动 / 重启代理服务"
systemctl daemon-reload
systemctl enable sing-box caddy >/dev/null 2>&1 || true
systemctl restart sing-box && log "sing-box 已启动"
systemctl restart caddy 2>/dev/null || systemctl start caddy && log "caddy 已启动"

hr "8/8 部署 Web 管理面板"
if [ "$DOCKER" = 1 ]; then
  if ! command -v docker >/dev/null; then curl -fsSL https://get.docker.com | sh; fi
  # 面板需访问宿主 systemd/配置，故 host 网络 + 挂载关键目录
  # 镜像默认 public 可匿名拉取；若为 private 需先 docker login ghcr.io
  if ! docker pull ghcr.io/${REPO%/*}/ansgo-panel:latest 2>/dev/null; then
    warn "ghcr 镜像拉取失败（可能为 private）。请先执行 docker login ghcr.io 后重试，或在 GitHub 将该 package 设为 public。"
    exit 5
  fi
  dl_or_exit "$RAW/ansgo-panel.service.docker" /etc/systemd/system/ansgo-panel.service
else
  log "下载 ansgo-panel 二进制 (from release)"
  dl_or_exit "$REL/$VER/ansgo-panel-linux-${AARCH}" /usr/local/bin/ansgo-panel
  chmod 0755 /usr/local/bin/ansgo-panel
  dl_or_exit "$RAW/ansgo-panel.service" /etc/systemd/system/ansgo-panel.service
fi
systemctl daemon-reload
# 设置管理员密码（bcrypt 写入 panel.json，打印明文仅一次）
PANEL_PW=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)
/usr/local/bin/ansgo-panel -setpass "$PANEL_PW" 2>/dev/null || true
systemctl enable ansgo-panel >/dev/null 2>&1 || true
systemctl restart ansgo-panel && log "ansgo-panel 已启动"
sleep 2

# ---- 输出最终信息 ----
hr "部署完成"
ansgo-admin info 2>/dev/null || true
URL_PATH=$(python3 -c "import json;print(json.load(open('/etc/ansgo/panel.json'))['url_path'])" 2>/dev/null)
cat <<EOF

═══════════════════════════════════════════════════
  Web 面板:   https://${DOMAIN}:${PANEL_PORT}${URL_PATH:-/}
  用户名:     ${PANEL_USER}
  密码(仅此一次): ${PANEL_PW}
═══════════════════════════════════════════════════
  离线管理:   ansgo-admin status | info | regen <协议>
  忘记密码:   ansgo-admin panel-pass
  忘记路径:   ansgo-admin panel-path
═══════════════════════════════════════════════════
EOF
log "完成。请记录上面的面板地址与密码。"
