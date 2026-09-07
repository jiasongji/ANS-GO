#!/usr/bin/env bash
# =============================================================================
# ANS-GO install.sh 新参数与初始化链路测试（v1.5.37，TDD）
#
# 覆盖：
#   1. --panel-title / --server-ip 参数解析 + 校验（危险字符/格式/长度拒绝）
#   2. 交互式只引导 Dynu API Key（OAuth 仅 CLI 兼容；缺凭证会明确中止）
#   3. 裸金属 panel.json 生成函数（JSON 转义 / 默认旧语义 / 已有文件不覆盖）
#   4. Docker ansgo.env 生成函数（env 特殊字符语义：两种 dotenv 解析器还原）
#
# 隔离性：全程非 root（install.sh 走到 root 校验即退出，无下载/安装副作用）；
#         生成函数经提取 + 路径参数输出到临时目录，不触碰系统路径。
# 用法：bash scripts/test-install-params.sh   （在仓库根目录或任意目录均可）
# =============================================================================
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL="$ROOT/install.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
PASS=0 FAIL=0
ok(){ PASS=$((PASS+1)); printf '  ok   %s\n' "$1"; }
no(){ FAIL=$((FAIL+1)); printf '  FAIL %s\n' "$1"; }
note(){ printf '       %s\n' "$1"; }

# 从脚本提取 bash 函数定义（约定：fnname(){ 顶格起，结束 } 顶格独立一行）
extract_fn(){
  awk -v fn="$2" '
    !f && index($0, fn"(){")==1 { f=1 }
    f { print }
    f && /^\}$/ { exit }
  ' "$1"
}

# ---- 探针运行：NONINT + 齐全参数 → 应顺利走到「需 root 运行」环境校验退出 ----
# （到达该输出 = 参数被接受、校验通过、交互收集不再阻塞；全程无副作用）
run_probe(){
  bash "$INSTALL" --domain your-domain.com --dynu-key fake-key-for-test \
    --email t@example.com --non-interactive "$@" 2>&1
}

echo "== G1 参数解析与校验（探针=到达『需 root 运行』）=="

OUT="$(run_probe --panel-title '节点 A（京' --server-ip 203.0.113.7)"; RC=$?
if printf '%s' "$OUT" | grep -q '需 root 运行'; then ok "--panel-title 合法值（中文/空格/括号）+ --server-ip IPv4 被接受"; else no "--panel-title/--server-ip 未被接受（输出：$(printf '%s' "$OUT" | head -2 | tr '\n' ' ')）"; fi
OUT="$(run_probe --panel-title NodeB --server-ip 2001:db8::1)"; RC=$?
if printf '%s' "$OUT" | grep -q '需 root 运行'; then ok "--server-ip IPv6（2001:db8::1）被接受"; else no "--server-ip IPv6 未被接受"; fi
OUT="$(run_probe --panel-title NodeB)"; RC=$?
if printf '%s' "$OUT" | grep -q '需 root 运行'; then ok "缺省 --server-ip 走旧语义（空值，Go 启动自动探测）"; else no "缺省 --server-ip 被拒"; fi
OUT="$(run_probe --panel-title NodeB --server-ip 999.1.1.1)"
if printf '%s' "$OUT" | grep -q -- '--server-ip'; then ok "--server-ip=999.1.1.1 被校验拒绝"; else no "--server-ip=999.1.1.1 未被拒绝"; fi
OUT="$(run_probe --panel-title NodeB --server-ip not-an-ip)"
if printf '%s' "$OUT" | grep -q -- '--server-ip'; then ok "--server-ip=not-an-ip 被校验拒绝"; else no "--server-ip=not-an-ip 未被拒绝"; fi
# v1.5.37 修订：无效 IPv6 必须拒绝（三连冒号/仅冒号/双::/前后单冒号/超长组/非hex）
for BAD_IP in '2001:::1' ':::' ':' ':::1' '1::2::3' ':2001:db8::1' '2001:db8::1:' 'gg::1' '12345::' '1.2.3' '::ffff:1.2.3.4x'; do
  OUT="$(run_probe --panel-title NodeB --server-ip "$BAD_IP")"
  if printf '%s' "$OUT" | grep -q -- '--server-ip'; then ok "--server-ip=$BAD_IP 被校验拒绝"; else no "--server-ip=$BAD_IP 未被拒绝（须严格 IPv6 解析）"; fi
