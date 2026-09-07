#!/usr/bin/env bash
# =============================================================================
# ANS-GO Docker entrypoint panel.json 初始化测试（v1.5.37，TDD）
#
# 覆盖：
#   1. entrypoint 可提取 entrypoint_ensure_panel_json（含 JSON 转义）
#   2. PANEL_TITLE_IN / SERVER_IP_IN 环境变量写入 panel.json（默认旧语义）
#   3. 注入特殊字符（" 与 \）时 JSON 仍合法、值精确还原（env 来源不可信）
#   4. 已有 panel.json 不覆盖
#   5. 端到端：install.sh docker_gen_ansgo_env 产物 -> 模拟 compose 注入 ->
#      entrypoint 生成 -> panel.json 值与 CLI 输入一致（env 全链路语义）
#
# 隔离性：仅提取函数在临时目录执行，不启动容器/不触碰系统路径。
# =============================================================================
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL="$ROOT/install.sh"
ENTRY="$ROOT/deploy/docker/entrypoint.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
PASS=0 FAIL=0
ok(){ PASS=$((PASS+1)); printf '  ok   %s\n' "$1"; }
no(){ FAIL=$((FAIL+1)); printf '  FAIL %s\n' "$1"; }
note(){ printf '       %s\n' "$1"; }
extract_fn(){
  awk -v fn="$2" '
    !f && index($0, fn"(){")==1 { f=1 }
    f { print }
    f && /^\}$/ { exit }
  ' "$1"
}

echo "== E1 entrypoint 可测函数可提取 =="
for FN in ansgo_json_escape ansgo_pct_decode entrypoint_ensure_panel_json; do
  DEF="$(extract_fn "$ENTRY" "$FN")"
  if [ -n "$DEF" ] && printf '%s' "$DEF" | tail -1 | grep -q '^}$'; then ok "entrypoint.sh 可提取函数 $FN"; else no "entrypoint.sh 无法提取函数 $FN"; fi
done

# 导入 entrypoint 的两个函数（json 转义 + panel.json 生成）
ENTRY_FNS="$(extract_fn "$ENTRY" ansgo_json_escape)
$(extract_fn "$ENTRY" ansgo_pct_decode)
$(extract_fn "$ENTRY" entrypoint_ensure_panel_json)"
if ! eval "$ENTRY_FNS" 2>/dev/null || ! command -v entrypoint_ensure_panel_json >/dev/null; then
  echo "结果: PASS=$PASS FAIL=$FAIL"; exit 1
fi
log(){ :; }; export -f log 2>/dev/null || true

# 受控环境（避免随机端口/openssl）；变量名与 ansgo.env 键一致
export DOMAIN=your-domain.com PANEL_PORT=23456 SS_PORT=23457 ANYTLS_PORT=23458 \
       SOCKS_PORT=23459 NAIVE_PORT=23460 PANEL_USER=admin URL_PATH=/test0123/ \
       DISGUISE_PANEL='proxy:https://example.com' DISGUISE_NAIVE='proxy:https://example.com' \
       NO_CADDY=0 CERT_MODE=acme CERT_FULLCHAIN= CERT_PRIVKEY= \
       DYNU_API_KEY=fake DYNU_CLIENT_ID= DYNU_SECRET= EMAIL=t@example.com

echo "== E2 env 注入写入 + 默认旧语义 =="
J="$TMP/panel1.json"
ENC1="$(python3 -c 'import urllib.parse;print(urllib.parse.quote("节点 A（京 $100% #tag",safe=""))')"
PANEL_TITLE_ENCODED="$ENC1" SERVER_IP_IN='203.0.113.7' entrypoint_ensure_panel_json "$J"
if [ -f "$J" ] && python3 -c "
import json
c=json.load(open('$J'))
assert c['panel_title']=='节点 A（京 \$100% #tag', repr(c.get('panel_title'))
assert c['server_ip']=='203.0.113.7', repr(c.get('server_ip'))
assert c['domain']=='your-domain.com' and c['panel_port']==23456
assert c['landings']==[]
" 2>/dev/null; then ok "PANEL_TITLE_IN/SERVER_IP_IN 写入 panel.json（合法 JSON 精确还原）"; else no "新字段未正确写入 panel.json"; fi

