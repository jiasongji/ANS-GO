#!/bin/bash
# =============================================================================
# ansgo-cert-issue.sh —— AGENTS §9 步骤 1-3（服务端执行）
#   1. 安装 acme.sh（curl，~200KB）
#   2. 部署 dns_dynukey.sh（路径A 钩子）+ 校验路径B 凭证可达
#   3. 签发 example.com ECDSA 证书：路径A(默认) → 失败降级路径B
#      安装到 /etc/ssl/ansgo/{fullchain,privkey}.pem，reloadcmd=ansgo-cert-reload
#
# 用法（凭证经环境传入，不落盘额外文件）：
#   DYNU_API_KEY=... DYNU_CLIENT_ID=... DYNU_SECRET=... \
#     nohup bash ansgo-cert-issue.sh > /root/ansgo-cert-issue.log 2>&1 &
#
# 输出状态文件：/etc/ansgo-cert.status  (SUCCESS_A | SUCCESS_B | FAILED)
# =============================================================================
set -uo pipefail
DOMAIN="${DOMAIN:-example.com}"
EMAIL="${EMAIL:-admin@${DOMAIN}}"
CERTDIR="/etc/ssl/ansgo"
STATUS_FILE="/etc/ansgo-cert.status"
ACME="/root/.acme.sh/acme.sh"

log(){ echo "[$(date '+%H:%M:%S')] $*"; }

mkdir -p "$CERTDIR"

# ---- 步骤1: 安装 acme.sh ----
log "=== 步骤1: 安装 acme.sh ==="
if [ -f "$ACME" ]; then
  log "acme.sh 已存在，跳过安装。"
elif [ -n "${ACME_TARBALL:-}" ] && [ -f "$ACME_TARBALL" ]; then
  log "从 vendored 快照安装 acme.sh: $ACME_TARBALL"
  cd /root && tar xzf "$ACME_TARBALL" && cd acme.sh-* && ./acme.sh --install --home /root/.acme.sh --accountemail "${EMAIL}" >/dev/null 2>&1 && cd /root
  [ -f "$ACME" ] || { log "FATAL: vendored acme.sh 安装失败"; echo "FAILED:install" > "$STATUS_FILE"; exit 1; }
else
  if curl -fsSL https://get.acme.sh | sh -s "email=${EMAIL}"; then
    log "acme.sh 安装完成（官方源）。"
  else
    log "FATAL: acme.sh 安装失败"; echo "FAILED:install" > "$STATUS_FILE"; exit 1
  fi
fi
"$ACME" --set-default-ca --server letsencrypt >/dev/null 2>&1
# 注册账号（幂等，已注册会提示）
"$ACME" --register-account -m "${EMAIL}" --server letsencrypt >/dev/null 2>&1 || log "register-account: 已注册或忽略"

# ---- 部署重载助手 + 钩子 ----
install -m 0755 /etc/ansgo-deploy/ansgo-cert-reload /usr/local/bin/ansgo-cert-reload
mkdir -p /root/.acme.sh/dnsapi
install -m 0644 /etc/ansgo-deploy/dns_dynukey.sh /root/.acme.sh/dnsapi/dns_dynukey.sh
log "已部署 ansgo-cert-reload 与 dns_dynukey.sh 钩子。"

# ---- 步骤3: 签发（路径A 默认，失败降级B）----
USED=""
if [ -n "${DYNU_API_KEY:-}" ]; then
  log "=== 步骤3A: 路径A（API Key）签发 ==="
  export DYNU_API_KEY
  if "$ACME" --issue --dns dns_dynukey -d "$DOMAIN" --keylength ec-256 --server letsencrypt; then
    USED="A"
    log "路径A 签发成功。"
  else
    log "路径A 签发失败，准备降级路径B。"
  fi
else
  log "未提供 DYNU_API_KEY，跳过路径A。"
fi

if [ -z "$USED" ] && [ -n "${DYNU_CLIENT_ID:-}" ] && [ -n "${DYNU_SECRET:-}" ]; then
  log "=== 步骤3B: 路径B（OAuth2）签发 ==="
  export Dynu_ClientId="$DYNU_CLIENT_ID"
  export Dynu_Secret="$DYNU_SECRET"
  if "$ACME" --issue --dns dns_dynu -d "$DOMAIN" --keylength ec-256 --server letsencrypt; then
    USED="B"
    log "路径B 签发成功。"
  else
    log "路径B 签发也失败。"
  fi
fi

if [ -z "$USED" ]; then
  log "FATAL: A/B 双路径均失败，保留现有自签证书继续服务。"
  echo "FAILED" > "$STATUS_FILE"
  exit 1
fi

# ---- 安装证书到固定路径 + 配置续期 reloadcmd ----
log "=== 安装证书到 ${CERTDIR}（reloadcmd=ansgo-cert-reload）==="
if "$ACME" --install-cert -d "$DOMAIN" --ecc \
    --key-file       "${CERTDIR}/privkey.pem" \
    --fullchain-file "${CERTDIR}/fullchain.pem" \
    --reloadcmd      "/usr/local/bin/ansgo-cert-reload"; then
  log "证书安装完成。"
else
  log "FATAL: install-cert 失败"; echo "FAILED:install-cert" > "$STATUS_FILE"; exit 1
fi

chmod 644 "${CERTDIR}/fullchain.pem"
chmod 600 "${CERTDIR}/privkey.pem"

# ---- 校验 ----
log "=== 证书校验 ==="
SUBJECT=$(openssl x509 -in "${CERTDIR}/fullchain.pem" -noout -subject 2>/dev/null)
ISSUER=$(openssl x509 -in "${CERTDIR}/fullchain.pem" -noout -issuer 2>/dev/null)
DATES=$(openssl x509 -in "${CERTDIR}/fullchain.pem" -noout -dates 2>/dev/null)
log "subject: $SUBJECT"
log "issuer:  $ISSUER"
log "dates:   $DATES"

if echo "$ISSUER" | grep -qi "let's encrypt\|R10\|R11\|R12\|R13\|R14\|E1\|E2\|E5\|E6\|E7\|E8\|E9"; then
  log "✅ 确认 Let's Encrypt 签发。"
  echo "SUCCESS_${USED}" > "$STATUS_FILE"
  echo "ISSUER=${ISSUER}" >> "$STATUS_FILE"
  echo "SUBJECT=${SUBJECT}" >> "$STATUS_FILE"
else
  log "⚠️ issuer 非 Let's Encrypt 系列，请人工核查：$ISSUER"
  echo "WARN_NOT_LE_${USED}" > "$STATUS_FILE"
fi

log "=== 完成（路径=${USED}）==="
