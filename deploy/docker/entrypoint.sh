#!/bin/bash
# =============================================================================
# ansgo-entrypoint —— all-in-one 容器初始化 + 启动 systemd（PID 1）
#
# 首次启动（/etc/ansgo/panel.json 不存在）：
#   1. 生成 panel.json（端口/伪装/svc_*_enabled=false/随机 url_path）
#   2. 生成 secrets.env（SS / ANYTLS / SOCKS / NAIVE 密钥）
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
# v1.5.32: 标记容器环境，面板 isDockerMode() 据此识别 Docker 形态（显示 manual 证书宿主路径 UI）。
# 注意：本 shell export 不会传给 systemd 拉起的 ansgo-panel.service，所以 isDockerMode()
#       主要依赖下方写入的 /.dockerenv 文件（每启动都确保存在）。env 仅作容器内其它脚本参考。
[ -f /.dockerenv ] || { : > /.dockerenv; }
export ANSGO_DOCKER=1

# v1.5.12: 端口默认随机生成（10000-65535）。容器内无 ss 命令查占用，
# host 网络模式下 install.sh 已在宿主选好随机端口并通过 ansgo.env 透传
# （SS_PORT/ANYTLS_PORT/SOCKS_PORT/NAIVE_PORT/PANEL_PORT），此处仅作容器首次启动兜底。
_rand_port(){ python3 -c "import random;print(random.randint(10000,65535))"; }
RPANEL_PORT="${PANEL_PORT:-$(_rand_port)}"
RSS_PORT="${SS_PORT:-$(_rand_port)}"
RANYTLS_PORT="${ANYTLS_PORT:-$(_rand_port)}"
RSOCKS_PORT="${SOCKS_PORT:-$(_rand_port)}"
RNAIVE_PORT="${NAIVE_PORT:-$(_rand_port)}"

# ---- 1/3 首次：panel.json + secrets.env ----
if [ ! -f "$CONF" ]; then
  log "首次初始化：生成 panel.json + secrets.env（端口随机：panel=$RPANEL_PORT ss=$RSS_PORT anytls=$RANYTLS_PORT socks=$RSOCKS_PORT naive=$RNAIVE_PORT）"
  URLPATH="${URL_PATH:-/$(openssl rand -hex 4)/}"
  cat > "$CONF" <<EOF
{
  "domain": "${DOMAIN}",
  "panel_port": ${RPANEL_PORT},
  "panel_title": "ANS-GO 管理面板",
  "url_path": "${URLPATH}",
  "admin_user": "${PANEL_USER:-admin}",
  "admin_pass_hash": "PLACEHOLDER",
  "session_hours": 8,
  "login_lock_threshold": 5,
  "login_lock_minutes": 10,
  "ss_port": ${RSS_PORT},
  "ss_method": "2022-blake3-aes-128-gcm",
  "anytls_port": ${RANYTLS_PORT},
  "socks_port": ${RSOCKS_PORT},
  "naive_port": ${RNAIVE_PORT},
  "disguise_panel": "${DISGUISE_PANEL:-proxy:https://example.com}",
  "disguise_naive": "${DISGUISE_NAIVE:-proxy:https://example.com}",
  "svc_ss_enabled": "false",
  "svc_anytls_enabled": "false",
  "svc_socks_enabled": "false",
  "svc_naive_enabled": "false",
  "caddy_enable": "$([ "${NO_CADDY:-0}" = 1 ] && echo false || echo true)",
  "cert_mode": "${CERT_MODE}",
  "cert_dir": "/etc/ssl/ansgo",
  "cert_fullchain": "${CERT_FULLCHAIN}",
  "cert_privkey": "${CERT_PRIVKEY}",
  "dynu_api_key": "${DYNU_API_KEY:-}",
  "dynu_client_id": "${DYNU_CLIENT_ID:-}",
  "dynu_secret": "${DYNU_SECRET:-}",
  "acme_email": "${EMAIL:-}",
  "db_path": "/etc/ansgo/sessions.db"
}
EOF
  chmod 600 "$CONF"
  log "已生成 $CONF (url_path=${URLPATH})"
  log "⚠️ 随机端口：panel=$RPANEL_PORT ss=$RSS_PORT anytls=$RANYTLS_PORT socks=$RSOCKS_PORT naive=$RNAIVE_PORT（请记下！）"

  if [ ! -f "$SECRETS" ]; then
    # v1.5.5: 支持宿主通过 ansgo.env 透传 SS_KEY_IN/ANYTLS_PASS_IN 等预指定密钥
    #         （install.sh 已校验格式），未指定的留空 → 容器内随机生成
    cat > "$SECRETS" <<EOF