done
# 合法 IPv6 边界（ipaddress 语义：:: / ::1 / fe80::1 / IPv4-mapped）
for OK_IP in '::' '::1' 'fe80::1' '::ffff:1.2.3.4'; do
  OUT="$(run_probe --panel-title NodeB --server-ip "$OK_IP")"
  if printf '%s' "$OUT" | grep -q '需 root 运行'; then ok "--server-ip=$OK_IP 被接受"; else no "--server-ip=$OK_IP 被误拒"; fi
done
# v1.5.37 修订：特殊字符标题属合法可读输入（Docker 侧 percent-encoding transport），必须接受
for OK_TITLE in 'a$b' 'a#b' 'a"b' "a'b" 'a\b' 'a`b' 'a b' '100%正品' '节点 A（京'; do
  OUT="$(run_probe --panel-title "$OK_TITLE" --server-ip 203.0.113.7)"
  if printf '%s' "$OUT" | grep -q '需 root 运行'; then ok "--panel-title 特殊字符 '$OK_TITLE' 被接受（ENCODED transport）"; else no "--panel-title '$OK_TITLE' 被拒绝（应走编码而非拒绝）"; fi
done
OUT="$(run_probe --panel-title '  NodeB  ' --server-ip 203.0.113.7)"
if printf '%s' "$OUT" | grep -q '需 root 运行'; then ok "--panel-title 首尾空白被接受（统一 trim）"; else no "--panel-title 首尾空白被误拒"; fi
OUT="$(run_probe --panel-title '   ' --server-ip 203.0.113.7)"
if printf '%s' "$OUT" | grep -q -- '--panel-title' && ! printf '%s' "$OUT" | grep -q '未知参数'; then ok "--panel-title 纯空白被拒绝（trim 后为空）"; else no "--panel-title 纯空白未被拒绝"; fi
OUT="$(run_probe --panel-title "$(printf 'x%.0s' {1..65})" --server-ip 203.0.113.7)"
if printf '%s' "$OUT" | grep -q -- '--panel-title' && ! printf '%s' "$OUT" | grep -q '未知参数'; then ok "--panel-title 65+ 字符被拒绝（上限 64）"; else no "--panel-title 65+ 字符未被校验拒绝"; fi
BAD_NL="$(printf 'a\nb')"
OUT="$(run_probe --panel-title "$BAD_NL" --server-ip 203.0.113.7)"
if printf '%s' "$OUT" | grep -q -- '--panel-title' && ! printf '%s' "$OUT" | grep -q '未知参数'; then ok "--panel-title 含换行被拒绝"; else no "--panel-title 含换行未被校验拒绝"; fi

HELP="$(bash "$INSTALL" --help 2>&1)"
if printf '%s' "$HELP" | grep -q -- '--panel-title'; then ok "--help 文档含 --panel-title"; else no "--help 缺 --panel-title"; fi
if printf '%s' "$HELP" | grep -q -- '--server-ip'; then ok "--help 文档含 --server-ip"; else no "--help 缺 --server-ip"; fi

OUT="$(run_probe --dynu-client-id legacy-cid --dynu-secret legacy-sec)"
if printf '%s' "$OUT" | grep -q '需 root 运行'; then ok "[回归] 旧 OAuth CLI 参数（--dynu-client-id/--dynu-secret）仍兼容"; else no "旧 OAuth CLI 参数被拒绝"; fi
HELP="$(bash "$INSTALL" --help 2>&1)"
if printf '%s' "$HELP" | grep -q -- '--dynu-client-id'; then ok "[回归] --help 仍含 --dynu-client-id（OAuth CLI 兼容）"; else no "--help 丢失 --dynu-client-id"; fi

# 注：root 检查位于交互收集之前，非 root 探针无法触达交互段；用结构断言锁定
#     Sentinel 语义：acme 交互必填 API Key（ask_req，NONINT 缺失即中止），
#     OAuth 仅作完整对备选（CLI --dynu-client-id/--dynu-secret 兼容）
if grep -q 'ask_req "Dynu API Key' "$INSTALL"; then
  ok "交互必填引导 Dynu API Key（ask_req；缺 API Key 且无完整 OAuth 时中止）"
else
  no "交互未必填引导 API Key（缺凭证不得静默继续）"
