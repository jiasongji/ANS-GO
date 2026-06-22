#!/bin/bash
# =============================================================================
# ansgo-entrypoint —— all-in-one 容器初始化 + 启动 systemd（PID 1）
#
# 首次启动（/etc/ansgo/panel.json 不存在）：
#   1. 生成 panel.json（端口/伪装/svc_*_enabled=false/随机 url_path）
#   2. 生成 secrets.env（SS / ANYTLS / NAIVE 密钥）
#   3. 设置管理员密码（PANEL_PASS 环境变量 → bcrypt）
# 每次启动：
#   4. 确保证书存在（无则自签占位，保证服务能起）
#   5. ansgo-genconf 生成 sing-box / caddy 配置
#   6. 后台签发真实证书（若有 Dynu 凭证且当前非 Let's Encrypt）
#   7. exec /sbin/init（systemd 拉起 caddy(:443) + ansgo-panel）
#
# 所有配置/密钥/证书持久化在挂载卷，容器重建不丢失。
# =============================================================================
set -uo pipefail

CONF=/etc/ansgo/panel.json
SECRETS=/etc/ansgo/secrets.env
CERTDIR=/etc/ssl/ansgo
DOMAIN="${DOMAIN:-example.com}"
EMAIL="${EMAIL:-admin@${DOMAIN}}"
# 证书来源：acme（默认，需 Dynu 凭证）/ manual（手动指定已有证书+私钥路径）
CERT_MODE="${CERT_MODE:-acme}"
CERT_FULLCHAIN="${CERT_FULLCHAIN:-}"
CERT_PRIVKEY="${CERT_PRIVKEY:-}"

log(){ echo "[entrypoint] $*"; }

mkdir -p /etc/ansgo "$CERTDIR" /etc/sing-box /etc/caddy /var/www/html /var/log

# ---- 1/3 首次：panel.json + secrets.env ----
if [ ! -f "$CONF" ]; then
  log "首次初始化：生成 panel.json + secrets.env"
  URLPATH="${URL_PATH:-/$(openssl rand -hex 4)/}"
  cat > "$CONF" <<EOF
{
  "domain": "${DOMAIN}",
  "panel_port": ${PANEL_PORT:-15608},
  "url_path": "${URLPATH}",
  "admin_user": "${PANEL_USER:-admin}",
  "admin_pass_hash": "PLACEHOLDER",
  "session_hours": 8,
  "login_lock_threshold": 5,
  "login_lock_minutes": 10,
  "ss_port": ${SS_PORT:-23456},
  "ss_method": "2022-blake3-aes-128-gcm",
  "anytls_port": ${ANYTLS_PORT:-8443},
  "naive_port": ${NAIVE_PORT:-44333},
  "disguise_panel": "${DISGUISE_PANEL:-proxy:https://example.com}",
  "disguise_naive": "${DISGUISE_NAIVE:-proxy:https://example.com}",
  "disguise_naive2": "${DISGUISE_NAIVE:-proxy:https://example.com}",
  "svc_ss_enabled": "false",
  "svc_anytls_enabled": "false",
  "svc_naive_enabled": "false",
  "caddy_enable": "$([ "${NO_CADDY:-0}" = 1 ] && echo false || echo true)",
  "cert_mode": "${CERT_MODE}",
  "cert_dir": "/etc/ssl/ansgo",
  "cert_fullchain": "${CERT_FULLCHAIN}",
  "cert_privkey": "${CERT_PRIVKEY}",
  "db_path": "/etc/ansgo/sessions.db"
}
EOF
  chmod 600 "$CONF"
  log "已生成 $CONF (url_path=${URLPATH})"

  if [ ! -f "$SECRETS" ]; then
    # v1.5.5: 支持宿主通过 ansgo.env 透传 SS_KEY_IN/ANYTLS_PASS_IN 等预指定密钥
    #         （install.sh 已校验格式），未指定的留空 → 容器内随机生成
    cat > "$SECRETS" <<EOF
