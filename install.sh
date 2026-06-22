#!/usr/bin/env bash
# =============================================================================
# ANS-GO 一键部署脚本 (install.sh)
#   交互式：bash install.sh          （逐项提问，带默认值）
#   带参数：bash install.sh --domain ... --dynu-key ... --non-interactive
#   Docker：bash install.sh --domain ... --docker --non-interactive
#   卸载  ：bash install.sh --uninstall           # 移除服务/容器，保留配置/卷
#           bash install.sh --uninstall --purge   # 彻底删除配置/卷/镜像
#
# 所有资源取自本仓库 GitHub / 官方上游：
#   脚本/源码    -> raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/
#   面板二进制    -> github.com/jiasongji/ANS-GO/releases/download/$VER/ansgo-panel-linux-<arch>
#   sing-box      -> SagerNet 官方 release（多架构）
#   caddy(naive) -> klzgrad/forwardproxy 源码 + xcaddy 编译（Docker 内）或本项目预编译产物
#   面板镜像      -> ghcr.io/jiasongji/ansgo（all-in-one，--docker 模式）
#
# 适用于 Debian 11/12（含 LXC），需 root。
# =============================================================================
set -uo pipefail

# curl | bash 管道形式下，bash 的 stdin 被 curl 的输出占用，导致脚本里的
# read（如卸载确认「输入 yes」、部署确认、ask 交互提问）直接读到 EOF 而失败。
# 检测到 stdin 非 tty 且 stdout 是 tty（用户在终端里跑）且有 /dev/tty 可用时，
# 把 stdin 重定向到 /dev/tty，恢复交互输入能力。
#   - --non-interactive 模式不触发任何 read，本段对 CI/自动化无影响
#   - 无 /dev/tty（纯 CI 容器）时跳过，走 --non-interactive 即可
if [ ! -t 0 ] && [ -t 1 ] && [ -c /dev/tty ]; then
  exec 0</dev/tty 2>/dev/null || true
fi

REPO="jiasongji/ANS-GO"
RAW="https://raw.githubusercontent.com/${REPO}/main/deploy"
REL="https://github.com/${REPO}/releases/download"
VER="v1.5.1"
# 架构映射（uname -m -> 下载用后缀）；用 case 避免关联数组在 set -u 下的 unbound variable 陷阱
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)        AARCH=amd64 ;;
  aarch64|arm64) AARCH=arm64 ;;
  *)             AARCH=amd64 ;;
esac

# ---- 默认值 ----
DOMAIN="" DYNU_KEY="" DYNU_CID="" DYNU_SECRET="" EMAIL=""
SS_PORT=23456 ANYTLS_PORT=8443 NAIVE_PORT=443 PANEL_PORT=15608
PANEL_USER="admin" DISGUISE_PANEL="proxy:https://example.com" DISGUISE_NAIVE="proxy:https://example.com"
# 证书来源：acme（默认，需 Dynu 凭证）/ manual（手动指定已有证书+私钥路径，二选一）
CERT_MODE="acme" CERT_FILE="" KEY_FILE=""
NONINT=0 DOCKER=0 FORCE_BIN=0 UNINSTALL=0 PURGE=0

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
  --cert-mode MODE         证书来源：acme（默认，用 Dynu DNS-01 自动签发）| manual（手动指定已有证书，跳过 Dynu/acme）
  --cert-fullchain PATH    manual 模式：证书文件完整绝对路径（如 /etc/letsencrypt/live/x.com/fullchain.pem）
  --cert-privkey PATH      manual 模式：私钥文件完整绝对路径（如 /etc/letsencrypt/live/x.com/privkey.pem）
  --docker                 用 ghcr.io all-in-one 镜像跑（KVM；否则裸金属）
  --force-bin              强制从 Releases 重装 sing-box/caddy（已装则跳过）
  --non-interactive        不交互，缺项报错退出
  --uninstall              卸载 ANS-GO（自动检测 Docker/裸金属；保留配置/卷）
  --purge                  与 --uninstall 同用：彻底删除配置/密钥/证书/卷/镜像
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
    --cert-mode) CERT_MODE="$2"; shift 2;;
    --cert-fullchain) CERT_FILE="$2"; shift 2;;
    --cert-privkey) KEY_FILE="$2"; shift 2;;
    --docker) DOCKER=1; shift;;
    --force-bin) FORCE_BIN=1; shift;;
    --non-interactive) NONINT=1; shift;;
    --uninstall) UNINSTALL=1; shift;;
    --purge) PURGE=1; shift;;
    -h|--help) usage; exit 0;;
    *) err "未知参数: $1"; usage; exit 1;;
  esac
