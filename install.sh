#!/usr/bin/env bash
# =============================================================================
# ANS-GO 一键部署脚本 (install.sh)   v1.5.12
#   交互式：bash install.sh          （主菜单：安装/卸载/彻底卸载/落地）
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
# ---- bootstrap：把脚本完整落地后再执行（解决 curl|bash 的两个问题） ----
# 问题1 SIGPIPE：`curl ... | bash -s -- ...` 形式下 bash 从管道逐块读脚本执行。
#   脚本中途任何 exit（do_uninstall/--landing/--help/错误退出）都会让 bash 提前结束
#   → 管道读端关闭 → curl 还在写剩余字节 → curl 收到 SIGPIPE → 报 (23)。
# 问题2 进程替换卡死：`bash <(curl ...)` 下 BASH_SOURCE=/dev/fd/NN，脚本内容在 fd，
#   fd0 是用户终端。若误用 cat（无参数）从 fd0 读 → 吃掉用户终端输入 → 卡死。
# 修复：检测管道/进程替换运行 → 落地临时文件 → exec 重跑。关键：
#   - 进程替换判断用 [ -e ]（fd 通过 -e 测试，但 [ -f ] 失败因为非常规文件）
#   - 进程替换下 cat "$_ansi_self"（从 /dev/fd/NN 读脚本），绝不 cat 无参数（会吃终端）
#   - 管道下（BASH_SOURCE 空）才 cat 无参数（从 fd0=管道读）
#   - `bash install.sh`（真实文件）→ BASH_SOURCE 常规路径 → 跳过 bootstrap
_ansi_self="${BASH_SOURCE[0]:-}"
case "$_ansi_self" in
  "" | /dev/fd/* | /proc/self/fd/*)
    # 来自管道或进程替换：落地到临时文件后 exec 重跑（保留所有原参数）
    _ansi_tmp="$(mktemp 2>/dev/null || mktemp -t ansgo)"
    # 注意：进程替换下 BASH_SOURCE=/dev/fd/NN，但 [ -f ] 对 fd 返回 false（非常规文件），
    #   必须用 [ -e ] 判断；且脚本内容在 /dev/fd/NN 里，不在 stdin(fd0=用户终端)。
    #   管道模式下 BASH_SOURCE 为空，脚本内容在 stdin，必须 cat 从 stdin 读。
    if [ -n "$_ansi_self" ] && [ -e "$_ansi_self" ]; then
      # 进程替换：脚本内容在 /dev/fd/NN，cat 它（绝不能 cat 无参数从 fd0 读，那会吃用户终端输入）
      cat "$_ansi_self" > "$_ansi_tmp" 2>/dev/null || cp "$_ansi_self" "$_ansi_tmp" 2>/dev/null
    else
      # 管道：BASH_SOURCE 为空，脚本内容在 stdin，cat 从 stdin 读
      cat > "$_ansi_tmp" 2>/dev/null
    fi
    # 落地失败则继续从管道执行（退化为旧行为，不至于卡死）
    if [ -s "$_ansi_tmp" ]; then
      chmod +rx "$_ansi_tmp" 2>/dev/null
      # 标记让新脚本退出时清理自身（避免 /tmp 残留）
      export _ANSGO_BOOTSTRAP_TMP="$_ansi_tmp"
      exec bash "$_ansi_tmp" "$@"
    fi
    rm -f "$_ansi_tmp" 2>/dev/null
    ;;
esac
unset _ansi_self _ansi_tmp
# ---- bootstrap end ----

set -uo pipefail

# bootstrap 落地模式：退出时清理临时文件（仅 curl|bash 形式产生，文件模式不触发）
if [ -n "${_ANSGO_BOOTSTRAP_TMP:-}" ] && [ -f "$_ANSGO_BOOTSTRAP_TMP" ]; then
  trap 'rm -f "$_ANSGO_BOOTSTRAP_TMP" 2>/dev/null' EXIT
fi

# curl | bash 管道形式下，bash 的 stdin(fd0) 被 curl 输出占用。若用 exec 0</dev/tty
# 全局切走 fd0，bash 会读不到脚本剩余部分（管道还在喂脚本）→ 后续代码全部不执行。
# 故不能在脚本中途对 fd0 做 exec；改为定义 readtty 函数，仅在某次 read 时临时把
# stdin 指到 /dev/tty（局部重定向，不动全局 fd0），读完即恢复。
#   - NONINT 模式不调用 readtty，本机制对 CI/自动化 无影响
#   - 无 /dev/tty（纯 CI 容器）时 readtty 回退到原 stdin（读 EOF → 返回空）
readtty(){ # 从 /dev/tty 读一行（curl|bash 下 stdin 被管道占用时的交互读取）
  local line=""
  if [ -c /dev/tty ]; then
    # 用子 shell + 局部重定向：仅这次 read 的 fd0 是 /dev/tty，不影响主脚本读管道
    line=$(read -r _LINE </dev/tty 2>/dev/null && printf '%s' "$_LINE") || line=""
  fi
  printf '%s' "$line"
}

REPO="jiasongji/ANS-GO"
RAW="https://raw.githubusercontent.com/${REPO}/main/deploy"
REL="https://github.com/${REPO}/releases/download"
VER="v1.5.12"         # 面板二进制 release tag（install.sh 脚本本体 v1.5.12）
# 架构映射（uname -m -> 下载用后缀）；用 case 避免关联数组在 set -u 下的 unbound variable 陷阱
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)        AARCH=amd64 ;;
  aarch64|arm64) AARCH=arm64 ;;
  *)             AARCH=amd64 ;;
esac

# ---- 默认值 ----
# v1.5.12: 所有端口默认空（未指定），在 validate_inputs 后随机生成（10000-65535，
#   避开 80/443/25822/互相冲突/已占用）。用户通过 --ss-port 等参数可显式指定。
DOMAIN="" DYNU_KEY="" DYNU_CID="" DYNU_SECRET="" EMAIL=""
SS_PORT="" ANYTLS_PORT="" NAIVE_PORT="" PANEL_PORT=""
PANEL_USER="admin" DISGUISE_PANEL="proxy:https://example.com" DISGUISE_NAIVE="proxy:https://example.com"
# 证书来源：acme（默认，需 Dynu 凭证）/ manual（手动指定已有证书+私钥路径，二选一）
CERT_MODE="acme" CERT_FILE="" KEY_FILE=""
# 各服务密码/密钥（v1.5.5 新增；留空则部署时自动随机生成）
SS_PASSWORD="" ANYTLS_PASSWORD="" ANYTLS_UUID_IN="" NAIVE_USER_IN="" NAIVE_PASSWORD=""
PANEL_PASSWORD_IN="" URL_PATH_IN=""
NONINT=0 DOCKER=0 FORCE_BIN=0 UNINSTALL=0 PURGE=0 LANDING=0 NO_CADDY=0
LANDING_ARGS=()

# ---- 颜色/日志 ----
if [ -t 1 ]; then C_G='\033[32m';C_Y='\033[33m';C_R='\033[31m';C_B='\033[36m';C_0='\033[0m'; else C_G='';C_Y='';C_R='';C_B='';C_0=''; fi
log(){ printf "${C_G}[*]${C_0} %s\n" "$*"; }
inf(){ printf "${C_B}[i]${C_0} %s\n" "$*"; }
warn(){ printf "${C_Y}[!]${C_0} %s\n" "$*" >&2; }
err(){ printf "${C_R}[X]${C_0} %s\n" "$*" >&2; }
hr(){ printf "\n${C_B}═══ %s ═══${C_0}\n" "$*"; }
# 下载辅助（curl 优先，回退 wget）—— v1.5.6 修复：提前定义到 do_docker_deploy 之前
# 之前定义在 line 654（在 do_docker_deploy 的 line 472 调用之后才解析），
# 导致 bash 单遍解析时 do_docker_deploy 内调用 dl_or_exit 报 command not found
dl(){ # URL DEST
  if command -v curl >/dev/null; then curl -fsSL "$1" -o "$2"
  else wget -qO "$2" "$1"; fi
}
dl_or_exit(){ dl "$1" "$2" || { err "下载失败: $1"; exit 3; }; }

# ---- 参数解析 ----
usage(){ cat <<EOF
用法: bash install.sh [选项]
  --domain DOMAIN          你的域名（必填）
  --dynu-key KEY           Dynu API Key（路径A，推荐）
  --dynu-client-id ID      Dynu OAuth Client ID（路径B，与 --dynu-secret 同用）
  --dynu-secret SECRET     Dynu OAuth Secret（路径B）
  --email EMAIL            ACME 注册邮箱
  --ss-port N              Shadowsocks 端口（默认随机 10000-65535）
  --anytls-port N          AnyTLS 端口（默认随机 10000-65535）
  --naive-port N           NaiveProxy 端口（默认随机 10000-65535，勿用 443）
  --panel-port N           面板端口（默认随机 10000-65535）
  --panel-user USER        面板管理员用户名（默认 admin）
  --panel-password PASS    面板管理员密码（默认随机；自定义须 6-64 字符）
  --panel-url-path PATH    面板 URL 路径（默认随机 /xxxxxxxx/；自定义形如 /my-path/）
  --ss-password KEY        Shadowsocks 密钥（默认随机；须 base64(16字节)，用 'openssl rand -base64 16' 生成）
  --anytls-password PASS   AnyTLS 密码（默认随机）
  --anytls-uuid UUID       AnyTLS 用户 UUID（默认随机；标准 UUID 格式如 a1b2c3d4-e5f6-7890-abcd-ef1234567890）
  --naive-user USER        NaiveProxy 用户名（默认随机；不含冒号和空白）
  --naive-password PASS    NaiveProxy 密码（默认随机；不含冒号和空白）
  --disguise-panel VAL     :443 直访伪装站点（proxy:<URL> 反代 / page 默认页，默认 proxy:https://example.com）
  --disguise-naive VAL     NaiveProxy 端口的伪装站点（同上格式，默认 proxy:https://example.com）
  --cert-mode MODE         证书来源：acme（默认，用 Dynu DNS-01 自动签发）| manual（手动指定已有证书，跳过 Dynu/acme）
  --cert-fullchain PATH    manual 模式：证书文件完整绝对路径（如 /etc/letsencrypt/live/x.com/fullchain.pem）
  --cert-privkey PATH      manual 模式：私钥文件完整绝对路径（如 /etc/letsencrypt/live/x.com/privkey.pem）
  --docker                 用 ghcr.io all-in-one 镜像跑（KVM；否则裸金属）
  --no-caddy               不部署 caddy 的 :80/:443 站点（让已装的 nginx/其它 web 服务器接管；naive 仍可装但只听 naive 端口）
  --force-bin              强制从 Releases 重装 sing-box/caddy（已装则跳过）
  --non-interactive        不交互，缺项报错退出
  --landing                在落地机部署独立 ss-server（剩余参数 --port N 等透传给落地脚本）
  --uninstall              卸载 ANS-GO（自动检测 Docker/裸金属；保留配置/卷）
  --purge                  与 --uninstall 同用：彻底删除配置/密钥/证书/卷/镜像
  -h, --help
交互式运行（无参数）会显示主菜单：安装 / 卸载 / 彻底卸载 / 落地
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
    --panel-password) PANEL_PASSWORD_IN="$2"; shift 2;;
    --panel-url-path) URL_PATH_IN="$2"; shift 2;;
    --ss-password) SS_PASSWORD="$2"; shift 2;;
    --anytls-password) ANYTLS_PASSWORD="$2"; shift 2;;
    --anytls-uuid) ANYTLS_UUID_IN="$2"; shift 2;;
    --naive-user) NAIVE_USER_IN="$2"; shift 2;;
    --naive-password) NAIVE_PASSWORD="$2"; shift 2;;
    --disguise-panel) DISGUISE_PANEL="$2"; shift 2;;
    --disguise-naive) DISGUISE_NAIVE="$2"; shift 2;;
    --cert-mode) CERT_MODE="$2"; shift 2;;
    --cert-fullchain) CERT_FILE="$2"; shift 2;;
    --cert-privkey) KEY_FILE="$2"; shift 2;;
    --docker) DOCKER=1; shift;;
    --no-caddy) NO_CADDY=1; shift;;
    --force-bin) FORCE_BIN=1; shift;;
    --non-interactive) NONINT=1; shift;;
    # --landing：剩余参数（--port N 等）透传给 ansgo-landing.sh，不在此解析
    --landing) LANDING=1; shift; LANDING_ARGS=("$@"); break;;
    --uninstall) UNINSTALL=1; shift;;
    --purge) PURGE=1; shift;;
    -h|--help) usage; exit 0;;
    *) err "未知参数: $1"; usage; exit 1;;
  esac
done

# =============================================================================
# 参数校验（v1.5.5 新增）
#   仅对「带参数安装/部署」场景校验；--uninstall / --landing / --purge 跳过
# =============================================================================
validate_inputs(){
  local errs=0 port p

  # 端口范围校验（1-65535，整数）
  for p in SS_PORT ANYTLS_PORT NAIVE_PORT PANEL_PORT; do
    port="${!p:-}"
    [ -n "$port" ] || continue
    if ! [[ "$port" =~ ^[0-9]+$ ]] || [ "$port" -lt 1 ] || [ "$port" -gt 65535 ]; then
      err "$p=$port 不合法（须 1-65535 整数）"; errs=$((errs+1))
    fi
  done

  # 端口冲突校验：四个可配端口互相不得重复，且不得占用 caddy/SSH 固定端口
  #   80=caddy HTTP 跳转，443=caddy :443 伪装站，25822=SSH 加固后端口（如启用）
  #   v1.5.10: --no-caddy 模式下 caddy 不监听 80/443，故不保留这两个端口（让用户可自由用）
  local sys_ports="25822"
  [ "$NO_CADDY" = 0 ] && sys_ports="80 443 25822"
  local conf_ports=()
  for p in SS_PORT ANYTLS_PORT NAIVE_PORT PANEL_PORT; do
    port="${!p:-}"; [ -n "$port" ] || continue
    # 与系统固定端口冲突
    for s in $sys_ports; do
      if [ "$port" = "$s" ]; then err "$p=$port 与系统固定端口冲突（$s 为 caddy/SSH 保留）"; errs=$((errs+1)); fi
    done
    # 互相冲突
    local prev
    for prev in "${conf_ports[@]:-}"; do
      if [ "$port" = "$prev" ]; then err "$p=$port 与其它服务端口重复"; errs=$((errs+1)); fi
    done
    conf_ports+=("$port")
  done

  # SS2022 密钥：base64 解码后必须恰好 16 字节（aes-128-gcm）
  if [ -n "$SS_PASSWORD" ]; then
    local n
    n=$(printf '%s' "$SS_PASSWORD" | base64 -d 2>/dev/null | wc -c | tr -d ' ')
    if [ "$n" != "16" ]; then
      err "--ss-password 不是合法的 2022-blake3-aes-128-gcm 密钥（base64 解码后须 16 字节，当前 $n 字节）"
      err "  生成命令: openssl rand -base64 16"
      errs=$((errs+1))
    fi
  fi

  # AnyTLS UUID：标准 v4 格式（大小写不敏感）
  if [ -n "$ANYTLS_UUID_IN" ]; then
    if ! [[ "$ANYTLS_UUID_IN" =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]]; then
      err "--anytls-uuid 格式错误（须标准 UUID 如 a1b2c3d4-e5f6-7890-abcd-ef1234567890）"
      errs=$((errs+1))
    fi
  fi

  # NaiveProxy 用户名/密码：不含冒号和空白（caddy basic_auth 限制）
  # 注：用平行数组而非 "val:flag" 拼接，避免值本身含冒号被截断
  local naive_vals=("$NAIVE_USER_IN" "$NAIVE_PASSWORD")
  local naive_flags=("--naive-user" "--naive-password")
  local i
  for i in 0 1; do
    local val="${naive_vals[$i]}" flag="${naive_flags[$i]}"
    [ -n "$val" ] || continue
    if [[ "$val" == *:* ]] || [[ "$val" =~ [[:space:]] ]]; then
      err "$flag 含有非法字符（冒号或空白，caddy basic_auth 不支持）"; errs=$((errs+1))
    fi
  done

  # AnyTLS 密码：非空即可（与现有 Go 端一致，无格式约束）
  # NaiveProxy 已在上方校验

  # 面板密码：6-64 字符
  if [ -n "$PANEL_PASSWORD_IN" ]; then
    local pn=${#PANEL_PASSWORD_IN}
    if [ "$pn" -lt 6 ] || [ "$pn" -gt 64 ]; then
      err "--panel-password 长度须 6-64 字符（当前 ${pn}）"; errs=$((errs+1))
    fi
  fi

  # 面板 URL 路径：/xxxx/ 形式
  if [ -n "$URL_PATH_IN" ]; then
    if ! [[ "$URL_PATH_IN" =~ ^/[A-Za-z0-9_-]+/$ ]]; then
      err "--panel-url-path 格式错误（须 /xxxx/ 形式，仅含字母数字 _ -）"; errs=$((errs+1))
    fi
  fi

  if [ "$errs" -gt 0 ]; then
    err "参数校验失败（$errs 项），已列出详情。修正后重试。"
    exit 1
  fi
}
# 仅在「安装/部署」场景下校验；卸载/落地/帮助场景跳过
if [ "$UNINSTALL" = 0 ] && [ "$LANDING" = 0 ]; then
  validate_inputs
fi

# =============================================================================
# 随机端口填补（v1.5.12）
#   未通过 --ss-port/--anytls-port/--naive-port/--panel-port 指定的端口，
#   在 10000-65535 范围随机生成。避免以下端口：
#     - 80/443（caddy 固定端口，--no-caddy 模式下除外）
#     - 25822（SSH 加固后端口）
#     - 已被占用的端口（ss -tln 检测）
#     - 已为本部署其它服务选用的端口（互斥）
# =============================================================================
_rand_port_used=" "   # 本次部署已选端口（空格分隔），避免互相冲突
_sys_busy_ports(){ echo "25822"; [ "$NO_CADDY" = 0 ] && echo "80 443"; }
_listening_ports(){ ss -tln 2>/dev/null | awk 'NR>1{print $4}' | sed 's/.*://' | sort -u; }
rand_port(){
  # 在 10000-65535 随机生成一个未冲突端口
  local busy sys_used p
  busy="$( { _listening_ports; echo "$_rand_port_used" | tr ' ' '\n'; } | sort -u )"
  sys_used="$(_sys_busy_ports)"
  while :; do
    p=$(( (RANDOM << 15 | RANDOM) % 55536 + 10000 ))  # 10000-65535
    # 跳过系统保留 / 已监听 / 本次已选
    { echo "$sys_used" | grep -qx "$p"; } && continue
    { echo "$busy" | grep -qx "$p"; } && continue
    echo "$p"; return
  done
}
# 仅在「安装/部署」场景下填补；卸载/落地/帮助场景跳过
if [ "$UNINSTALL" = 0 ] && [ "$LANDING" = 0 ]; then
  [ -z "$SS_PORT" ]     && SS_PORT="$(rand_port)"     && _rand_port_used+="$SS_PORT "
  [ -z "$ANYTLS_PORT" ] && ANYTLS_PORT="$(rand_port)" && _rand_port_used+="$ANYTLS_PORT "
  [ -z "$NAIVE_PORT" ]  && NAIVE_PORT="$(rand_port)"  && _rand_port_used+="$NAIVE_PORT "
  [ -z "$PANEL_PORT" ]  && PANEL_PORT="$(rand_port)"  && _rand_port_used+="$PANEL_PORT "
fi

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
  printf "确认卸载？输入 yes: " >&2
  a="$(readtty)"
  [ "$a" = yes ] || { echo "已取消"; exit 0; }

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
# 注：首次执行时 /etc/ansgo-deploy/ansgo-landing.sh 尚未下载，需先从仓库拉取
do_landing(){
  [ "$(id -u)" = 0 ] || { err "需 root 运行"; exit 1; }
  mkdir -p /etc/ansgo-deploy
  local LSCRIPT=/etc/ansgo-deploy/ansgo-landing.sh
  if [ ! -f "$LSCRIPT" ]; then
    log "下载 ansgo-landing.sh ..."
    if command -v curl >/dev/null; then curl -fsSL "$RAW/ansgo-landing.sh" -o "$LSCRIPT"
    else wget -qO "$LSCRIPT" "$RAW/ansgo-landing.sh"; fi
    [ -s "$LSCRIPT" ] || { err "下载 ansgo-landing.sh 失败（$RAW/ansgo-landing.sh）"; exit 3; }
    chmod 0755 "$LSCRIPT"
  fi
  # 透传 --landing 之后剩余的参数（--port / --non-interactive 等）
  bash "$LSCRIPT" "${LANDING_ARGS[@]}"
  exit $?
}
if [ "${LANDING:-0}" = 1 ]; then do_landing; exit 0; fi

# ---- 交互收集 ----
ask(){ # prompt default -> 读入到 ASK_VAL
  local p="$1" d="${2:-}" v
  if [ "${NONINT:-0}" = 1 ]; then ASK_VAL="$d"; return; fi
  if [ -n "$d" ]; then printf "%s [%s]: " "$p" "$d" >&2
  else printf "%s: " "$p" >&2; fi
  v="$(readtty)"
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

  # host 网络端口冲突预警（v1.5.10: --no-caddy 模式下 80/443 不再预警，由 nginx 等接管）
  WARN_PORTS="$PANEL_PORT"
  [ "$NO_CADDY" = 0 ] && WARN_PORTS="80 443 $PANEL_PORT"
  for p in $WARN_PORTS; do
    if command -v ss >/dev/null && ss -tln 2>/dev/null | awk '{print $4}' | grep -qE ":$p$"; then
      warn "端口 $p 已被占用，host 网络下容器可能启动失败，请先停掉占用进程"
    fi
  done

  local DIR=/etc/ansgo-docker
  mkdir -p "$DIR" || { err "无法创建 $DIR"; exit 1; }

  # 随机面板密码 + URL 路径（仅首次部署生效；卷已存在时以容器内实际为准）
  # v1.5.5: 支持 --panel-password / --panel-url-path 指定
  local PANEL_PW URL_PATH
  PANEL_PW="${PANEL_PASSWORD_IN:-$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)}"
  URL_PATH="${URL_PATH_IN:-/$(openssl rand -hex 4)/}"

  # ansgo.env：凭证 + 端口 + 用户预指定密钥（600 权限，敏感不入 git）
  # 注：代理服务密钥（SS_KEY_IN 等）用 _IN 后缀避免与容器内同名变量冲突；
  #     entrypoint.sh 用 ${VAR:-随机} 接收，留空则在容器内随机生成。
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
SS_KEY_IN=${SS_PASSWORD}
ANYTLS_PASS_IN=${ANYTLS_PASSWORD}
ANYTLS_UUID_IN=${ANYTLS_UUID_IN}
NAIVE_USER_IN=${NAIVE_USER_IN}
NAIVE_PASS_IN=${NAIVE_PASSWORD}
NO_CADDY=${NO_CADDY}
EOF
  chmod 600 "$DIR/ansgo.env"
  log "已生成 $DIR/ansgo.env（含凭证，权限 600）"

  dl_or_exit "$RAW/docker-compose.yml" "$DIR/docker-compose.yml"

  # v1.5.10: manual 证书模式——由 entrypoint.sh 在容器启动时把宿主证书
  #   bind mount 进来并同步到 /etc/ssl/ansgo/ 卷。这里不再注入 docker-compose
  #   volumes（v1.5.10 的 bind mount 方案有 SELinux/权限问题，实测失败）。
  #   新方案：install.sh 把 manual 证书路径写到 ansgo.env（CERT_FULLCHAIN/CERT_PRIVKEY），
  #   docker-compose.yml 增加只读 bind mount（下方注入），entrypoint 启动时
  #   读 CERT_FULLCHAIN，cp 到 /etc/ssl/ansgo/，genconf 用 acme 路径。
  if [ "$CERT_MODE" = "manual" ] && [ -n "$CERT_FILE" ] && [ -n "$KEY_FILE" ]; then
    CERT_DIR_HOST=$(dirname "$CERT_FILE")
    KEY_DIR_HOST=$(dirname "$KEY_FILE")
    log "manual 证书模式：宿主证书目录 bind mount → entrypoint 启动时同步到 /etc/ssl/ansgo/"
    log "  续期后操作: docker exec ansgo ansgo-sync-manual-cert && docker exec ansgo ansgo-cert-reload"
    # 去重收集要挂载的目录（cert 和 key 常在同目录）
    declare -A MOUNT_DIRS=()
    for d in "$CERT_DIR_HOST" "$KEY_DIR_HOST"; do
      [ -d "$d" ] && MOUNT_DIRS["$d"]=1
    done
    # 在 volumes 段追加 bind mount（entrypoint 会从此路径 cp 到 /etc/ssl/ansgo/）
    for d in "${!MOUNT_DIRS[@]}"; do
      awk -v dir="$d" '
        /- ansgo_acme:\/root\/\.acme\.sh/ {
          print
          printf "      - %s:%s:ro\n", dir, dir
          next
        }
        {print}
      ' "$DIR/docker-compose.yml" > "$DIR/docker-compose.yml.tmp" && mv "$DIR/docker-compose.yml.tmp" "$DIR/docker-compose.yml"
    done
  fi

  cd "$DIR" || exit 1

  # v1.5.10 修复：检测 docker compose v2（子命令）vs docker-compose v1（独立二进制）
  #   宝塔/老 Debian 可能只有 v1（docker-compose），没有 v2（docker compose 子命令）
  #   之前用 `docker compose pull 2>/dev/null` 吞掉 stderr，导致 v2 不存在时静默失败 → 误走本地构建
  COMPOSE=""
  if docker compose version >/dev/null 2>&1; then
    COMPOSE="docker compose"
  elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE="docker-compose"
  else
    err "未找到 docker compose（v2 子命令）也未找到 docker-compose（v1）。请安装 docker compose plugin 或 docker-compose。"
    err "  Debian/Ubuntu: apt-get install docker-compose-plugin 或 curl -L https://github.com/docker/compose/releases/latest/download/docker-compose-$(uname -s)-$(uname -m) -o /usr/local/bin/docker-compose && chmod +x /usr/local/bin/docker-compose"
    exit 8
  fi
  log "使用 compose 命令: $COMPOSE ($($COMPOSE version --short 2>/dev/null || echo unknown))"

  # 拉镜像：优先 compose pull；失败再用 docker pull 兜底（绕过 compose 自身问题）
  #   v1.5.10 已发布公开镜像 ghcr.io/jiasongji/ansgo:latest（amd64+arm64）
  IMG="ghcr.io/${REPO%/*}/ansgo:latest"
  if ! $COMPOSE pull 2>&1 | tail -5; then
    warn "$COMPOSE pull 失败，尝试直接 docker pull $IMG 兜底"
    if ! docker pull "$IMG" 2>&1 | tail -10; then
      err "docker pull $IMG 失败（网络问题？）。可手动："
      err "  1. docker pull $IMG（检查错误）"
      err "  2. 或回退本地构建：git clone https://github.com/${REPO} && docker build -f deploy/Dockerfile.allinone ."
      exit 6
    fi
    log "docker pull 成功（绕过 compose 问题）"
  fi

  $COMPOSE up -d && log "容器已启动" || { err "$COMPOSE up 失败"; exit 7; }
  sleep 5

  # 读取容器内实际 url_path（卷已存在时可能与此处生成的不一致）
  local ACTUAL_PATH
  ACTUAL_PATH=$(docker exec ansgo python3 -c 'import json;print(json.load(open("/etc/ansgo/panel.json")).get("url_path","/"))' 2>/dev/null || echo "$URL_PATH")

  hr "部署完成"
  cat <<EOF

╔═══════════════════════════════════════════════════════════════╗
║  ✅ ANS-GO 一体化部署完成（Docker / KVM）                     ║
╠═══════════════════════════════════════════════════════════════╣
║                                                               ║
║  ⚠️  本次部署端口均为随机生成，请立即记录！                   ║
║  ─────────────────────────────────────────────────            ║
║                                                               ║
║  Web 面板:   https://${DOMAIN}:${PANEL_PORT}${ACTUAL_PATH}
║  用户名:     ${PANEL_USER}
║  密码(首次): ${PANEL_PW}
║                                                               ║
║  ──────── 代理服务端口（登录面板后启用） ────────             ║
║   • Shadowsocks : ${SS_PORT}
║   • AnyTLS      : ${ANYTLS_PORT}
║   • NaiveProxy  : ${NAIVE_PORT}
║   • 管理面板    : ${PANEL_PORT}
║                                                               ║
╠═══════════════════════════════════════════════════════════════╣
║  ⚠️ 证书: $([ "$CERT_MODE" = "manual" ] && echo "手动模式（${CERT_FILE}），无需后台签发" || echo "Let's Encrypt 后台签发中（约 1-3 分钟），首次访问若提示不受信稍候刷新即可")
║  管理命令:
║     cd ${DIR} && $COMPOSE logs -f ansgo   # 查日志/签证书进度
║     docker exec ansgo ansgo-admin status         # 服务状态
║     docker exec ansgo ansgo-admin info           # 节点连接参数
║  下一步: 登录面板 →「服务管理」页按需开启代理服务
╚═══════════════════════════════════════════════════════════════╝
EOF
  log "代理服务默认未启动，请在面板「服务管理」页按需开启。"
  exit 0
}

hr "ANS-GO 一键部署"

# ---- 交互式主菜单（仅在交互模式且未通过参数指定动作时显示）----
# 设计：用户 bash <(curl ...) 无参数进入时，先让其选择 安装/卸载/彻底卸载/落地。
#   参数式调用（--uninstall/--purge/--landing/--non-interactive 或任意部署参数）跳过菜单。
if [ "${NONINT:-0}" = 0 ] && [ -z "${DOMAIN:-}" ] && [ -c /dev/tty ]; then
  printf "\n请选择操作：\n"
  printf "  1) 安装 / 部署管理面板（默认）\n"
  printf "  2) 卸载（移除服务/容器/二进制，保留配置/卷，可重装不丢参数）\n"
  printf "  3) 彻底卸载（删除配置/密钥/证书/卷/镜像/调优，不可恢复）\n"
  printf "  4) 部署落地服务器（独立 ss-server，供中转机第二组接入）\n"
  printf "请选择 [1]: " >&2
  _choice="$(readtty)"
  case "$_choice" in
    2) UNINSTALL=1; PURGE=0; do_uninstall; exit 0;;
    3) UNINSTALL=1; PURGE=1; do_uninstall; exit 0;;
    4) LANDING=1;;
    ""|1|*) ;;  # 继续部署流程
  esac
  unset _choice
fi

# 菜单选了落地（或 --landing 参数）→ 进入落地部署
if [ "${LANDING:-0}" = 1 ]; then do_landing; exit 0; fi

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
  ask "Shadowsocks 端口（回车=随机）" "$SS_PORT"; SS_PORT="${ASK_VAL:-$SS_PORT}"
  ask "AnyTLS 端口（回车=随机）" "$ANYTLS_PORT"; ANYTLS_PORT="${ASK_VAL:-$ANYTLS_PORT}"
  ask "NaiveProxy 端口（回车=随机）" "$NAIVE_PORT"; NAIVE_PORT="${ASK_VAL:-$NAIVE_PORT}"
  ask "面板端口（回车=随机）" "$PANEL_PORT"; PANEL_PORT="${ASK_VAL:-$PANEL_PORT}"
  ask "面板管理员用户名" "$PANEL_USER"; PANEL_USER="${ASK_VAL:-$PANEL_USER}"
  ask "直访伪装 :443 (proxy:<URL> 或 page)" "$DISGUISE_PANEL"; DISGUISE_PANEL="${ASK_VAL:-$DISGUISE_PANEL}"
  ask "Naive伪装 (代理端口, proxy:<URL> 或 page)" "$DISGUISE_NAIVE"; DISGUISE_NAIVE="${ASK_VAL:-$DISGUISE_NAIVE}"
  # v1.5.10: 检测 80/443 占用，提示是否跳过 caddy（适配已有 nginx 的服务器）
  if [ "$NO_CADDY" = 0 ] && command -v ss >/dev/null 2>&1; then
    PORT_BUSY=""
    for p in 80 443; do
      ss -tln 2>/dev/null | awk '{print $4}' | grep -qE ":$p$" && PORT_BUSY="$PORT_BUSY $p"
    done
    if [ -n "$PORT_BUSY" ]; then
      warn "检测到端口$PORT_BUSY 已被占用（可能已有 nginx/Caddy/宝塔等 web 服务器）"
      ask "是否跳过 caddy 部署让现有 web 服务器接管 80/443？(y/n)" "y"
      [ "${ASK_VAL:-y}" = "y" ] && NO_CADDY=1
    fi
  fi
fi
echo "----------------------------------------"
printf "  域名        : %s\n" "$DOMAIN"
printf "  ⚠️ 端口（随机生成，请记下）：\n"
printf "    ss=%s anytls=%s naive=%s panel=%s\n" "$SS_PORT" "$ANYTLS_PORT" "$NAIVE_PORT" "$PANEL_PORT"
printf "  直访伪装   : %s\n" "$DISGUISE_PANEL"
printf "  Naive伪装  : %s\n" "$DISGUISE_NAIVE"
if [ "$CERT_MODE" = "manual" ]; then
  printf "  证书来源    : 手动（%s）\n" "$CERT_FILE"
else
  printf "  证书来源    : acme（Dynu %s）\n" "${DYNU_KEY:+路径A(API Key)}${DYNU_KEY:-${DYNU_CID:+路径B(OAuth)}}"
fi
printf "  面板模式    : %s\n" "$([ "$DOCKER" = 1 ] && echo Docker || echo 裸金属)"
[ "$NO_CADDY" = 1 ] && printf "  caddy       : 跳过（--no-caddy，不监听 80/443，让现有 web 服务器接管）\n"
echo "----------------------------------------"
if [ "$NONINT" = 0 ]; then
  printf "确认开始部署？[Y/n] " >&2
  c="$(readtty)"
  [ "${c:-Y}" = "Y" ] || [ "${c:-Y}" = "y" ] || { warn "已取消"; exit 0; }
fi

# ---- Docker 一体化分流（KVM 主机推荐）：容器内跑全部服务，这里完成后即退出 ----
if [ "$DOCKER" = 1 ]; then
  do_docker_deploy
fi

# ---- 下载助手已提前在文件顶部定义（line 112 附近，v1.5.10 修复）----

hr "1/8 下载部署脚本（本仓库 raw）"
mkdir -p /etc/ansgo /etc/ansgo-deploy
for f in ansgo-admin ansgo-genconf ansgo-cert-reload ansgo-cert-issue.sh dns_dynukey.sh; do
  log "下载 $f"; dl_or_exit "$RAW/$f" "/etc/ansgo-deploy/$f"
done
install -m 0755 /etc/ansgo-deploy/ansgo-admin       /usr/local/bin/ansgo-admin
install -m 0755 /etc/ansgo-deploy/ansgo-genconf     /usr/local/bin/ansgo-genconf
install -m 0755 /etc/ansgo-deploy/ansgo-cert-reload /usr/local/bin/ansgo-cert-reload

hr "2/8 确保 sing-box / caddy(naive) 就位"
# 预创建服务配置目录（与 Dockerfile.allinone / entrypoint.sh 一致）。
# ansgo-genconf 写 /etc/sing-box/config.json 与 /etc/caddy/Caddyfile 时
# 不会自动建父目录（早期版本），旧部署或全新机首次安装可能缺失 → 面板
# 「服务安装」时报 FileNotFoundError。这里 mkdir 兜底，genconf 也加了
# os.makedirs 二次保险（v1.5.4）。
mkdir -p /etc/sing-box /etc/caddy /var/www/html
# sing-box：优先 SagerNet 官方 release（与 Dockerfile.allinone / ansgo-admin 一致），
#   回退本项目 release vendored 产物。官方格式：sing-box-<ver>-linux-<arch>.tar.gz
SB_VER="1.13.13"
if [ "$FORCE_BIN" = 1 ] || ! command -v sing-box >/dev/null; then
  log "安装 sing-box v${SB_VER} (arch=$AARCH)"
  SB_OK=0
  # 源1: SagerNet 官方 release（稳定，多架构）
  if dl "https://github.com/SagerNet/sing-box/releases/download/v${SB_VER}/sing-box-${SB_VER}-linux-${AARCH}.tar.gz" /tmp/sb.tgz; then
    log "  → 从 SagerNet 官方下载成功"
    SB_OK=1
  # 源2: 本项目 release vendored（兜底）
  elif dl "$REL/$VER/sing-box-linux-${AARCH}.tar.gz" /tmp/sb.tgz; then
    log "  → 从本项目 release 下载成功"
    SB_OK=1
  fi
  if [ "$SB_OK" = 1 ] && tar xzf /tmp/sb.tgz -C /tmp 2>/dev/null && [ -f /tmp/sing-box-*/sing-box ]; then
    install -m 0755 /tmp/sing-box-*/sing-box /usr/local/bin/sing-box
  else
    err "sing-box 下载失败（SagerNet 官方 + 本项目 release 均不可用）"
    err "可手动下载后放到 /usr/local/bin/sing-box 再重跑，或检查网络/代理"
    exit 3
  fi