SS_METHOD=2022-blake3-aes-128-gcm
SS_KEY=${SS_KEY_IN:-$(openssl rand -base64 16)}
ANYTLS_PASS=${ANYTLS_PASS_IN:-$(openssl rand -hex 16)}
ANYTLS_UUID=${ANYTLS_UUID_IN:-$(cat /proc/sys/kernel/random/uuid)}
NAIVE_USER=${NAIVE_USER_IN:-$(openssl rand -hex 6)}
NAIVE_PASS=${NAIVE_PASS_IN:-$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)}
EOF
    chmod 600 "$SECRETS"
    log "已生成 $SECRETS"
  fi
fi

# ---- 2/3 设置管理员密码（仅当仍是 PLACEHOLDER 且提供了 PANEL_PASS）----
if grep -q '"admin_pass_hash": "PLACEHOLDER"' "$CONF" 2>/dev/null && [ -n "${PANEL_PASS:-}" ]; then
  log "设置管理员密码（来自 PANEL_PASS 环境变量，已写入 bcrypt）"
  /usr/local/bin/ansgo-panel -setpass "${PANEL_PASS}" \
    || log "WARN: setpass 失败，稍后可用 docker exec ansgo ansgo-admin panel-pass 重置"
fi

# ---- 3/3 证书：manual 模式同步到 /etc/ssl/ansgo/ 卷；acme 模式无则自签占位 ----
# v1.5.10: manual 模式不再让 sing-box/caddy 直接读宿主路径（SELinux/权限问题），
#         改为启动时把宿主证书 cp 到 /etc/ssl/ansgo/ 卷（容器完全控制），
#         并把 panel.json 的 cert_mode 改为 acme（让 genconf 用 /etc/ssl/ansgo/ 路径）。
#         续期：宿主更新证书后，docker restart ansgo（重跑 entrypoint 自动同步）
if [ "$CERT_MODE" = "manual" ]; then
  log "证书来源：手动模式（宿主 cert=$CERT_FULLCHAIN）"
  if [ ! -f "$CERT_FULLCHAIN" ] || [ ! -f "$CERT_PRIVKEY" ]; then
    log "ERROR: 证书/私钥文件不存在（确认 docker-compose.yml 已 bind mount 宿主证书目录）"
    log "  缺失: $([ ! -f "$CERT_FULLCHAIN" ] && echo "$CERT_FULLCHAIN") $([ ! -f "$CERT_PRIVKEY" ] && echo "$CERT_PRIVKEY")"
  else
    # 同步宿主证书到 ansgo_ssl 卷（容器完全控制，无权限问题）
    cp -f "$CERT_FULLCHAIN" "$CERTDIR/fullchain.pem"
    cp -f "$CERT_PRIVKEY" "$CERTDIR/privkey.pem"
    chmod 644 "$CERTDIR/fullchain.pem" "$CERTDIR/privkey.pem"
    log "已同步宿主证书到 $CERTDIR/（fullchain.pem + privkey.pem，644 权限）"
    # 改 panel.json 为 acme 模式（让 genconf 用 /etc/ssl/ansgo/，不再依赖宿主路径）
    if [ -f "$CONF" ]; then
      python3 -c "
import json
p = '$CONF'
c = json.load(open(p))
c.pop('cert_fullchain', None)
c.pop('cert_privkey', None)
c['cert_mode'] = 'acme'
json.dump(c, open(p, 'w'), indent=2, ensure_ascii=False)
" 2>/dev/null && log "panel.json 已切换为 acme 模式（用 $CERTDIR/）"
    fi
  fi
else
  if [ ! -s "$CERTDIR/fullchain.pem" ] || [ ! -s "$CERTDIR/privkey.pem" ]; then
    log "生成自签占位证书（真实证书将由 acme.sh 后台签发覆盖）"
    openssl ecparam -genkey -name prime256v1 -out "$CERTDIR/privkey.pem" 2>/dev/null \
      || openssl genrsa -out "$CERTDIR/privkey.pem" 2048
    openssl req -new -x509 -days 365 -key "$CERTDIR/privkey.pem" \
      -out "$CERTDIR/fullchain.pem" -subj "/CN=${DOMAIN}" 2>/dev/null || true
  fi