SS_METHOD=2022-blake3-aes-128-gcm
SS_KEY=${SS_KEY_IN:-$(openssl rand -base64 16)}
ANYTLS_PASS=${ANYTLS_PASS_IN:-$(openssl rand -hex 16)}
ANYTLS_UUID=${ANYTLS_UUID_IN:-$(cat /proc/sys/kernel/random/uuid)}
SOCKS_USER=${SOCKS_USER_IN:-$(openssl rand -hex 6)}
SOCKS_PASS=${SOCKS_PASS_IN:-$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)}
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

# ---- 3/3 证书：manual 模式同步宿主证书到 /etc/ssl/ansgo/ 卷；acme 模式无则自签占位 ----
# v1.5.32: Docker 手动证书改为「固定同步目录」模型：
#   宿主计划任务把证书写入 /etc/ansgo-docker/manual-certs/（compose 只读挂载到 /host/manual-certs）
#   -> ansgo-sync-manual-cert 校验并原子替换 /etc/ssl/ansgo/{fullchain,privkey}.pem
#   -> caddy/sing-box/ansgo-panel 始终使用 /etc/ssl/ansgo/ 副本（权限友好，避免宿主目录 600 读不了）
#   面板不再要求容器读取任意宿主路径，也不再动态改 compose 挂载。
#   续期：用户在面板「证书管理」复制系统定时任务/宝塔计划任务脚本到宿主执行即可。
# 兼容：若旧 env 仍带 CERT_FULLCHAIN/CERT_PRIVKEY 且文件可读（旧部署已 bind mount），
#       则把它们当作初始同步源，cp 进 /host/manual-certs 的对应挂载宿主目录（若可写），
#       或直接 cp 到 /etc/ssl/ansgo/；同时记录到 cert_host_fullchain/cert_host_privkey。
HOST_SYNC_DIR_HOST="/etc/ansgo-docker/manual-certs"
HOST_SYNC_FULL="/host/manual-certs/fullchain.pem"
HOST_SYNC_KEY="/host/manual-certs/privkey.pem"
mkdir -p "$CERTDIR"

# 旧部署迁移：若 env 提供 CERT_FULLCHAIN/CERT_PRIVKEY 且 /host/manual-certs 为空，
# 尝试把宿主源路径记录到 cert_host_*（供面板显示并生成同步脚本）。
migrate_legacy_manual_meta(){
  [ -z "$CERT_FULLCHAIN" ] && [ -z "$CERT_PRIVKEY" ] && return
  [ -f "$CONF" ] || return
  command -v python3 >/dev/null 2>&1 || return
  python3 - "$CONF" "$CERT_FULLCHAIN" "$CERT_PRIVKEY" <<'PY' 2>/dev/null || true
import json, sys
p, full, key = sys.argv[1], sys.argv[2], sys.argv[3]
c = json.load(open(p))
chg = False
if full and not c.get('cert_host_fullchain'):
    c['cert_host_fullchain'] = full; chg = True
if key and not c.get('cert_host_privkey'):
    c['cert_host_privkey'] = key; chg = True
if chg:
    json.dump(c, open(p, 'w'), indent=2, ensure_ascii=False)
    print('[entrypoint] 已从 env 迁移 manual 证书宿主源路径到 cert_host_*')
PY
}