done

# =============================================================================
# 卸载（apt purge 风格两级：默认保留配置/卷，--purge 彻底删除一切）
#   自动检测部署模式：Docker（/etc/ansgo-docker/）或裸金属（/etc/ansgo/）
# =============================================================================
do_uninstall(){
  [ "$(id -u)" = 0 ] || { err "需 root 运行"; exit 1; }
  local PURGE="${PURGE:-0}"

  hr "ANS-GO 卸载（purge=$PURGE）"

  # ---- 检测部署模式 ----
  local HAS_DOCKER=0 HAS_METAL=0
  [ -f /etc/ansgo-docker/docker-compose.yml ] && HAS_DOCKER=1
  { [ -f /etc/ansgo/panel.json ] || systemctl list-unit-files 2>/dev/null | grep -qE 'ansgo-panel|sing-box|caddy' \
    || [ -x /usr/local/bin/ansgo-panel ]; } && HAS_METAL=1

  if [ "$HAS_DOCKER" = 0 ] && [ "$HAS_METAL" = 0 ]; then
    warn "未检测到 ANS-GO 部署痕迹（无 /etc/ansgo-docker、无 /etc/ansgo/、无 systemd unit）"
    warn "可能从未安装，或已卸载。退出。"
    exit 0
  fi

  echo "检测到的部署模式：$([ "$HAS_DOCKER" = 1 ] && echo 'Docker ')$([ "$HAS_METAL" = 1 ] && echo '裸金属')"
  if [ "$PURGE" = 1 ]; then
    warn "--purge 模式：将彻底删除全部配置/密钥/证书/数据卷/镜像/系统调优，不可恢复"
  else
    echo "默认模式：移除服务/容器/二进制，保留配置/卷（可重装不丢参数）"
    echo "如需彻底删除一切，用: bash install.sh --uninstall --purge"
  fi
  read -r -p "确认卸载？输入 yes: " a; [ "$a" = yes ] || { echo "已取消"; exit 0; }

  # ---- Docker 模式卸载 ----
  if [ "$HAS_DOCKER" = 1 ]; then
    # 兼手清理：不论 compose 文件在不在，都强制删同名容器（避免 compose down 遗漏残留）
    docker rm -f ansgo 2>/dev/null || true
    if command -v docker >/dev/null 2>&1 && [ -f /etc/ansgo-docker/docker-compose.yml ]; then
      log "[Docker] 停止并删除容器..."
      ( cd /etc/ansgo-docker && docker compose down --remove-orphans 2>/dev/null \
        || docker-compose down 2>/dev/null || true )
      # 再次兑底：compose down 可能因 project 名不一致而遗漏
      docker rm -f ansgo 2>/dev/null || true
      if [ "$PURGE" = 1 ]; then
        log "[Docker] 删除数据卷（配置/密钥/证书/acme）..."
        ( cd /etc/ansgo-docker && docker compose down -v --remove-orphans 2>/dev/null \
          || docker-compose down -v 2>/dev/null || true )
        # 兑底删除可能残留的卷（compose project 名不同时）；按名称模式匹配，
        # 覆盖 ansgo_* / ansgo-docker_ansgo_* / 自定义 project 名的卷
        docker volume ls --format '{{.Name}}' 2>/dev/null \
          | grep -iE 'ansgo_(etc|ssl|caddy|sb|acme)$|_ansgo_(etc|ssl|caddy|sb|acme)$' \
          | xargs -r docker volume rm 2>/dev/null || true
        log "[Docker] 删除本地镜像（容器已删，镜像可安全移除）..."
        docker image rm ghcr.io/jiasongji/ansgo:latest 2>/dev/null || true
        docker image rm ghcr.io/jiasongji/ansgo-panel:latest 2>/dev/null || true
      fi
    else
      warn "[Docker] docker 命令缺失或 compose 文件不在，已尝试手动清理容器"
      [ "$PURGE" = 1 ] && docker volume ls --format '{{.Name}}' 2>/dev/null \
        | grep -iE 'ansgo_(etc|ssl|caddy|sb|acme)$|_ansgo_(etc|ssl|caddy|sb|acme)$' \
        | xargs -r docker volume rm 2>/dev/null || true
    fi
    if [ "$PURGE" = 1 ]; then
      rm -rf /etc/ansgo-docker
      ok "[Docker] 已删除 /etc/ansgo-docker/（含 ansgo.env + compose）"
    else
      warn "[Docker] 保留 /etc/ansgo-docker/（含凭证，重装可复用）"
    fi
  fi

  # ---- 裸金属模式卸载 ----
  if [ "$HAS_METAL" = 1 ]; then
    log "[裸金属] 停止并禁用服务..."
    for s in ansgo-panel caddy sing-box; do
      systemctl stop "$s" 2>/dev/null || true
      systemctl disable "$s" 2>/dev/null || true
      rm -f "/etc/systemd/system/$s.service"
      rm -f "/etc/systemd/system/multi-user.target.wants/$s.service"
    done
    systemctl daemon-reload 2>/dev/null || true
    log "[裸金属] 移除管理二进制 + 脚本..."
    rm -f /usr/local/bin/ansgo-panel /usr/local/bin/ansgo-admin \
          /usr/local/bin/ansgo-genconf /usr/local/bin/ansgo-cert-reload \
          /usr/local/bin/ansgo-cert-issue.sh /usr/local/bin/dns_dynukey.sh
    if [ "$PURGE" = 1 ]; then
      log "[裸金属] --purge：移除代理二进制 + 配置/密钥/证书/备份/调优..."
      rm -f /usr/local/bin/sing-box /usr/local/bin/caddy
      rm -rf /etc/ansgo /etc/ssl/ansgo /etc/sing-box /etc/caddy /var/www/html
      rm -rf /root/.acme.sh /etc/ansgo-deploy
      rm -rf /etc/ansgo-backup-* /etc/ansgo-uninstall-backup-*
      rm -f /etc/sysctl.d/99-ansgo-tune.conf /etc/security/limits.d/99-ansgo.conf
      ok "[裸金属] 配置/密钥/证书/备份/调优已彻底删除"
    else
      ok "[裸金属] 配置保留在 /etc/ansgo/ /etc/ssl/ansgo/（--purge 可彻底删）"
    fi
  fi

  hr "卸载完成（purge=$PURGE）"
  [ "$PURGE" = 0 ] && echo "提示：彻底删除一切请重跑  bash install.sh --uninstall --purge"
  echo "注：未卸载 docker 本体（可能被其他服务使用）；如需卸载 docker 请手动执行。"
  exit 0
}