fi

# 生成 sing-box / caddy 配置（幂等；失败时写兜底 Caddyfile，避免 caddy 起不来）
if /usr/local/bin/ansgo-genconf all >/var/log/ansgo-genconf.log 2>&1; then
  log "已生成 sing-box + caddy 配置"
else
  log "WARN: ansgo-genconf 失败（见 /var/log/ansgo-genconf.log），写入兜底 Caddyfile"
  [ -f /etc/caddy/Caddyfile ] || \
    printf '0.0.0.0:443 {\n  respond "ANS-GO"\n}\n:80 {\n  redir https://%s{uri} 308\n}\n' "$DOMAIN" > /etc/caddy/Caddyfile
fi

# v1.5.10: --no-caddy 模式 caddy 启停逻辑（修复 v1.5.10 的 bug）
#   v1.5.10 bug：容器重启时即使 naive 已装（svc_naive_enabled=true）也 stop caddy → naive 失效
#   正确逻辑：caddy_enable=false 且 naive 未装时才不启 caddy；
#             naive 已装时 caddy 必须启动（naive 依赖 caddy 的 forwardproxy）
NO_CADDY_MODE=0
if [ "${NO_CADDY:-0}" = 1 ] || grep -q '"caddy_enable": "false"' "$CONF" 2>/dev/null; then
  NO_CADDY_MODE=1
fi
if [ "$NO_CADDY_MODE" = 1 ]; then
  # 检查 naive 是否已启用（从 panel.json 读 svc_naive_enabled）
  NAIVE_ON=$(grep -o '"svc_naive_enabled": *"[^"]*"' "$CONF" 2>/dev/null | grep -o '"[^"]*"$' | tr -d '"')
  if [ "$NAIVE_ON" = "true" ]; then
    log "--no-caddy 模式但 naive 已启用：启动 caddy（仅听 naive 端口，不碰 80/443）"
    systemctl enable caddy.service 2>/dev/null || true
    systemctl restart caddy.service 2>/dev/null || true
  else
    log "--no-caddy 模式且 naive 未启用：不启动 caddy（80/443 由 nginx 等接管）"
    systemctl disable caddy.service 2>/dev/null || true
    systemctl stop caddy.service 2>/dev/null || true
  fi
fi

# 后台签发真实证书（acme 模式且当前非 LE 且提供了 Dynu 凭证；manual 模式跳过）
is_le(){ [ -f "$CERTDIR/fullchain.pem" ] \
  && openssl x509 -in "$CERTDIR/fullchain.pem" -noout -issuer 2>/dev/null \
  | grep -qi "let's encrypt"; }
if [ "$CERT_MODE" != "manual" ] && ! is_le && { [ -n "${DYNU_API_KEY:-}" ] || [ -n "${DYNU_CLIENT_ID:-}" ]; }; then
  log "后台签发 Let's Encrypt 证书（DNS-01，约 1-3 分钟），日志 /var/log/ansgo-cert.log"
  DOMAIN="$DOMAIN" EMAIL="$EMAIL" \
    DYNU_API_KEY="${DYNU_API_KEY:-}" DYNU_CLIENT_ID="${DYNU_CLIENT_ID:-}" DYNU_SECRET="${DYNU_SECRET:-}" \
    nohup bash /usr/local/bin/ansgo-cert-issue.sh > /var/log/ansgo-cert.log 2>&1 &
fi

# 容器内 PATH（含 acme.sh，供面板 exec ansgo-admin/genconf 使用）
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/root/.acme.sh

log "启动 systemd（PID 1）—— 拉起 caddy(:443 伪装) + ansgo-panel"
exec /sbin/init