if [ "$CERT_MODE" = "manual" ]; then
  log "证书来源：手动模式（Docker 同步目录 /host/manual-certs -> $CERTDIR/）"
  # 优先用固定同步目录；若为空但旧 env 源可读，回退用旧源做一次性同步
  if [ -s "$HOST_SYNC_FULL" ] && [ -s "$HOST_SYNC_KEY" ]; then
    log "检测到 /host/manual-certs 证书，调用 ansgo-sync-manual-cert 同步"
    if command -v /usr/local/bin/ansgo-sync-manual-cert >/dev/null 2>&1; then
      /usr/local/bin/ansgo-sync-manual-cert || log "WARN: ansgo-sync-manual-cert 退出码 $?"
    else
      cp -f "$HOST_SYNC_FULL" "$CERTDIR/fullchain.pem"
      cp -f "$HOST_SYNC_KEY" "$CERTDIR/privkey.pem"
      chmod 644 "$CERTDIR/fullchain.pem"; chmod 600 "$CERTDIR/privkey.pem"
    fi
  elif [ -f "$CERT_FULLCHAIN" ] && [ -f "$CERT_PRIVKEY" ]; then
    # 兼容旧部署：env 源仍可读（旧 compose 已 bind mount）
    log "WARN: /host/manual-certs 为空，回退使用 env 证书源（旧部署兼容）。建议在面板「证书管理」复制同步脚本到宿主执行。"
    cp -f "$CERT_FULLCHAIN" "$CERTDIR/fullchain.pem"
    cp -f "$CERT_PRIVKEY" "$CERTDIR/privkey.pem"
    chmod 644 "$CERTDIR/fullchain.pem"; chmod 600 "$CERTDIR/privkey.pem"
    log "已从 env 源同步到 $CERTDIR/（fullchain.pem + privkey.pem）"
  else
    log "ERROR: 未发现 manual 证书。请在面板「证书管理」复制「系统自动任务一键安装」或「宝塔计划任务脚本」到宿主执行后再重建容器。"
  fi
  # 无论哪种来源，运行路径始终指向卷内副本
  if [ -f "$CONF" ] && command -v python3 >/dev/null 2>&1; then
    python3 - "$CONF" "$CERTDIR" <<'PY' 2>/dev/null || true
import json, sys
p, certdir = sys.argv[1], sys.argv[2]
c = json.load(open(p))
c['cert_mode'] = 'manual'
c['cert_fullchain'] = certdir + '/fullchain.pem'
c['cert_privkey'] = certdir + '/privkey.pem'
json.dump(c, open(p, 'w'), indent=2, ensure_ascii=False)
print('[entrypoint] panel.json 已设为 manual 模式（运行路径指向卷内 ' + certdir + '/）')
PY
  fi
  migrate_legacy_manual_meta
else
  if [ ! -s "$CERTDIR/fullchain.pem" ] || [ ! -s "$CERTDIR/privkey.pem" ]; then
    log "生成自签占位证书（真实证书将由 acme.sh 后台签发覆盖）"
    openssl ecparam -genkey -name prime256v1 -out "$CERTDIR/privkey.pem" 2>/dev/null \
      || openssl genrsa -out "$CERTDIR/privkey.pem" 2048
    openssl req -new -x509 -days 365 -key "$CERTDIR/privkey.pem" \
      -out "$CERTDIR/fullchain.pem" -subj "/CN=${DOMAIN}" 2>/dev/null || true
  fi
fi

# v1.5.19: 幂等补全 panel.json 新增字段（容器重建后旧 panel.json 可能缺字段）
#   - server_ip（v1.5.18 新增，VPC 公网 IP，缺则空串走探测，Go 端兼容）
#   与裸金属 upgrade.sh 的补字段逻辑保持一致，避免 Docker 形态字段滞后。
if [ -f "$CONF" ] && command -v python3 >/dev/null 2>&1; then
  python3 -c "
import json
p = '$CONF'
c = json.load(open(p))
changed = False
# v1.5.18: server_ip（用户手动填写的公网 IP，默认空）
if 'server_ip' not in c:
    c['server_ip'] = ''
    changed = True
if changed:
    json.dump(c, open(p, 'w'), indent=2, ensure_ascii=False)
    print('[entrypoint] panel.json 已补全新字段: server_ip')
" 2>/dev/null || true
fi

# v1.5.31: 修复/规范化旧部署遗留的非标准 SS2022 密钥。
# v1.5.28+ 在 genconf 后执行 sing-box check；sing-box 的 SS2022 password 只接受标准 base64
#（带 padding），raw/url-safe/完全非法的 SS_KEY 都会导致保存/修复被 decode psk 阻断。
# 容器重建升级时若 SS 已启用：可解码的 raw/url-safe 密钥规范化为标准 base64；不可解码才重置。
python3 - <<'PY' 2>/dev/null || true
import base64, json, os
CONF='/etc/ansgo/panel.json'; SECRETS='/etc/ansgo/secrets.env'
try:
    cfg=json.load(open(CONF))
