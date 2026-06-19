#!/usr/bin/env sh
# shellcheck disable=SC2034
# =============================================================================
# acme.sh DNS API hook for Dynu — API Key auth (路径 A，默认)
#
# 与官方 dns_dynu.sh 用完全相同的 Dynu v2 端点，唯一区别：
#   官方用 OAuth2 Bearer token（需 Dynu_ClientId + Dynu_Secret）
#   本钩子用 Api-Key 请求头（只需 DYNU_API_KEY）
#
# 用法：
#   export DYNU_API_KEY="xxxxxxxx"
#   acme.sh --issue --dns dns_dynukey -d example.com
#
# 凭证会被 _saveaccountconf_mutable 持久化到 account.conf，续期无需重新提供。
# =============================================================================
dns_dynukey_info='Dynu.com (API Key auth)
Domains: dynu.com
Site: dynu.com
Docs: BV-LA private hook (API Key header)
Options:
 DYNU_API_KEY Dynu API Key (see Dynu 控制台 API Keys)
Issues: BV-LA deploy
'

Dynu_EndPoint="https://api.dynu.com/v2"

########  Public functions #####################

#Usage: dns_dynukey_add _acme-challenge.domain.com "XKrxpRBos...==
dns_dynukey_add() {
  fulldomain=$1
  txtvalue=$2

  DYNU_API_KEY="${DYNU_API_KEY:-$(_readaccountconf_mutable DYNU_API_KEY)}"
  if [ -z "$DYNU_API_KEY" ]; then
    _err "DYNU_API_KEY 未设置。请 export DYNU_API_KEY 或写入 account.conf。"
    return 1
  fi
  _saveaccountconf_mutable DYNU_API_KEY "$DYNU_API_KEY"

  _debug "Detect root zone for $fulldomain"
  if ! _dynukey_get_root "$fulldomain"; then
    _err "无效域名: $fulldomain"
    return 1
  fi
  _debug2 _dnsId "$_dnsId"
  _debug _node "$_node"

  _info "通过 Dynu API Key 创建 TXT 记录 ($fulldomain) -> $txtvalue"
  if ! _dynukey_rest POST "dns/$_dnsId/record" "{\"domainId\":\"$_dnsId\",\"nodeName\":\"$_node\",\"recordType\":\"TXT\",\"textData\":\"$txtvalue\",\"state\":true,\"ttl\":90}"; then
    return 1
  fi
  if ! _contains "$response" "200"; then
    _err "添加 TXT 记录失败。"
    _err "$response"
    return 1
  fi
  return 0
}

#Usage: dns_dynukey_rm _acme-challenge.domain.com "XKrxpRBos...==
dns_dynukey_rm() {
  fulldomain=$1
  txtvalue=$2

  DYNU_API_KEY="${DYNU_API_KEY:-$(_readaccountconf_mutable DYNU_API_KEY)}"
  if [ -z "$DYNU_API_KEY" ]; then
    _err "DYNU_API_KEY 未设置。"
    return 1
  fi

  _debug "Detect root zone for $fulldomain"
  if ! _dynukey_get_root "$fulldomain"; then
    _err "无效域名: $fulldomain"
    return 1
  fi

  _info "查询待删除 TXT 记录 ($fulldomain)"
  _dns_record_id=""
  if ! _dynukey_get_recordid "$txtvalue"; then
    _err "查询 TXT 记录失败。"
    return 1
  fi
  if [ -z "$_dns_record_id" ] || [ "$_dns_record_id" = "0" ]; then
    _info "未找到对应 TXT 记录，跳过删除。"
    return 0
  fi

  _info "删除 TXT 记录 id=$_dns_record_id"
  if ! _dynukey_rest DELETE "dns/$_dnsId/record/$_dns_record_id"; then
    return 1
  fi
  if ! _contains "$response" "200"; then
    _err "删除 TXT 记录失败。"
    _err "$response"
    return 1
  fi
  return 0
}

########  Private functions ####################

# 解析 fulldomain -> _dnsId(zone), _node(主机相对节点)
# 例: _acme-challenge.your-domain.com -> _dnsId=<zone_id>, _node=_acme-challenge
_dynukey_get_root() {
  domain=$1
  i=2
  p=1
  while true; do
    h=$(printf "%s" "$domain" | cut -d . -f "$i"-100)
    _debug h "$h"
    if [ -z "$h" ]; then
      return 1
    fi
    if ! _dynukey_rest GET "dns/getroot/$h"; then
      return 1
    fi
    if _contains "$response" "\"domainName\":\"$h\""; then
      _dnsId=$(printf "%s" "$response" | _egrep_o '"id":[0-9]*' | head -1 | cut -d : -f 2)
      _domain_name=$h
      _node=$(printf "%s" "$domain" | cut -d . -f 1-"$p")
      return 0
    fi
    p=$i
    i=$(_math "$i" + 1)
  done
  return 1
}

# 在 _dnsId zone 下查找 textData==$txtvalue 的 TXT 记录 id
_dynukey_get_recordid() {
  txtvalue=$1
  if ! _dynukey_rest GET "dns/$_dnsId/record"; then
    return 1
  fi
  if ! _contains "$response" "$txtvalue"; then
    _dns_record_id="0"
    return 0
  fi
  _dns_record_id=$(printf "%s" "$response" | sed -e 's/[^{]*\({[^}]*}\)[^{]*/\1\n/g' | grep "\"textData\":\"$txtvalue\"" | sed -e 's/.*"id":\([0-9]*\).*/\1/' | head -1)
  return 0
}

# 统一 REST 调用：Api-Key 头鉴权
_dynukey_rest() {
  _m=$1
  _ep="$2"
  _data="$3"
  _debug "$_ep"

  export _H1="Api-Key: $DYNU_API_KEY"
  export _H2="Content-Type: application/json"
  export _H3="Accept: application/json"

  if [ "$_data" ] || [ "$_m" = "DELETE" ]; then
    _debug _data "$_data"
    response="$(_post "$_data" "$Dynu_EndPoint/$_ep" "" "$_m")"
  else
    response="$(_get "$Dynu_EndPoint/$_ep")"
  fi

  if [ "$?" != "0" ]; then
    _err "请求失败: $_ep"
    return 1
  fi
  _debug2 response "$response"
  return 0
}