# ---- 卸载子命令（在交互收集前拦截，避免无谓的域名/凭证提问）----
if [ "${UNINSTALL:-0}" = 1 ]; then do_uninstall; exit 0; fi

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

# =============================================================================
# Docker 一体化部署（all-in-one 容器，KVM / 资源充足主机推荐）
#   一个容器跑全部服务（caddy :443 伪装 + sing-box 按需 + ansgo-panel），
#   容器内 systemd 作 PID 1，复用裸金属全部 unit/脚本/面板代码（0 改动）。
#   配置/密钥/证书在容器内首次生成，持久化到 docker volume。
# =============================================================================
do_docker_deploy(){
  hr "Docker 一体化部署（all-in-one 容器）"
  command -v docker >/dev/null 2>&1 || { log "安装 docker ..."; curl -fsSL https://get.docker.com | sh; }

  # host 网络端口冲突预警
  for p in 80 443 "$PANEL_PORT"; do
    if command -v ss >/dev/null && ss -tln 2>/dev/null | awk '{print $4}' | grep -qE ":$p$"; then
      warn "端口 $p 已被占用，host 网络下容器可能启动失败，请先停掉占用进程"
    fi
  done

  local DIR=/etc/ansgo-docker
  mkdir -p "$DIR" || { err "无法创建 $DIR"; exit 1; }

  # 随机面板密码 + URL 路径（仅首次部署生效；卷已存在时以容器内实际为准）
  local PANEL_PW URL_PATH
  PANEL_PW=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)
  URL_PATH="/$(openssl rand -hex 4)/"

  # ansgo.env：凭证 + 端口（600 权限，敏感不入 git）
  cat > "$DIR/ansgo.env" <<EOF
DOMAIN=${DOMAIN}
EMAIL=${EMAIL}
PANEL_USER=${PANEL_USER}
PANEL_PASS=${PANEL_PW}
URL_PATH=${URL_PATH}
PANEL_PORT=${PANEL_PORT}
SS_PORT=${SS_PORT}
ANYTLS_PORT=${ANYTLS_PORT}
NAIVE_PORT=${NAIVE_PORT}
DISGUISE_PANEL=${DISGUISE_PANEL}
DISGUISE_NAIVE=${DISGUISE_NAIVE}
DYNU_API_KEY=${DYNU_KEY}
DYNU_CLIENT_ID=${DYNU_CID}
DYNU_SECRET=${DYNU_SECRET}
CERT_MODE=${CERT_MODE}
CERT_FULLCHAIN=${CERT_FILE}
CERT_PRIVKEY=${KEY_FILE}
EOF
  chmod 600 "$DIR/ansgo.env"
  log "已生成 $DIR/ansgo.env（含凭证，权限 600）"

  dl_or_exit "$RAW/docker-compose.yml" "$DIR/docker-compose.yml"
  cd "$DIR" || exit 1

  # 拉镜像；失败则 clone 仓库本地构建
  if ! docker compose pull 2>/dev/null; then
    warn "ghcr 镜像拉取失败，尝试本地构建（需下载 sing-box/caddy，国内建议配 docker 代理）"
    command -v git >/dev/null 2>&1 || apt-get install -y git
    rm -rf /tmp/ansgo-build
    if git clone --depth 1 https://github.com/${REPO} /tmp/ansgo-build 2>/dev/null; then
      docker build -t ghcr.io/${REPO%/*}/ansgo:latest -f deploy/Dockerfile.allinone /tmp/ansgo-build \
        || { err "本地构建失败，请检查网络/代理后重跑 install.sh --docker"; exit 6; }
    else
      err "git clone 失败。请手动：git clone https://github.com/${REPO} && docker build -f deploy/Dockerfile.allinone ."
      exit 5
    fi
  fi

  docker compose up -d && log "容器已启动" || { err "docker compose up 失败"; exit 7; }
  sleep 5

  # 读取容器内实际 url_path（卷已存在时可能与此处生成的不一致）
  local ACTUAL_PATH
  ACTUAL_PATH=$(docker exec ansgo python3 -c 'import json;print(json.load(open("/etc/ansgo/panel.json")).get("url_path","/"))' 2>/dev/null || echo "$URL_PATH")

  hr "部署完成"
  cat <<EOF

═══════════════════════════════════════════════════
  ✅ ANS-GO 一体化部署完成（Docker / KVM）
═══════════════════════════════════════════════════
  Web 面板:   https://${DOMAIN}:${PANEL_PORT}${ACTUAL_PATH}
  用户名:     ${PANEL_USER}
  密码(首次): ${PANEL_PW}
───────────────────────────────────────────────────
  ⚠️ 证书: $([ "$CERT_MODE" = "manual" ] && echo "手动模式（${CERT_FILE}），无需后台签发" || echo "Let's Encrypt 后台签发中（约 1-3 分钟），首次访问若提示不受信稍候刷新即可")
  管理命令:
     cd ${DIR} && docker compose logs -f ansgo   # 查日志/签证书进度
     docker exec ansgo ansgo-admin status         # 服务状态
     docker exec ansgo ansgo-admin info           # 节点连接参数
  下一步: 登录面板 →「服务安装」页按需开启代理服务
═══════════════════════════════════════════════════
EOF
  log "代理服务默认未启动，请在面板「服务安装」页按需开启。"
  exit 0
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
if [ -z "$DOMAIN" ]; then ask_req "域名（已解析到本机）"; DOMAIN="$ASK_VAL"; fi
[ -n "$DOMAIN" ] || { err "域名必填"; exit 2; }
EMAIL="${EMAIL:-admin@${DOMAIN}}"
# 证书来源：未通过参数指定时询问（acme 需要 Dynu 凭证，manual 跳过 Dynu）
if [ "$CERT_MODE" != "manual" ] && [ "$CERT_MODE" != "acme" ]; then
  if [ "$NONINT" = 0 ]; then
    inf "证书来源：1) acme 自动签发（需要 Dynu DNS 凭证，推荐）  2) 手动指定已有证书路径"
    ask "选择 [1/acme 或 2/manual]" "acme"; CERT_MODE="${ASK_VAL:-acme}"
    case "$CERT_MODE" in 1|acme|ACME) CERT_MODE="acme";; 2|manual|MANUAL) CERT_MODE="manual";; *) CERT_MODE="acme";; esac
  else
    CERT_MODE="acme"
  fi
fi
if [ "$CERT_MODE" = "manual" ]; then
  # manual 模式：必须提供证书+私钥路径
  if [ -z "$CERT_FILE" ]; then ask_req "证书文件完整路径（fullchain）"; CERT_FILE="$ASK_VAL"; fi
  if [ -z "$KEY_FILE" ];  then ask_req "私钥文件完整路径（privkey）"; KEY_FILE="$ASK_VAL"; fi
  [ -f "$CERT_FILE" ] || { err "证书文件不存在: $CERT_FILE"; exit 2; }
  [ -f "$KEY_FILE" ]  || { err "私钥文件不存在: $KEY_FILE"; exit 2; }
  inf "证书来源：手动模式（跳过 Dynu / acme 签发）"
else
  # acme 模式：需要 Dynu 凭证
  CERT_MODE="acme"
  if [ -z "$DYNU_KEY" ] && [ -z "$DYNU_CID" ]; then
    inf "Dynu 凭证双保险：路径A(API Key，推荐) 或 路径B(OAuth Client ID+Secret)"
    ask "Dynu API Key（路径A，留空则用路径B）"; DYNU_KEY="$ASK_VAL"
    if [ -z "$DYNU_KEY" ]; then
      ask_req "Dynu OAuth Client ID（路径B）"; DYNU_CID="$ASK_VAL"
      ask_req "Dynu OAuth Secret（路径B）"; DYNU_SECRET="$ASK_VAL"
    fi
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
if [ "$CERT_MODE" = "manual" ]; then
  printf "  证书来源    : 手动（%s）\n" "$CERT_FILE"
else
  printf "  证书来源    : acme（Dynu %s）\n" "${DYNU_KEY:+路径A(API Key)}${DYNU_KEY:-${DYNU_CID:+路径B(OAuth)}}"
fi
printf "  面板模式    : %s\n" "$([ "$DOCKER" = 1 ] && echo Docker || echo 裸金属)"
echo "----------------------------------------"
if [ "$NONINT" = 0 ]; then
  read -r -p "确认开始部署？[Y/n] " c; [ "${c:-Y}" = "Y" ] || [ "${c:-Y}" = "y" ] || { warn "已取消"; exit 0; }
fi

# ---- Docker 一体化分流（KVM 主机推荐）：容器内跑全部服务，这里完成后即退出 ----
if [ "$DOCKER" = 1 ]; then
  do_docker_deploy
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
# 从本项目 Releases 拉 vendored 二进制（sing-box 1.13.13 / caddy-naive v2.11.4）
# 均为 CGO-free 纯静态；sing-box 官方包含 glibc 依赖，debian/ubuntu 原生支持
if [ "$FORCE_BIN" = 1 ] || ! command -v sing-box >/dev/null; then
  log "安装 sing-box (from release, arch=$AARCH)"
  dl_or_exit "$REL/$VER/sing-box-linux-${AARCH}.tar.gz" /tmp/sb.tgz
  tar xzf /tmp/sb.tgz -C /tmp && install -m 0755 /tmp/sing-box-*/sing-box /usr/local/bin/sing-box
fi
if [ "$FORCE_BIN" = 1 ] || ! command -v caddy >/dev/null || ! caddy list-modules 2>/dev/null | grep -q forwardproxy; then
  log "安装 caddy (naive 分支, from release, arch=$AARCH)"
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
  "disguise_naive2": "${DISGUISE_NAIVE}",
  "svc_ss_enabled": "false",
  "svc_anytls_enabled": "false",
  "svc_naive_enabled": "false",
  "cert_mode": "${CERT_MODE}",
  "cert_dir": "/etc/ssl/ansgo",
  "cert_fullchain": "${CERT_FILE}",
  "cert_privkey": "${KEY_FILE}",
  "db_path": "/etc/ansgo/sessions.db"
}
EOF
  log "已生成 /etc/ansgo/panel.json (url_path=${URLPATH})"
else warn "/etc/ansgo/panel.json 已存在，保留（端口/伪装以现有为准）"; fi
chmod 600 /etc/ansgo/panel.json

mkdir -p /etc/ssl/ansgo
if [ "$CERT_MODE" = "manual" ]; then
  hr "4/8 使用手动指定证书（跳过 acme 签发）"
  # 二次校验文件存在且可读（交互阶段已校验，此处幂等兜底）
  [ -f "$CERT_FILE" ] || { err "证书文件不存在: $CERT_FILE"; exit 2; }
  [ -f "$KEY_FILE" ]  || { err "私钥文件不存在: $KEY_FILE"; exit 2; }
  log "✅ 手动证书模式："
  log "   证书: $CERT_FILE"
  log "   私钥: $KEY_FILE"
  log "   （面板/caddy/sing-box 将直接引用上述绝对路径，不再走 /etc/ssl/ansgo/）"
  # 不复制到 /etc/ssl/ansgo，保留用户原位置；续期由用户自行管理
  echo "SUCCESS manual-mode (cert=$CERT_FILE key=$KEY_FILE)" > /etc/ansgo-cert.status
else
  hr "4/8 签发 Let's Encrypt 证书（DNS-01，A 默认 / B 降级）"
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
fi

hr "5/7 生成配置（代理服务默认关闭，面板内按需安装）"
# 初始化密钥（面板安装服务时需要）
ansgo-genconf all 2>&1 | tail -3

hr "6/7 部署 systemd unit + 启动面板与伪装站"
# sing-box / caddy 的 unit（面板安装服务时会 enable）
for s in sing-box caddy; do
  if [ ! -f /etc/systemd/system/$s.service ]; then
    dl "$RAW/systemd/$s.service" /etc/systemd/system/$s.service 2>/dev/null && log "已安装 $s.service"
  fi
done
systemctl daemon-reload
# caddy 启动（:443 伪装站 + :80 跳转 是域名直访体验的一部分，随面板一起起来）
systemctl enable caddy >/dev/null 2>&1 || true
systemctl restart caddy && log "caddy 已启动（:443 伪装站）"
# sing-box 暂不启动（无代理服务安装前不需要）

hr "7/7 部署 Web 管理面板"
log "下载 ansgo-panel 二进制 (from release)"
dl_or_exit "$REL/$VER/ansgo-panel-linux-${AARCH}" /usr/local/bin/ansgo-panel
chmod 0755 /usr/local/bin/ansgo-panel
dl_or_exit "$RAW/ansgo-panel.service" /etc/systemd/system/ansgo-panel.service
systemctl daemon-reload
# 设置管理员密码（bcrypt 写入 panel.json，打印明文仅一次）
PANEL_PW=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)
/usr/local/bin/ansgo-panel -setpass "$PANEL_PW" 2>/dev/null || true
systemctl enable ansgo-panel >/dev/null 2>&1 || true
systemctl restart ansgo-panel && log "ansgo-panel 已启动"
sleep 2

# ---- 输出最终信息 ----
hr "部署完成"
URL_PATH=$(python3 -c "import json;print(json.load(open('/etc/ansgo/panel.json'))['url_path'])" 2>/dev/null)
cat <<EOF

═══════════════════════════════════════════════════
  ✅ ANS-GO 管理面板部署完成
═══════════════════════════════════════════════════
  Web 面板:   https://${DOMAIN}:${PANEL_PORT}${URL_PATH:-/}
  用户名:     ${PANEL_USER}
  密码(仅此一次): ${PANEL_PW}
───────────────────────────────────────────────────
  下一步：登录面板 →「服务安装」页，按需安装代理服务
           （Shadowsocks / AnyTLS / NaiveProxy）
           第二组服务（走 SS 落地）在「第二组服务」页
═══════════════════════════════════════════════════
EOF
log "完成。代理服务未启动，请在面板「服务安装」页按需开启。"