fi

# caddy-naive：klzgrad/forwardproxy 只发源码，需本项目预编译产物或现场 xcaddy 编译
#   源1: 本项目 release 预编译产物（推荐，免编译，秒级）
#   源2: 现场 xcaddy 编译（需 Go 1.22+ + git，约 3-5 分钟）
#        注意：Debian 12 apt 的 golang-go 是 1.19，xcaddy@latest 的 go.mod 用了
#        toolchain 指令（需 Go 1.21+），故必须用 Go 官方二进制，不能 apt 装。
if [ "$FORCE_BIN" = 1 ] || ! command -v caddy >/dev/null || ! caddy list-modules 2>/dev/null | grep -q forwardproxy; then
  log "安装 caddy (naive 分支, arch=$AARCH)"
  if dl "$REL/$VER/caddy-naive-linux-${AARCH}" /usr/local/bin/caddy 2>/dev/null && [ -s /usr/local/bin/caddy ]; then
    chmod 0755 /usr/local/bin/caddy
    log "  → 从本项目 release 下载成功"
  else
    # 回退：现场用 xcaddy 编译
    warn "本项目 release 无 caddy-naive 预编译产物，改用 xcaddy 现场编译（约 3-5 分钟）"
    command -v git >/dev/null 2>&1 || { apt-get update && apt-get install -y git; }

    # 确保 Go 版本 >= 1.21（xcaddy go.mod 用 toolchain 指令，旧版 Go 报 unknown directive）
    # 优先用已装的 go；版本不足或缺失则装 Go 官方二进制（apt 的 1.19 不够新）
    GO_BIN=""
    if command -v go >/dev/null 2>&1; then
      # go version 输出 "go version go1.22.5 ..."，提取 minor 版本号（22）判断 >= 21
      GO_MINOR=$(go version 2>/dev/null | grep -oE 'go[0-9]+\.[0-9]+' | head -1 | sed 's/go[0-9]*\.//')
      [ "${GO_MINOR:-0}" -ge 21 ] 2>/dev/null && GO_BIN="$(command -v go)"
    fi
    if [ -z "$GO_BIN" ]; then
      log "当前无 Go 或版本 < 1.21，安装 Go 官方二进制 ..."
      GO_INSTALL_VER="1.22.5"
      case "$AARCH" in amd64) GO_ARCH=amd64;; arm64) GO_ARCH=arm64;; *) GO_ARCH=amd64;; esac
      if dl "https://go.dev/dl/go${GO_INSTALL_VER}.linux-${GO_ARCH}.tar.gz" /tmp/go.tgz; then
        rm -rf /usr/local/go && tar xzf /tmp/go.tgz -C /usr/local
        GO_BIN="/usr/local/go/bin/go"
        export PATH="/usr/local/go/bin:$PATH"
      else
        err "Go 官方二进制下载失败，无法编译 caddy-naive。请确保 release 有预编译产物"
        exit 3
      fi
    fi

    rm -rf /tmp/caddy-naive-build && mkdir -p /tmp/caddy-naive-build
    if ( export PATH="/usr/local/go/bin:$PATH" GOPATH="${GOPATH:-/root/go}"; \
         cd /tmp/caddy-naive-build && \
         "$GO_BIN" install github.com/caddyserver/xcaddy/cmd/xcaddy@latest && \
         git clone -b naive --depth 1 https://github.com/klzgrad/forwardproxy.git /tmp/caddy-naive-build/fp && \
         CGO_ENABLED=0 "${GOPATH:-/root/go}/bin/xcaddy" build \
           --with github.com/caddyserver/forwardproxy=/tmp/caddy-naive-build/fp \
           --output /usr/local/bin/caddy ); then
      chmod 0755 /usr/local/bin/caddy
      log "  → xcaddy 编译成功"
    else
      err "caddy-naive 编译失败（需 Go 1.22+ + git + 网络）。可手动编译后放到 /usr/local/bin/caddy"
      exit 3
    fi
  fi
