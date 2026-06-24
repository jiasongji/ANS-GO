#!/usr/bin/env bash
# =============================================================================
# ANS-GO 一键升级脚本 (upgrade.sh)   v1.5.20
#
# 把任意已部署旧版本的 ANS-GO 服务器升级到当前版本（裸金属 / Docker 自动识别）。
# 幂等可重复执行，每次升级自动备份，SOCKS5 默认不启用（符合「面板内按需装服务」架构）。
#
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/upgrade.sh | bash
#   bash upgrade.sh [--docker | --metal] [--ver v1.5.20] [--yes]
#
# 设计要点（与 install.sh / ansgo-admin 保持一致）：
#   - VER 与 install.sh 顶部硬编码一致，发新版只需改这一行 + commit
#   - 复用 install.sh 的 bootstrap（解决 curl|bash 的 SIGPIPE/进程替换卡死）
#   - panel 二进制无 -version flag（main.go 仅 -setpass），用 md5 对比判断是否真更新，
#     启动后用 journalctl 日志行 "ansgo-panel v1.5.20 监听..." 验证版本
#   - 裸金属 panel 替换走 .new→md5→.bak→mv→restart 安全流程（AGENTS.md §9 铁律）
#   - 备份目录命名 /etc/ansgo-backup-upgrade-{TS}，遵循 ansgo-admin 约定
# =============================================================================
# ---- bootstrap：把脚本完整落地后再执行（解决 curl|bash 的两个问题，移植自 install.sh） ----
# 问题1 SIGPIPE：curl ... | bash 下脚本中途 exit 让 bash 提前结束 → 管道读端关闭 →
#   curl 写剩余字节收到 SIGPIPE → 报 (23)。
# 问题2 进程替换卡死：bash <(curl ...) 下 fd0 是用户终端，误用 cat（无参数）会吃终端输入。
# 修复：检测管道/进程替换运行 → 落地临时文件 → exec 重跑。
_ansi_self="${BASH_SOURCE[0]:-}"
case "$_ansi_self" in
  "" | /dev/fd/* | /proc/self/fd/*)
    _ansi_tmp="$(mktemp 2>/dev/null || mktemp -t ansgo-upgrade)"
    if [ -n "$_ansi_self" ] && [ -e "$_ansi_self" ]; then
      cat "$_ansi_self" > "$_ansi_tmp" 2>/dev/null || cp "$_ansi_self" "$_ansi_tmp" 2>/dev/null
    else
      cat > "$_ansi_tmp" 2>/dev/null
    fi
    if [ -s "$_ansi_tmp" ]; then
      chmod +rx "$_ansi_tmp" 2>/dev/null
      export _ANSGO_UPGRADE_BOOTSTRAP_TMP="$_ansi_tmp"
      exec bash "$_ansi_tmp" "$@"
    fi
    rm -f "$_ansi_tmp" 2>/dev/null
    ;;
esac
unset _ansi_self _ansi_tmp
# ---- bootstrap end ----

set -uo pipefail

if [ -n "${_ANSGO_UPGRADE_BOOTSTRAP_TMP:-}" ] && [ -f "$_ANSGO_UPGRADE_BOOTSTRAP_TMP" ]; then
  trap 'rm -f "$_ANSGO_UPGRADE_BOOTSTRAP_TMP" 2>/dev/null' EXIT
fi

# ---- 版本号与资源源（与 install.sh 顶部一致）----
REPO="jiasongji/ANS-GO"
RAW="https://raw.githubusercontent.com/${REPO}/main/deploy"
REL="https://github.com/${REPO}/releases/download"
VER="v1.5.20"         # 升级目标版本（发新版只改这一行）

# 架构映射（uname -m -> release 二进制后缀）
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)        AARCH=amd64 ;;
  aarch64|arm64) AARCH=arm64 ;;
  *)             AARCH=amd64 ;;
esac

# ---- 固定路径（与 install.sh / ansgo-admin 一致）----
METAL_CONF="/etc/ansgo/panel.json"
METAL_SECRETS="/etc/ansgo/secrets.env"
METAL_PANEL_BIN="/usr/local/bin/ansgo-panel"
METAL_GENCONF="/usr/local/bin/ansgo-genconf"
METAL_ADMIN="/usr/local/bin/ansgo-admin"
DOCKER_DIR="/etc/ansgo-docker"
DOCKER_COMPOSE_FILE="${DOCKER_DIR}/docker-compose.yml"
BACKUP_ROOT="/etc"

# ---- 参数 ----
FORCE_MODE=""     # "" | docker | metal
ASSUME_YES=0

# ---- 颜色/日志（移植自 install.sh）----
if [ -t 1 ]; then C_G='\033[32m';C_Y='\033[33m';C_R='\033[31m';C_B='\033[36m';C_0='\033[0m'; else C_G='';C_Y='';C_R='';C_B='';C_0=''; fi
log(){  printf "${C_G}[*]${C_0} %s\n" "$*"; }
inf(){  printf "${C_B}[i]${C_0} %s\n" "$*"; }
warn(){ printf "${C_Y}[!]${C_0} %s\n" "$*" >&2; }
err(){  printf "${C_R}[X]${C_0} %s\n" "$*" >&2; }
ok(){   printf "${C_G}[✓]${C_0} %s\n" "$*"; }
hr(){   printf "\n${C_B}═══ %s ═══${C_0}\n" "$*"; }

readtty(){
  local line=""
  if [ -c /dev/tty ]; then
    line=$(read -r _LINE </dev/tty 2>/dev/null && printf '%s' "$_LINE") || line=""
  fi
  printf '%s' "$line"
}

dl(){ # URL DEST —— curl 优先 wget 兜底
  if command -v curl >/dev/null; then curl -fsSL "$1" -o "$2"
  else wget -qO "$2" "$1"; fi
}
dl_or_exit(){ dl "$1" "$2" || { err "下载失败: $1"; exit 3; }; }

usage(){ cat <<EOF
用法: bash upgrade.sh [选项]
  把已部署的 ANS-GO 服务器升级到 ${VER}（裸金属 / Docker 自动识别）。

选项:
  --docker        强制走 Docker 升级路径（仅拉镜像重建容器）
  --metal         强制走裸金属升级路径（更新 3 组件 + 补配置字段）
  --ver TAG       覆盖目标版本号（默认 ${VER}，一般不需要）
  --yes, -y       跳过升级前确认
  --help, -h      显示本帮助

示例:
  curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/upgrade.sh | bash
  bash upgrade.sh --metal --yes
  bash upgrade.sh --docker --yes

升级内容（${VER}）:
  - 仪表盘精简：移除管理面板卡片，状态改为顶栏用户名旁圆点显示
  - AnyTLS-2 管理集中到「落地服务」页（密码/检测/远端 SS 统一管理）
  - 服务管理页移除 AnyTLS-2 卡片（避免与落地服务页重复）

注意:
  - SOCKS5 升级后默认不启用（svc_socks_enabled=false），需登录面板「服务管理」页
    点「安装」或 SSH 执行 ansgo-admin regen socks 才会启用。
  - 升级前自动备份到 /etc/ansgo-backup-upgrade-{时间戳}/。
EOF
}

# ---- 参数解析 ----
while [ $# -gt 0 ]; do
  case "$1" in
    --docker)
      [ "$FORCE_MODE" = metal ] && { err "--docker 与 --metal 互斥"; exit 1; }
      FORCE_MODE="docker"; shift;;
    --metal)
      [ "$FORCE_MODE" = docker ] && { err "--docker 与 --metal 互斥"; exit 1; }
      FORCE_MODE="metal"; shift;;
    --ver)      VER="${2:-}"; [ -z "$VER" ] && { err "--ver 需要参数"; exit 1; }; shift 2;;
    --yes|-y)   ASSUME_YES=1; shift;;
    --help|-h)  usage; exit 0;;
    *) err "未知参数: $1（用 --help 查看用法）"; exit 1;;
  esac
done

# 去掉 VER 前缀 v 用于日志展示
VER_NUM="${VER#v}"

# ---- 形态检测（复用 install.sh:343-346 双探针）----
detect_mode(){
  local has_docker=0 has_metal=0
  [ -f "$DOCKER_COMPOSE_FILE" ] && has_docker=1
  { [ -f "$METAL_CONF" ] || [ -x "$METAL_PANEL_BIN" ] \
    || systemctl list-unit-files 2>/dev/null | grep -qE 'ansgo-panel|sing-box|caddy'; } && has_metal=1

  if [ -n "$FORCE_MODE" ]; then
    echo "$FORCE_MODE"
  elif [ "$has_docker" = 1 ] && [ "$has_metal" = 1 ]; then
    echo "ambiguous"
  elif [ "$has_docker" = 1 ]; then
    echo "docker"
  elif [ "$has_metal" = 1 ]; then
    echo "metal"
  else
    echo "none"
  fi
}

# 检测 compose 命令（v2 子命令优先，v1 独立二进制兜底；移植自 install.sh:568-577）
detect_compose(){
  if docker compose version >/dev/null 2>&1; then echo "docker compose"
  elif command -v docker-compose >/dev/null 2>&1; then echo "docker-compose"
  else echo ""; fi
}

# 磁盘空间检查（MB），不足给出警告（升级需要拉取 panel 二进制约 15MB + 解压空间）
check_disk(){
  local avail_mb
  avail_mb=$(df -m / 2>/dev/null | awk 'NR==2{print $4}')
  if [ -n "${avail_mb:-}" ] && [ "$avail_mb" -lt 200 ] 2>/dev/null; then
    warn "磁盘可用空间仅 ${avail_mb}MB（<200MB），升级可能失败。建议先清理。"
  fi
}

# =============================================================================
# 裸金属升级
# =============================================================================
upgrade_metal(){
  hr "裸金属升级 → ${VER}"

  # 前置检查：必须 root
  [ "$(id -u)" = 0 ] || { err "裸金属升级需要 root 权限"; exit 1; }
  command -v systemctl >/dev/null || { err "未找到 systemctl（裸金属升级仅支持 systemd 系统）"; exit 1; }
  command -v python3 >/dev/null || { err "未找到 python3（补 panel.json 字段需要）"; exit 1; }

  # 备份
  hr "1/7 备份当前状态"
  local TS BK
  TS=$(date +%Y%m%d-%H%M%S)
  BK="${BACKUP_ROOT}/ansgo-backup-upgrade-${TS}"
  mkdir -p "$BK"
  for f in "$METAL_PANEL_BIN" "$METAL_GENCONF" "$METAL_ADMIN" "$METAL_CONF" "$METAL_SECRETS" \
           /etc/sing-box/config.json /etc/caddy/Caddyfile; do
    if [ -f "$f" ]; then
      cp -a "$f" "$BK/$(echo "$f" | tr '/' '_')"
      log "  已备份 $(echo "$f" | tr '/' '_')"
    fi
  done
  ok "备份目录: $BK"

  # 记录旧 panel md5（用于判断是否真更新）
  local old_panel_md5=""
  [ -f "$METAL_PANEL_BIN" ] && old_panel_md5=$(md5sum "$METAL_PANEL_BIN" 2>/dev/null | awk '{print $1}')

  # 更新 genconf + admin 脚本（raw 拉取，直接覆盖——下次调用即生效）
  hr "2/7 更新 ansgo-genconf + ansgo-admin 脚本"
  log "从 ${RAW}/ 下载最新脚本"
  dl_or_exit "$RAW/ansgo-genconf" "$METAL_GENCONF"
  dl_or_exit "$RAW/ansgo-admin"   "$METAL_ADMIN"
  chmod 0755 "$METAL_GENCONF" "$METAL_ADMIN"
  ok "脚本已更新（genconf: SOCKS5 inbound 生成 / admin: regen socks + info 输出 SOCKS5 URI）"

  # 更新 panel 二进制（release 拉取，走 .new→md5→.bak→mv→restart 安全流程）
  hr "3/7 更新 ansgo-panel 二进制"
  local panel_tmp="/tmp/ansgo-panel-${VER_NUM}.new"
  log "从 release 下载 ansgo-panel-linux-${AARCH} (${VER})"
  dl_or_exit "$REL/$VER/ansgo-panel-linux-${AARCH}" "$panel_tmp"
  chmod 0755 "$panel_tmp"

  local new_panel_md5
  new_panel_md5=$(md5sum "$panel_tmp" 2>/dev/null | awk '{print $1}')

  if [ -n "$old_panel_md5" ] && [ "$old_panel_md5" = "$new_panel_md5" ]; then
    ok "panel 二进制 md5 与当前一致（${new_panel_md5:0:12}...），跳过替换"
    rm -f "$panel_tmp"
  else
    # 安全替换：先下载校验过的 .new → 备份旧为 .bak → mv 替换
    # panel 无 -version flag，靠 md5 变化 + 启动日志验证
    if [ -f "$METAL_PANEL_BIN" ]; then
      cp -a "$METAL_PANEL_BIN" "${METAL_PANEL_BIN}.bak"
      log "  旧二进制备份为 ${METAL_PANEL_BIN}.bak（md5 ${old_panel_md5:0:12}...）"
    fi
    mv "$panel_tmp" "$METAL_PANEL_BIN"
    chmod 0755 "$METAL_PANEL_BIN"
    ok "panel 二进制已替换（新 md5 ${new_panel_md5:0:12}...）"
  fi

  # 补 panel.json 缺失字段（socks_port / svc_socks_enabled / panel_title）
  hr "4/7 补 panel.json 缺失字段（SOCKS5 + 自定义标题）"
  if [ ! -f "$METAL_CONF" ]; then
    warn "$METAL_CONF 不存在，跳过字段补全（panel 启动后会用默认值创建）"
  else
    python3 - "$METAL_CONF" <<'PY' || warn "python3 补字段失败（手动检查 panel.json 是否有 socks_port/svc_socks_enabled/panel_title）"
import json, random, sys
p = sys.argv[1]
with open(p) as f:
    d = json.load(f)
changed = []

def rand_port(d):
    used = set()
    for k in ("ss_port","anytls_port","naive_port","naive2_port","anytls2_port",
              "panel_port","socks_port"):
        v = d.get(k)
        if isinstance(v, int) and v > 0:
            used.add(v)
    used |= {80, 443, 25822}
    for _ in range(1000):
        r = random.randint(10000, 65535)
        if r not in used:
            return r
    return random.randint(10000, 65535)

if not d.get("socks_port"):
    d["socks_port"] = rand_port(d)
    changed.append(f"socks_port={d['socks_port']}（随机生成，可在面板改）")
if "svc_socks_enabled" not in d:
    d["svc_socks_enabled"] = "false"   # 默认不启用（按需在面板开启）
    changed.append("svc_socks_enabled=false（SOCKS5 默认不启用）")
if not d.get("panel_title"):
    d["panel_title"] = "ANS-GO 管理面板"
    changed.append("panel_title=ANS-GO 管理面板（可在面板设置页改）")

with open(p, "w") as f:
    json.dump(d, f, ensure_ascii=False, indent=2)
if changed:
    print("  已补字段:")
    for c in changed:
        print(f"    + {c}")
else:
    print("  panel.json 字段已齐全，无需补全")
PY
  fi

  # 补 secrets.env 缺失的 SOCKS5 凭证（幂等：已有则跳过）
  hr "5/7 补 secrets.env 缺失的 SOCKS5 凭证"
  if [ ! -f "$METAL_SECRETS" ]; then
    warn "$METAL_SECRETS 不存在，跳过凭证补全"
  else
    local added=0
    if ! grep -q '^SOCKS_USER=' "$METAL_SECRETS"; then
      echo "SOCKS_USER=$(openssl rand -hex 6)" >> "$METAL_SECRETS"
      log "  + SOCKS_USER（随机生成）"; added=1
    fi
    if ! grep -q '^SOCKS_PASS=' "$METAL_SECRETS"; then
      echo "SOCKS_PASS=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c20)" >> "$METAL_SECRETS"
      log "  + SOCKS_PASS（随机生成）"; added=1
    fi
    chmod 600 "$METAL_SECRETS"
    [ "$added" = 0 ] && ok "secrets.env 凭证已齐全，无需补全" || ok "SOCKS5 凭证已补全"
  fi

  # 重启面板加载新二进制 + 新字段
  hr "6/7 重启 ansgo-panel"
  systemctl restart ansgo-panel || { err "ansgo-panel 重启失败，检查 journalctl -u ansgo-panel"; exit 1; }
  ok "ansgo-panel 已重启"

  # 验证
  hr "7/7 验证"
  sleep 2
  local panel_active svc_active
  panel_active=$(systemctl is-active ansgo-panel 2>/dev/null || echo unknown)
  if [ "$panel_active" = active ]; then
    ok "ansgo-panel: active"
  else
    err "ansgo-panel: $panel_active（非 active！请检查 journalctl -u ansgo-panel）"
  fi

  # 版本号验证（panel 无 -version flag，从启动日志读）
  local logged_ver
  logged_ver=$(journalctl -u ansgo-panel -n 20 --no-pager 2>/dev/null \
    | grep -oE 'ansgo-panel v[0-9]+\.[0-9]+\.[0-9]+' | tail -1 | grep -oE 'v[0-9.]+$' || echo "")
  if [ -n "$logged_ver" ]; then
    if [ "$logged_ver" = "$VER" ]; then
      ok "面板版本: ${logged_ver}（目标 ${VER} ✓）"
    else
      warn "面板日志版本: ${logged_ver}（目标 ${VER}，可能二进制未随 release 同步或日志未刷新）"
    fi
  else
    warn "未能从日志读取版本号（可稍后 journalctl -u ansgo-panel | grep 'ansgo-panel v' 确认）"
  fi

  # 其他服务状态（不强制 active，仅提示）
  for svc in caddy sing-box; do
    local st; st=$(systemctl is-active "$svc" 2>/dev/null || echo unknown)
    log "  ${svc}: ${st}"
  done

  echo
  hr "升级完成"
  # 注意：必须用 printf 而非 echo。bash 的 echo 默认不解释 \033 转义，
  # 会把颜色码原样输出成字面 "\033[36m..."（即"乱码"）。printf 会正确解释。
  printf '  备份目录 : %b%s%b\n' "$C_B" "$BK" "$C_0"
  printf '  回滚命令 : %bcp -a "%s/_usr_local_bin_ansgo-panel" %s \&\& systemctl restart ansgo-panel%b\n' "$C_B" "$BK" "$METAL_PANEL_BIN" "$C_0"
  echo
  printf '  %bSOCKS5 默认未启用%b。启用方式：\n' "$C_Y" "$C_0"
  echo "    1. 登录面板 →「服务管理」页 → SOCKS5 卡片 → 点「安装」"
  echo "    2. 或 SSH 执行: ansgo-admin regen socks（生成/重置凭证，sing-box 自动重载）"
  echo "    3. 查看连接 URI: ansgo-admin info"
}

# =============================================================================
# Docker 升级
# =============================================================================
upgrade_docker(){
  hr "Docker 升级 → ${VER}"

  command -v docker >/dev/null || { err "未找到 docker（这台机器不是 Docker 部署形态？用 --metal 走裸金属路径）"; exit 1; }

  if [ ! -f "$DOCKER_COMPOSE_FILE" ]; then
    err "未找到 $DOCKER_COMPOSE_FILE"
    err "Docker 部署的 compose 文件应在此路径。如果你的部署目录不同，请手动 cd 到该目录执行："
    err "  docker compose pull && docker compose up -d"
    exit 1
  fi

  local COMPOSE
  COMPOSE=$(detect_compose)
  if [ -z "$COMPOSE" ]; then
    err "未找到 docker compose（v2 子命令）也未找到 docker-compose（v1）。"
    err "  Debian/Ubuntu: apt-get install docker-compose-plugin"
    exit 8
  fi
  log "compose 命令: $COMPOSE ($($COMPOSE version --short 2>/dev/null || echo unknown))"

  # 备份配置卷（可选，失败不阻断升级）
  hr "1/4 备份配置卷"
  local TS backup_file
  TS=$(date +%Y%m%d-%H%M%S)
  backup_file="${DOCKER_DIR}/ansgo-etc-vol-backup-${TS}.tgz"
  # 尝试备份 ansgo_etc 命名卷（compose project 名可能不同，用通配）
  if docker run --rm -v ansgo_etc:/src:ro -v "${DOCKER_DIR}:/dst" alpine \
       sh -c "tar czf /dst/$(basename "$backup_file") -C /src ." 2>/dev/null; then
    ok "配置卷备份: $backup_file"
  else
    warn "配置卷备份跳过（卷名可能不是 ansgo_etc；volume 数据不会因重建容器丢失，可放心继续）"
    rm -f "$backup_file"
  fi

  # 拉取新镜像
  hr "2/4 拉取新镜像"
  ( cd "$DOCKER_DIR" && $COMPOSE pull ) || { err "镜像拉取失败（检查网络/代理）"; exit 1; }
  ok "镜像已更新"

  # 重建容器（复用 volume，配置/密钥/证书不丢）
  # v1.5.19: 必须 --force-recreate。普通 `up -d` 在镜像 digest 未变（compose 配置哈希相同）
  #   时会跳过 recreate → entrypoint 不重跑 → 旧二进制继续运行，升级"没变化"。
  #   --force-recreate 强制销毁重建容器，volume 数据不丢，entrypoint 重新执行：
  #     ① ansgo-genconf 重新生成配置 ② systemd 全新状态拉起新版 caddy/sing-box/ansgo-panel
  hr "3/4 重建容器（--force-recreate 确保 entrypoint 重跑 + 新二进制生效）"
  ( cd "$DOCKER_DIR" && $COMPOSE up -d --force-recreate ) || { err "容器重建失败"; exit 1; }
  ok "容器已重建（entrypoint 已重新执行）"

  # 验证
  hr "4/4 验证"
  sleep 3
  local container_status
  container_status=$(docker ps --filter name=ansgo --format '{{.Status}}' 2>/dev/null | head -1)
  if [ -n "$container_status" ]; then
    ok "容器状态: ${container_status}"
  else
    err "未发现运行中的 ansgo 容器（docker ps -a 检查）"
  fi

  # 版本号验证（从容器日志读）
  local logged_ver
  logged_ver=$(docker logs ansgo 2>&1 | grep -oE 'ansgo-panel v[0-9]+\.[0-9]+\.[0-9]+' \
    | tail -1 | grep -oE 'v[0-9.]+$' || echo "")
  if [ -n "$logged_ver" ]; then
    if [ "$logged_ver" = "$VER" ]; then
      ok "面板版本: ${logged_ver}（目标 ${VER} ✓）"
    else
      err "面板日志版本: ${logged_ver}（目标 ${VER}）—— 镜像可能未同步发版，或容器未真正重建"
      err "排查：docker exec ansgo md5sum /usr/local/bin/ansgo-panel 对比 release 资产"
      err "强制重建：cd $DOCKER_DIR && $COMPOSE up -d --force-recreate"
    fi
  else
    warn "未能从容器日志读取版本号（docker logs ansgo 2>&1 | grep 'ansgo-panel v'）"
  fi

  echo
  hr "升级完成"
  # 注意：必须用 printf 而非 echo（同 metal 分支，bash echo 不解释 \033 转义）
  printf '  %bSOCKS5 默认未启用%b。启用方式：\n' "$C_Y" "$C_0"
  echo "    1. 登录面板 →「服务管理」页 → SOCKS5 卡片 → 点「安装」"
  echo "    2. 或进容器执行: docker exec ansgo ansgo-admin regen socks"
  echo "    3. 查看连接 URI: docker exec ansgo ansgo-admin info"
  echo
  echo "  回滚: 编辑 ${DOCKER_COMPOSE_FILE}，把 image: ghcr.io/jiasongji/ansgo:latest"
  echo "        改成 :v1.5.15（或上一版），再 docker compose up -d"
}

# =============================================================================
# 主流程
# =============================================================================
main(){
  hr "ANS-GO 升级脚本 → 目标版本 ${VER}"
  log "架构: ${ARCH} → ${AARCH}  |  主机: $(hostname)"

  check_disk

  local mode
  mode=$(detect_mode)

  case "$mode" in
    docker|metal)
      log "检测到部署形态: ${mode}$([ -n "$FORCE_MODE" ] && echo "（--${FORCE_MODE} 强制）" || true)"
      ;;
    ambiguous)
      err "同时检测到 Docker 和裸金属部署特征，无法自动判断形态。"
      err "  Docker 标记:   $DOCKER_COMPOSE_FILE 存在"
      err "  裸金属标记:    $METAL_CONF 或 $METAL_PANEL_BIN 存在"
      err "请用 --docker 或 --metal 显式指定升级路径。"
      exit 1
      ;;
    none)
      err "未检测到 ANS-GO 部署（既无 ${DOCKER_COMPOSE_FILE}，也无裸金属特征）。"
      err "如果是全新部署，请用 install.sh 而非本升级脚本。"
      exit 1
      ;;
  esac

  # 确认
  if [ "$ASSUME_YES" != 1 ]; then
    echo
    printf "将升级到 ${C_B}${VER}${C_0}（${mode} 形态）。升级前会自动备份。继续？[y/N] "
    local c; c=$(readtty)
    case "${c:-}" in
      y|Y) ;;
      *) warn "已取消"; exit 0;;
    esac
  fi

  case "$mode" in
    metal)  upgrade_metal;;
    docker) upgrade_docker;;
  esac
}

main "$@"