J="$TMP/panel2.json"
PANEL_TITLE_ENCODED='' SERVER_IP_IN='' entrypoint_ensure_panel_json "$J"
if python3 -c "
import json
c=json.load(open('$J'))
assert c['panel_title']=='ANS-GO 管理面板', repr(c.get('panel_title'))
assert c['server_ip']=='', repr(c.get('server_ip'))
" 2>/dev/null; then ok "默认旧语义（title='ANS-GO 管理面板'，ip=''）"; else no "默认值偏离旧语义"; fi

echo "== E3 env 来源不可信时的 JSON 转义防御 =="
J="$TMP/panel3.json"
ENC3="$(python3 -c 'import urllib.parse;print(urllib.parse.quote("x\"y\\z",safe=""))')"
PANEL_TITLE_ENCODED="$ENC3" SERVER_IP_IN='' entrypoint_ensure_panel_json "$J"
if python3 -c "
import json
c=json.load(open('$J'))
assert c['panel_title']=='x\"y\\\\z', repr(c.get('panel_title'))
" 2>/dev/null; then ok "注入 x\"y\\z 生成合法 JSON 且值还原（entrypoint 层转义）"; else no "注入特殊字符破坏 JSON"; fi

echo "== E4 已有 panel.json 不覆盖 =="
J="$TMP/panel4.json"
printf '{"panel_title":"UserKept","panel_port":1}' > "$J"
PANEL_TITLE_ENCODED='NewTitleEncodedPlaceholder' entrypoint_ensure_panel_json "$J"
RC=$?
if [ $RC -ne 0 ] && python3 -c "
import json
c=json.load(open('$J'))
assert c['panel_title']=='UserKept' and c['panel_port']==1
" 2>/dev/null; then ok "已有 panel.json 不覆盖（返回非 0，内容原样）"; else no "已有 panel.json 被覆盖"; fi

echo "== E4.5 失败路径与 trim（Sentinel：失败不被吞 + 原子写 + 函数级 trim）=="
# 函数级 trim：ENCODED 解码后含首尾空白 -> panel.json 写 trim 后值
J="$TMP/panel-trim.json"
ENCT="$(python3 -c 'import urllib.parse;print(urllib.parse.quote("  NodeB  ",safe=""))')"
PANEL_TITLE_ENCODED="$ENCT" entrypoint_ensure_panel_json "$J"
if python3 -c "
import json
assert json.load(open('$J'))['panel_title']=='NodeB'
" 2>/dev/null; then ok "ENCODED 解码后 trim：'  NodeB  ' -> 'NodeB'"; else no "entrypoint 未 trim 首尾空白"; fi