fi

hr "3/8 生成协议密钥 + 面板配置"
# secrets.env（如已存在则保留，避免覆盖现网密钥）
# v1.5.5: 支持用户通过 --ss-password / --anytls-password / --anytls-uuid
#         / --naive-user / --naive-password 预先指定密钥；未指定的随机生成。
if [ ! -f /etc/ansgo/secrets.env ]; then
  # 用 ${VAR:-默认} 模式：用户值优先，留空则随机
  cat > /etc/ansgo/secrets.env <<EOF
SS_METHOD=2022-blake3-aes-128-gcm
SS_KEY=${SS_PASSWORD:-$(openssl rand -base64 16)}
ANYTLS_PASS=${ANYTLS_PASSWORD:-$(openssl rand -hex 16)}
ANYTLS_UUID=${ANYTLS_UUID_IN:-$(cat /proc/sys/kernel/random/uuid)}
NAIVE_USER=${NAIVE_USER_IN:-$(openssl rand -hex 6)}
NAIVE_PASS=${NAIVE_PASSWORD:-$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)}
EOF
  log "已生成 /etc/ansgo/secrets.env"
  # 提示哪些字段用了用户提供的值（不会打印值本身，仅提示字段名）
  [ -n "$SS_PASSWORD$ANYTLS_PASSWORD$ANYTLS_UUID_IN$NAIVE_USER_IN$NAIVE_PASSWORD" ] && \
    inf "  其中部分密钥由 CLI 参数指定（详见各 --xxx-password 参数）"