except Exception:
    cfg={}
if str(cfg.get('svc_ss_enabled','true')).lower() in ('true','1') and os.path.exists(SECRETS):
    pairs=[]; data={}
    for line in open(SECRETS):
        if '=' in line and not line.lstrip().startswith('#'):
            k,v=line.rstrip('\n').split('=',1); data[k]=v.strip().strip('"')
        pairs.append(line.rstrip('\n'))
    method=data.get('SS_METHOD') or cfg.get('ss_method') or '2022-blake3-aes-128-gcm'
    want=16 if method.startswith('2022-blake3-aes-128') else (32 if method.startswith('2022-blake3-aes-256') else 0)
    def normalize(k):
        if want <= 0: return k, False
        for alt in (k.strip(), k.strip()+'=', k.strip()+'==', k.strip()+'==='):
            for cand in (alt, alt.replace('-','+').replace('_','/')):
                try:
                    raw=base64.b64decode(cand.encode(), validate=True)
                    if len(raw)==want:
                        return base64.b64encode(raw).decode(), True
                    return '', False
                except Exception:
                    pass
        return '', False
    new, ok = normalize(data.get('SS_KEY',''))
    changed_msg = '规范化'
    if want and not ok:
        new=base64.b64encode(os.urandom(want)).decode(); changed_msg='非法，已自动重置'
    if want and (not ok or new != data.get('SS_KEY','')):
        seen=False; out=[]
        for line in pairs:
            if line.startswith('SS_KEY='):
                out.append('SS_KEY='+new); seen=True
            else:
                out.append(line)
        if not seen: out.append('SS_KEY='+new)
        open(SECRETS,'w').write('\n'.join(out).rstrip('\n')+'\n')
        os.chmod(SECRETS,0o600)
        print('[entrypoint] 已将 SS_KEY '+changed_msg+'为 sing-box 接受的标准 base64（SS 客户端需重新复制节点信息）')
PY

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
else
  # 非 --no-caddy 模式：caddy 始终启用（:443 伪装站是域名基础设施）
  # v1.5.17: 容器重建后 systemd 是全新状态，caddy 之前可能被 disable，需显式 enable
  log "默认模式：启用 caddy（:443 伪装站）"
  systemctl enable caddy.service 2>/dev/null || true
fi

# v1.5.17: sing-box 自动启停逻辑（修复容器重建后代理服务不自动恢复）
#   容器重建（docker compose up -d）后 systemd 是全新状态，sing-box.service 默认 disabled，
#   即使 panel.json 里 svc_*_enabled=true 也不会被拉起 → 用户拉新镜像后代理全断。
#   genconf 已据 svc 开关生成对应 inbound，此处按"是否有启用的 sing-box 服务"决定启停。
#   v1.5.26: 判断条件与 Go 端 needSB 一致：
#     ss/anytls/socks 任一启用 或 landings 数组里有任一 enabled 项 → 需要 sing-box
SB_NEED=0
for K in svc_ss_enabled svc_anytls_enabled svc_socks_enabled; do
  V=$(grep -o "\"$K\": *\"[^\"]*\"" "$CONF" 2>/dev/null | grep -o '"[^"]*"$' | tr -d '"')
  [ "$V" = "true" ] && SB_NEED=1
done
# v1.5.26: landings 数组里是否有 enabled=true（用 python 精确解析，避免粗 grep 误判）
LAND_EN=$(python3 -c "
import json
try:
    d=json.load(open('$CONF'))
    print('1' if any(str(L.get('enabled','false')).lower() in ('true','1') for L in d.get('landings',[])) else '0')
except Exception:
    print('0')
" 2>/dev/null || echo 0)
[ "$LAND_EN" = "1" ] && SB_NEED=1
if [ "$SB_NEED" = 1 ]; then
  log "检测到启用的 sing-box 服务：enable + restart sing-box"
  systemctl enable sing-box.service 2>/dev/null || true
  systemctl restart sing-box.service 2>/dev/null || true
else
  log "无启用的 sing-box 服务：sing-box 保持 disabled（面板内按需安装时再起）"
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