fi
if grep -q 'Dynu OAuth Client ID' "$INSTALL"; then
  ok "OAuth 完整对备选输入保留（兼容）"
else
  no "OAuth 备选输入丢失"
fi

echo "== G2 可测函数可提取（fnname(){ ... 顶格} 约定）=="
for FN in ansgo_json_escape ansgo_pct_encode ansgo_ip_check metal_ensure_panel_json docker_gen_ansgo_env; do
  DEF="$(extract_fn "$INSTALL" "$FN")"
  if [ -n "$DEF" ] && printf '%s' "$DEF" | tail -1 | grep -q '^}$'; then ok "install.sh 可提取函数 ${FN}"; else no "install.sh 无法提取函数 ${FN}（不存在或格式不符）"; fi
done

echo "== G2.5 IP 严格校验（ansgo_ip_check：python ipaddress 优先 / 无 python shell 兜底）=="
if eval "$(extract_fn "$INSTALL" ansgo_ip_check)" 2>/dev/null && command -v ansgo_ip_check >/dev/null; then
  IP_BAD_SHELL='2001:::1 ::: : :::1 1::2::3 :2001:db8::1 2001:db8::1: gg::1 12345:: 1.2.3 999.1.1.1'
  IP_OK_SHELL='203.0.113.7 0.0.0.0 255.255.255.255 2001:db8::1 :: ::1 fe80::1 a:: 1:2:3:4:5:6:7:8'
  for ip in $IP_BAD_SHELL; do
    if ansgo_ip_check "$ip" 2>/dev/null; then no "[python] ipaddress 应拒绝 '$ip'"; else ok "[python] 拒绝 '$ip'"; fi
    if ANSGO_IP_FORCE_SHELL=1 ansgo_ip_check "$ip" 2>/dev/null; then no "[shell 兜底] 应拒绝 '$ip'（无 python 也不崩）"; else ok "[shell 兜底] 拒绝 '$ip'"; fi
  done
  for ip in $IP_OK_SHELL; do
    ansgo_ip_check "$ip" 2>/dev/null && ok "[python] 接受 '$ip'" || no "[python] 误拒 '$ip'"
    ANSGO_IP_FORCE_SHELL=1 ansgo_ip_check "$ip" 2>/dev/null && ok "[shell 兜底] 接受 '$ip'" || no "[shell 兜底] 误拒 '$ip'"
  done
else
  no "ansgo_ip_check 不可用，G2.5 全组跳过"
fi