else warn "/etc/ansgo/secrets.env 已存在，保留现有密钥"; fi
chmod 600 /etc/ansgo/secrets.env

# panel.json（端口/设置/域名/伪装）
# v1.5.5: URL 路径支持 --panel-url-path 指定，否则随机
URLPATH="${URL_PATH_IN:-/$(openssl rand -hex 4)/}"
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
  "caddy_enable": "$([ "$NO_CADDY" = 1 ] && echo false || echo true)",
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
# 初始化占位配置（genconf 已自建父目录，v1.5.4）。失败不阻塞部署——
# 代理服务本就是面板内按需安装，占位配置缺失不影响面板/caddy 起来。
ansgo-genconf all 2>&1 | tail -3 || warn "ansgo-genconf 初始化失败（不阻塞，面板内装服务时会再调）"

hr "6/7 部署 systemd unit + 启动面板与伪装站"
# sing-box / caddy 的 unit（面板安装服务时会 enable）
for s in sing-box caddy; do
  if [ ! -f /etc/systemd/system/$s.service ]; then
    dl "$RAW/systemd/$s.service" /etc/systemd/system/$s.service 2>/dev/null && log "已安装 $s.service"
  fi
done
systemctl daemon-reload
# caddy 启动（:443 伪装站 + :80 跳转 是域名直访体验的一部分，随面板一起起来）
# v1.5.10: --no-caddy 模式下不启动 caddy（让现有 nginx 等 web 服务器接管 80/443）。
#   caddy 二进制和 unit 仍安装（面板后续装 naive 时可手动 enable/restart caddy 只听 naive 端口）。
if [ "$NO_CADDY" = 1 ]; then
  log "跳过 caddy 启动（--no-caddy 模式；80/443 由现有 web 服务器接管）"