# 失败路径 1：目录不可写
RO="$TMP/rodir"; mkdir -p "$RO"; chmod 500 "$RO"
entrypoint_ensure_panel_json "$RO/x.json" 2>"$TMP/ro.err"; RRC=$?
chmod 700 "$RO" 2>/dev/null
if [ "$RRC" -ne 0 ] && [ ! -f "$RO/x.json" ] && ! ls "$RO"/*.tmp* >/dev/null 2>&1; then
  ok "目录不可写：失败不被吞（rc=${RRC}），无输出/无 tmp 残留"
else
  no "目录不可写时失败被吞（rc=${RRC}）或残留 tmp"
fi

# 失败路径 2：JSON 校验拦截（PANEL_PORT 非数字）
J2="$TMP/panel-bad.json"
PANEL_PORT_SAVE="$PANEL_PORT"; PANEL_PORT=notanum
entrypoint_ensure_panel_json "$J2" 2>"$TMP/bad.err"; BRC=$?
PANEL_PORT="$PANEL_PORT_SAVE"
if [ "$BRC" -ne 0 ] && [ ! -f "$J2" ]; then
  ok "非法 JSON 被校验拦截，不落地坏文件"
else
  no "非法 JSON 未被拦截（rc=${BRC}）"
fi

# 调用点语义：entrypoint 首次生成失败必须中止容器初始化（不允许带病起 systemd）
if grep -q 'if ! entrypoint_ensure_panel_json' "$ENTRY" && grep -A2 'if ! entrypoint_ensure_panel_json' "$ENTRY" | grep -q 'exit'; then
  ok "entrypoint 调用点：生成失败中止容器初始化"
else
  no "entrypoint 调用点未中止（失败可能被吞继续起 systemd）"
fi

echo "== E5 端到端：install ansgo.env -> (模拟 compose env 注入) -> entrypoint panel.json =="
if eval "$(extract_fn "$INSTALL" ansgo_pct_encode)" 2>/dev/null && eval "$(extract_fn "$INSTALL" docker_gen_ansgo_env)" 2>/dev/null && command -v docker_gen_ansgo_env >/dev/null; then
  E="$TMP/ansgo.env"
  # install 侧生成（CLI 可读值含中文/空格/$/#/引号）
  PANEL_TITLE_IN='节点 A（京 $100% #tag "q"' SERVER_IP_IN='203.0.113.7' \
    DOMAIN=your-domain.com EMAIL=t@example.com PANEL_USER=admin \
    PANEL_PORT=23456 SS_PORT=23457 ANYTLS_PORT=23458 SOCKS_PORT=23459 NAIVE_PORT=23460 \
    DISGUISE_PANEL='proxy:https://example.com' DISGUISE_NAIVE='proxy:https://example.com' \
    DYNU_KEY=fake-key DYNU_CID= DYNU_SECRET= CERT_MODE=acme CERT_FILE= KEY_FILE= \
    SS_PASSWORD= ANYTLS_PASSWORD= ANYTLS_UUID_IN= SOCKS_USER_IN= SOCKS_PASSWORD= \
    NAIVE_USER_IN= NAIVE_PASSWORD= PANEL_PASSWORD_IN= URL_PATH_IN=/abcd1234/ NO_CADDY=0 \
    docker_gen_ansgo_env "$E"
  # 模拟 compose v2 dotenv 注入：逐行解析 KEY=VALUE 再 export 给 entrypoint
  J="$TMP/panel-e2e.json"
  if ( python3 - "$E" <<'PY' > "$TMP/env.exports"
import sys
for line in open(sys.argv[1]):
    line=line.rstrip('\n')
    s=line.strip()
    if not s or s.startswith('#') or '=' not in s: continue
    k,v=s.split('=',1)
    v=v.strip()
    i=v.find(' #')
    if i>=0: v=v[:i].rstrip()
    if len(v)>=2 and v[0]==v[-1] and v[0] in ('"',"'"): v=v[1:-1]
    print("export %s=%s" % (k, "'"+v.replace("'", "'\\''")+"'"))
PY
    . "$TMP/env.exports"
    entrypoint_ensure_panel_json "$J"
  ) 2>/dev/null; then :; fi
  if python3 -c "
import json
c=json.load(open('$J'))
assert c['panel_title']=='节点 A（京 \$100% #tag \"q\"', repr(c.get('panel_title'))
assert c['server_ip']=='203.0.113.7', repr(c.get('server_ip'))
assert c['url_path']=='/abcd1234/'
assert c['panel_port']==23456
" 2>/dev/null; then ok "全链路还原：CLI 可读标题（含 \$ # 引号）经 ansgo.env ENCODED -> entrypoint 解码 -> panel.json 可读值一致"; else no "端到端值不一致（env 语义在某层丢失）"; fi
else
  no "install.sh docker_gen_ansgo_env 不可用，E5 跳过（install 测试另有覆盖）"
fi

echo
echo "结果: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