echo "== G3 JSON 字符串转义（ansgo_json_escape）=="
if eval "$(extract_fn "$INSTALL" ansgo_json_escape)" 2>/dev/null && command -v ansgo_json_escape >/dev/null; then
  [ "$(ansgo_json_escape 'plain')" = 'plain' ] && ok "plain 原样" || no "plain 被改写"
  [ "$(ansgo_json_escape 'a"b')" = 'a\"b' ] && ok '双引号转义 a"b -> a\"b' || no '双引号转义错误'
  [ "$(ansgo_json_escape 'a\b')" = 'a\\b' ] && ok '反斜杠转义 a\b -> a\\b' || no '反斜杠转义错误'
  [ "$(ansgo_json_escape 'a"b\c')" = 'a\"b\\c' ] && ok '混合转义 a"b\c' || no '混合转义错误'
  [ "$(ansgo_json_escape '节点 A')" = '节点 A' ] && ok "中文/空格原样" || no "中文被改写"
  [ "$(printf 'a	b' | { read -r v; ansgo_json_escape "$v"; } 2>/dev/null)" = 'a\tb' ] 2>/dev/null
  TABV="$(printf 'a	b')"
  [ "$(ansgo_json_escape "$TABV")" = 'a\tb' ] && ok "TAB 转义为 \t（env 来源可含 TAB 不破坏 JSON）" || no "TAB 未正确转义"
  CTLV="$(printf 'ab')"
  [ "$(ansgo_json_escape "$CTLV")" = 'a\u0001b' ] && ok "控制字符 0x01 转义为 \u0001" || no "控制字符未正确转义"
else
  no "ansgo_json_escape 不可用，G3 全组跳过"; note "（函数缺失或不可执行）"
fi

echo "== G4 裸金属 panel.json 生成（metal_ensure_panel_json）=="
if eval "$(extract_fn "$INSTALL" ansgo_json_escape)" 2>/dev/null && eval "$(extract_fn "$INSTALL" metal_ensure_panel_json)" 2>/dev/null && command -v metal_ensure_panel_json >/dev/null; then
  log(){ :; }; warn(){ :; }; export -f log warn 2>/dev/null || true
  # 函数依赖的环境（全部走受控值，避免触发 openssl/随机）
  export DOMAIN=your-domain.com PANEL_PORT=23456 SS_PORT=23457 ANYTLS_PORT=23458 \
         SOCKS_PORT=23459 NAIVE_PORT=23460 PANEL_USER=admin DISGUISE_PANEL='proxy:https://example.com' \
         DISGUISE_NAIVE='proxy:https://example.com' CERT_MODE=acme CERT_FILE= KEY_FILE= \
         DYNU_KEY=fake DYNU_CID= DYNU_SECRET= EMAIL=t@example.com NO_CADDY=0 URL_PATH_IN=/test0123/

  J="$TMP/panel1.json"
  PANEL_TITLE_IN='节点 A（京' SERVER_IP_IN='203.0.113.7' metal_ensure_panel_json "$J"
  if [ $? -eq 0 ] && python3 -c "
import json,sys
c=json.load(open('$J'))
assert c['panel_title']=='节点 A（京', c.get('panel_title')
assert c['server_ip']=='203.0.113.7', c.get('server_ip')
assert c['domain']=='your-domain.com'
assert c['landings']==[]
assert c['url_path']=='/test0123/'
" 2>/dev/null; then ok "指定 title/ip 写入 panel.json（合法 JSON，值精确还原）"; else no "指定 title/ip 未正确写入 panel.json"; fi

  J="$TMP/panel2.json"
  PANEL_TITLE_IN='' SERVER_IP_IN='' metal_ensure_panel_json "$J"
  if python3 -c "
import json
c=json.load(open('$J'))
assert c['panel_title']=='ANS-GO 管理面板', c.get('panel_title')
assert c['server_ip']=='', c.get('server_ip')
" 2>/dev/null; then ok "默认保持旧语义（title='ANS-GO 管理面板'，ip=''）"; else no "默认值偏离旧语义"; fi

  # 已有配置不覆盖（行为级：预置用户改过的文件 -> 函数不得动它）
  J="$TMP/panel3.json"
  printf '{"panel_title":"UserKept","server_ip":"198.51.100.9"}' > "$J"
  PANEL_TITLE_IN='NewTitle' SERVER_IP_IN='203.0.113.7' metal_ensure_panel_json "$J"
  RC=$?
  if [ $RC -ne 0 ] && python3 -c "
import json
c=json.load(open('$J'))
assert c['panel_title']=='UserKept' and c['server_ip']=='198.51.100.9'
" 2>/dev/null; then ok "已有 panel.json 不覆盖（返回非 0，内容原样）"; else no "已有 panel.json 被覆盖"; fi

  # 函数级 trim（env/CLI 来源统一去首尾空白）
  J="$TMP/panel-trim.json"
  PANEL_TITLE_IN='  NodeB  ' SERVER_IP_IN='' metal_ensure_panel_json "$J"
  if python3 -c "
import json
assert json.load(open('$J'))['panel_title']=='NodeB'
" 2>/dev/null; then ok "函数级 trim：'  NodeB  ' -> 'NodeB'"; else no "函数未 trim 首尾空白"; fi

  # 失败路径 1：目标目录不可写 → 必须失败（非 0）、无输出文件、无 .tmp 残留
  RO="$TMP/rodir"; mkdir -p "$RO"; chmod 500 "$RO"
  PANEL_TITLE_IN=NodeB SERVER_IP_IN='' metal_ensure_panel_json "$RO/x.json" 2>"$TMP/ro.err"; RRC=$?
  chmod 700 "$RO" 2>/dev/null
  if [ "$RRC" -ne 0 ] && [ ! -f "$RO/x.json" ] && ! ls "$RO"/*.tmp* >/dev/null 2>&1; then
    ok "目录不可写：失败不被吞（rc=${RRC}），无输出/无 tmp 残留"
  else
    no "目录不可写时失败被吞（rc=${RRC}）或残留 tmp"
  fi

  # 失败路径 2：JSON 校验拦截非法产物（PANEL_PORT 非数字 -> heredoc 产出非法 JSON）
  J2="$TMP/panel-bad.json"
  PANEL_PORT_SAVE="$PANEL_PORT"; PANEL_PORT=notanum
  PANEL_TITLE_IN=NodeB SERVER_IP_IN='' metal_ensure_panel_json "$J2" 2>"$TMP/bad.err"; BRC=$?
  PANEL_PORT="$PANEL_PORT_SAVE"
  if [ "$BRC" -ne 0 ] && [ ! -f "$J2" ]; then
    ok "非法 JSON 被校验拦截（写 tmp 校验失败即中止，不落地坏文件）"
  else
    no "非法 JSON 未被拦截（rc=${BRC}，文件存在：$([ -f "$J2" ] && echo yes || echo no)）"
  fi

  # 转义防御：绕过 CLI 校验的注入值（模拟 env 来历不明）也不能破坏 JSON
  J="$TMP/panel4.json"
  PANEL_TITLE_IN='x"y\z' SERVER_IP_IN='' metal_ensure_panel_json "$J"
  if python3 -c "
import json
c=json.load(open('$J'))
assert c['panel_title']=='x\"y\\\\z', repr(c.get('panel_title'))
" 2>/dev/null; then ok "注入 x\"y\\z 经 JSON 转义后合法还原（分层防御）"; else no "注入特殊字符破坏 JSON 或值丢失"; fi
else
  no "metal_ensure_panel_json 不可用，G4 全组跳过"
fi
# 调用点语义（Sentinel）：已存在 -> warn 保留；生成失败 -> 必须中止安装（不允许把失败当已存在）
if grep -q 'elif ! metal_ensure_panel_json' "$INSTALL" && grep -A3 'elif ! metal_ensure_panel_json' "$INSTALL" | grep -q 'exit'; then
  ok "裸金属调用点：生成失败中止安装（已有文件单独 warn 保留）"
else
  no "裸金属调用点未区分已有/失败（失败可能被当已存在继续跑）"
fi

echo "== G5 Docker ansgo.env 生成（PANEL_TITLE_ENCODED percent-transport + 双解析器还原）=="
if eval "$(extract_fn "$INSTALL" ansgo_pct_encode)" 2>/dev/null && eval "$(extract_fn "$INSTALL" docker_gen_ansgo_env)" 2>/dev/null && command -v docker_gen_ansgo_env >/dev/null; then
  export DOMAIN=your-domain.com EMAIL=t@example.com PANEL_USER=admin PANEL_PORT=23456 \
         SS_PORT=23457 ANYTLS_PORT=23458 SOCKS_PORT=23459 NAIVE_PORT=23460 \
         DISGUISE_PANEL='proxy:https://example.com' DISGUISE_NAIVE='proxy:https://example.com' \
         DYNU_KEY=fake-key DYNU_CID= DYNU_SECRET= CERT_MODE=acme CERT_FILE= KEY_FILE= \
         SS_PASSWORD= ANYTLS_PASSWORD= ANYTLS_UUID_IN= SOCKS_USER_IN= SOCKS_PASSWORD= \
         NAIVE_USER_IN= NAIVE_PASSWORD= PANEL_PASSWORD_IN= URL_PATH_IN=/abcd1234/ NO_CADDY=0
  # 标题含全套危险字符（$ # 引号 反斜杠 反引号 空格 % 中文）——可读值作为唯一输入
  TITLE_TEST='节点 A（京 $100% #tag "q" '\''sq'\'' \b `tick`'
  E="$TMP/ansgo.env"
  PANEL_TITLE_IN="$TITLE_TEST" SERVER_IP_IN='203.0.113.7' docker_gen_ansgo_env "$E"
  if [ -s "$E" ]; then ok "ansgo.env 生成成功"; else no "ansgo.env 未生成"; fi
  EXPECT_ENC="$(python3 -c 'import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1],safe=""))' "$TITLE_TEST")"
  # 语义断言 1：env 写入 ENCODED 键（明确编码语义），不再写明文 PANEL_TITLE_IN
  if grep -qx "PANEL_TITLE_ENCODED=${EXPECT_ENC}" "$E" && ! grep -q '^PANEL_TITLE_IN=' "$E"; then
    ok "PANEL_TITLE_ENCODED 写入 percent 值且无明文 PANEL_TITLE_IN 键"
  else
    no "env 未正确写 PANEL_TITLE_ENCODED（或残留明文 PANEL_TITLE_IN 键）"
  fi
  grep -qx 'SERVER_IP_IN=203.0.113.7' "$E" && ok "SERVER_IP_IN 明文保留（IP 经校验值域天然安全）" || no "SERVER_IP_IN 行异常"
  # 语义断言 2：ENCODED 值仅含跨解析器安全字符（compose v2 对 env_file 做 $ 插值与 ' #' 截断，
  #             percent 值不含这些字符；v1 逐行 split 全字面）
  if python3 - "$E" <<'PY' 2>/dev/null
import sys, re, urllib.parse
enc=None
for line in open(sys.argv[1]):
    line=line.rstrip('\n')
    if line.startswith('PANEL_TITLE_ENCODED='):
        enc=line.split('=',1)[1]
assert enc is not None, 'PANEL_TITLE_ENCODED 行缺失'
assert re.fullmatch(r'[A-Za-z0-9%_.\-]*', enc), '编码值含跨解析器危险字符: %r' % enc
PY
  then ok "ENCODED 值字符集 [A-Za-z0-9%_.-]（跨 v1/v2 解析器安全）"; else no "ENCODED 值含危险字符"; fi
  # 语义断言 3：compose v1（逐行 split）与 v2 dotenv（引号剥离/行内注释）两代解析器
  #             读出的 ENCODED 值均原样，unquote 后 == 原始可读标题
  if python3 - "$E" "$TITLE_TEST" <<'PY' 2>/dev/null
import sys, urllib.parse
raw=open(sys.argv[1]).read()
def parse_v1(t):
    d={}
    for line in t.splitlines():
        if '=' in line and not line.lstrip().startswith('#'):
            k,v=line.split('=',1); d[k]=v
    return d
def parse_v2(t):
    d={}
    for line in t.splitlines():
        s=line.strip()
        if not s or s.startswith('#') or '=' not in s: continue
        k,v=s.split('=',1); v=v.strip()
        i=v.find(' #')
        if i>=0: v=v[:i].rstrip()
        if len(v)>=2 and v[0]==v[-1] and v[0] in ('"',"'"): v=v[1:-1]
        d[k]=v
    return d
want=sys.argv[2]
for name,d in (('v1',parse_v1(raw)),('v2',parse_v2(raw))):
    enc=d.get('PANEL_TITLE_ENCODED')
    assert enc is not None, name+': ENCODED 缺失'
    assert urllib.parse.unquote(enc)==want, '%s: 还原失配 %r' % (name, urllib.parse.unquote(enc))
PY
  then ok "两代 dotenv 解析器下 ENCODED 值原样，unquote == 原始标题（含 \$ # 引号 反引号 空格 中文 %）"; else no "双解析器还原失败"; fi
else
  no "docker_gen_ansgo_env / ansgo_pct_encode 不可用，G5 全组跳过"
fi

echo "== G6 真实 docker compose config 验证（非自制解析器）=="
if [ -s "$TMP/ansgo.env" ]; then
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    C="$TMP/compose"; mkdir -p "$C"; cp "$TMP/ansgo.env" "$C/ansgo.env"
    printf 'services:\n  t:\n    image: busybox\n    env_file: [ansgo.env]\n    command: ["true"]\n' > "$C/compose.yaml"
    if (cd "$C" && docker compose config 2>/dev/null) | grep -q 'PANEL_TITLE_ENCODED:'; then
      VAL="$( (cd "$C" && docker compose config 2>/dev/null) | sed -n 's/^.*PANEL_TITLE_ENCODED: *//p' | head -1 | sed "s/^'//;s/'$//")"
      if [ -n "$VAL" ] && [ "$(python3 -c 'import urllib.parse,sys;print(urllib.parse.unquote(sys.argv[1]))' "$VAL")" = "$TITLE_TEST" ]; then
        ok "真实 docker compose config：ENCODED 值原样进入容器环境，unquote == 原始标题"
      else
        no "真实 docker compose config 解析后标题还原失配（值：$VAL）"
      fi
    else
      no "真实 docker compose config 未输出 PANEL_TITLE_ENCODED（env 行被拒或被吞）"
    fi
  else
    note "SKIP: 本机无 docker compose，G6 真实链路验证跳过（G5 双解析器已覆盖语义）"
  fi
else
  no "G5 未生成 ansgo.env，G6 跳过"
fi

echo
echo "结果: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