else
  systemctl enable caddy >/dev/null 2>&1 || true
  systemctl restart caddy && log "caddy 已启动（:443 伪装站）"
fi
# sing-box 暂不启动（无代理服务安装前不需要）

hr "7/7 部署 Web 管理面板"
log "下载 ansgo-panel 二进制 (from release)"
dl_or_exit "$REL/$VER/ansgo-panel-linux-${AARCH}" /usr/local/bin/ansgo-panel
chmod 0755 /usr/local/bin/ansgo-panel
dl_or_exit "$RAW/ansgo-panel.service" /etc/systemd/system/ansgo-panel.service
systemctl daemon-reload
# 设置管理员密码（bcrypt 写入 panel.json，打印明文仅一次）
# v1.5.5: 支持 --panel-password 指定，否则随机
PANEL_PW="${PANEL_PASSWORD_IN:-$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)}"
/usr/local/bin/ansgo-panel -setpass "$PANEL_PW" 2>/dev/null || true
systemctl enable ansgo-panel >/dev/null 2>&1 || true
systemctl restart ansgo-panel && log "ansgo-panel 已启动"
sleep 2

# ---- 输出最终信息 ----
hr "部署完成"
URL_PATH=$(python3 -c "import json;print(json.load(open('/etc/ansgo/panel.json'))['url_path'])" 2>/dev/null)
cat <<EOF

╔═══════════════════════════════════════════════════════════════╗
║  ✅ ANS-GO 管理面板部署完成                                   ║
╠═══════════════════════════════════════════════════════════════╣
║                                                               ║
║  ⚠️  本次部署端口均为随机生成，请立即记录！                   ║
║  ─────────────────────────────────────────────────            ║
║                                                               ║
║  Web 面板:   https://${DOMAIN}:${PANEL_PORT}${URL_PATH:-/}
║  用户名:     ${PANEL_USER}
║  密码(仅此一次): ${PANEL_PW}
║                                                               ║
║  ──────── 代理服务端口（登录面板后启用） ────────             ║
║   • Shadowsocks : ${SS_PORT}
║   • AnyTLS      : ${ANYTLS_PORT}
║   • NaiveProxy  : ${NAIVE_PORT}
║   • 管理面板    : ${PANEL_PORT}
║                                                               ║
╠═══════════════════════════════════════════════════════════════╣
║  下一步：登录面板 →「服务管理」页，按需安装代理服务           ║
║           落地服务（AnyTLS-2/NaiveProxy-2 + 远端 SS）在        ║
║           「落地服务」页（原「第二组服务」+「出口落地」合并）  ║
╚═══════════════════════════════════════════════════════════════╝
EOF
log "代理服务未启动，请在面板「服务管理」页按需开启。"
