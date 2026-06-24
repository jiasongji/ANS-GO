# 多落地服务（Multi-Landing Services）设计

- **版本**：v1.5.26
- **日期**：2026-06-24
- **状态**：待实现
- **关联**：AGENTS.md §5.5（落地服务）、§6.4（落地服务页）、§9（部署架构）

## 1. 背景与目标

### 1.1 现状（单落地服务）
当前「落地服务」是硬编码的单个 AnyTLS-2：
- `panel.json` 扁平字段：`group2_enabled` / `anytls2_port` / `naive2_port`(legacy) / `ss_landing_enabled` / `ss_landing_host` / `ss_landing_port` / `ss_landing_method` / `ss_landing_password`
- `secrets.env`：`ANYTLS2_PASS` / `ANYTLS2_UUID`
- sing-box：1 个 `anytls-in2` inbound + 1 个 `ss-out` outbound + 1 条 route 规则
- Go：`landingHandler`（远端 SS）+ `group2Handler`（AnyTLS-2 启用/端口）
- `ansgo-admin`：`regen2` + `group2 enable/disable`
- 前端：2 张固定卡片（AnyTLS-2 卡 + 远端 SS 落地卡）

### 1.2 新需求
1. 落地服务从「单个」变为「**可创建多个**」，每个有独立端口、独立 AnyTLS 凭证、独立远端落地配置。
2. 每个落地服务的「远端落地出口」**独立开关**，支持 **SS** 和 **SOCKS5** 两种远端协议（原仅 SS）。
3. Web 后台每个落地服务（inbound）与它对应的远端落地服务器（outbound）在**同一区配置**。
4. 现有单落地服务**自动迁移**为列表第 1 项，老用户升级无感。

### 1.3 不变项（架构约束）
- 落地代理入站协议**固定 AnyTLS**（与现状一致；不做「入站可选协议」以免与「服务管理」页的 ss/anytls/socks 概念重叠）。
- 落地数量**不设上限**（每个落地 = sing-box 一个 inbound+outbound，用户自负端口占用）。
- `panel.json` 仍是**单一事实来源**；`secrets.env` 仅存 anytls 凭证（pass+uuid）。
- caddy（NaiveProxy）**不参与落地**（caddy/sing-box 进程隔离约束不变）。

## 2. 数据模型

### 2.1 `Config` 结构体（`main.go`）

新增 `LandingService` 类型 + `Landings` 数组字段；**移除**旧的扁平落地字段：

```go
// LandingService 一个落地服务 = 一个 anytls 入站 + 可选一个远端出站
type LandingService struct {
    ID      string `json:"id"`       // 内部标识，如 "1","2"，用作 tag 后缀 + secrets key 前缀
    Name    string `json:"name"`     // 用户可读名称，如 "香港落地"
    Enabled bool   `json:"enabled"`  // 是否启用该 anytls 入站
    Port    int    `json:"port"`     // anytls 入站端口

    // 远端落地出口（独立开关；关闭则该落地走 direct）
    RemoteEnabled  bool   `json:"remote_enabled"`
    RemoteType     string `json:"remote_type"`     // "ss" | "socks"
    RemoteHost     string `json:"remote_host"`
    RemotePort     int    `json:"remote_port"`
    RemoteMethod   string `json:"remote_method"`   // SS 加密方式（socks5 留空）
    RemotePassword string `json:"remote_password"` // SS 密钥 / socks5 密码（明文存 panel.json，与现状 SSLandingPassword 一致）
    RemoteUser     string `json:"remote_user"`     // socks5 用户名（SS 留空）
}

type Config struct {
    // ... 既有字段不变 ...
    // 新增（替代旧的 group2_*/ss_landing_* 字段）：
    Landings []LandingService `json:"landings"`
    // 移除：Group2Enabled / AnyTLS2Port / Naive2Port /
    //       SSLandingEnabled / SSLandingHost / SSLandingPort /
    //       SSLandingMethod / SSLandingPassword
}
```

### 2.2 `secrets.env` 凭证 key 规则
- `LANDING_<ID>_PASS`（anytls 密码，明文）
- `LANDING_<ID>_UUID`（anytls 用户名，UUID）

旧 `ANYTLS2_PASS` / `ANYTLS2_UUID` 在 `upgrade.sh` 迁移时 rename 为 `LANDING_1_PASS` / `LANDING_1_UUID`。

### 2.3 ID 分配规则
- 新建落地服务时，`id = str(max(现有 int(id)) + 1)`；无现有则 `"1"`。
- 删除不回收 id（避免 tag/inbound 残留引用混乱；id 是字符串无所谓数值连续）。
- 迁移时老落地服务的 id 固定为 `"1"`。

### 2.4 安全模型（不变）
- `panel.json` 权限 0600 root-only（现状已是），`RemotePassword` 明文存储与现状 `SSLandingPassword` 一致，不改变安全边界。
- `secrets.env` 同样 0600 root-only。

## 3. `ansgo-genconf`（Python）改动

替换现有的 `anytls-in2` + `ss-out` 单一逻辑为遍历 `landings` 数组。

### 3.1 配置读取
```python
landings = cfg.get("landings", [])  # list of dict
```
移除旧的 `GROUP2_ENABLED` / `ANYTLS2_PORT` / `LANDING_*`（远端 SS）扁平变量读取。

### 3.2 sing-box 生成（核心循环）
```python
for L in landings:
    if not L.get("enabled"):
        continue
    lid = str(L["id"])
    in_tag  = f"landing-in-{lid}"
    out_tag = f"landing-out-{lid}"

    # 凭证从 secrets 读（缺失则报错退出，同现状 anytls2 逻辑）
    lp = sec(f"LANDING_{lid}_PASS")
    lu = sec(f"LANDING_{lid}_UUID")

    # 1) anytls inbound
    inbounds.append({
        "type": "anytls", "tag": in_tag,
        "listen": "::", "listen_port": int(L["port"]),
        "users": [{"name": lu, "password": lp}],
        "tls": {"enabled": True, "server_name": DOMAIN,
                "certificate_path": FULLCHAIN, "key_path": PRIVKEY},
    })

    # 2) 远端 outbound（仅 remote_enabled 且配置完整时）
    r = L.get("remote_enabled") and L.get("remote_host") and L.get("remote_port")
    if r:
        rtype = L.get("remote_type", "ss")
        if rtype == "socks":
            outbounds.append({
                "type": "socks", "tag": out_tag,
                "server": L["remote_host"], "server_port": int(L["remote_port"]),
                "username": L.get("remote_user", ""),
                "password": L.get("remote_password", ""),
            })
        else:  # ss（默认）
            outbounds.append({
                "type": "shadowsocks", "tag": out_tag,
                "server": L["remote_host"], "server_port": int(L["remote_port"]),
                "method": L.get("remote_method", "2022-blake3-aes-128-gcm"),
                "password": L.get("remote_password", ""),
            })
        rules.append({"inbound": [in_tag], "outbound": out_tag})
    # remote_enabled=false → 该落地走 direct（不加 outbound/rule，sing-box 默认 direct）
```

### 3.3 端口冲突检测（`check_port_conflicts`）
遍历所有 `enabled` 落地的 `port` 加入 `sb_ports` 检测，错误信息含 landing 名称。

### 3.4 校验层级（保持现状原则）
- `genconf` **不做 SS2022 密钥长度校验**（生成器只生成；校验全在 Go handler）。
- 若 genconf 生成后 `sing-box check` 失败（如密钥长度错），由 Go 端 `genconfRestartVerify` 捕获并报错（sing-box check 已在 `do_validate`）。
- 但落地改配置是「写 panel.json → genconf → restart → verify active」，verify 失败时 sing-box 进 deactivating，Go 端 `genconfRestartVerify` 会返回 active!=active 错误，用户可见。

> 注：sing-box `systemctl restart` 后即使配置错误也会进 deactivating/failed（与 caddy 同理，v1.5.24 教训适用），`genconfRestartVerify` 的 4 秒轮询 `is-active` 会捕获。

## 4. Go 后端 API

### 4.1 新增 RESTful 端点（`handlers.go`）

| Method | Path | Body | 作用 |
|--------|------|------|------|
| GET | `api/landings` | — | 返回 `{landings: [...]}`，每个含 `password`（从 secrets 读 `LANDING_<id>_PASS`）+ `uuid` |
| POST | `api/landings` | `{name, port, enabled, remote_*}` | **新建**：分配 id、生成凭证（调 `ansgo-admin regen-landing <id>`）、校验端口冲突、genconf+restart+verify |
| POST | `api/landings/update` | `{id, name?, port?, enabled?, remote_*?}` | **修改**：全字段校验（SS2022 密钥长度、socks 凭证非空、端口冲突）、genconf+restart+verify |
| POST | `api/landings/delete` | `{id}` | **删除**：移除数组项 + 删 secrets 的 `LANDING_<id>_*`、genconf+restart+verify |
| POST | `api/landings/regen` | `{id}` | **重置凭证**：生成新 pass+uuid、genconf+restart+verify |

### 4.2 凭证生成
新增 `ansgo-admin regen-landing <id>`：
```bash
cmd_regen_landing(){
  local id="$1"
  [ -n "$id" ] || { err "用法: ansgo-admin regen-landing <id>"; return 1; }
  do_backup >/dev/null
  local p u; p="$(gen_anytls_pass)"; u="$(cat /proc/sys/kernel/random/uuid)"
  _setsecret "LANDING_${id}_PASS" "$p"; _setsecret "LANDING_${id}_UUID" "$u"
  ok "已生成 落地服务 #${id} 凭证（LANDING_${id}_*）"
}
```
- Go 调用方式与现状 `regenHandler` 调 `regen2` 一致。
- `regen2` 改为 `regen-landing 1` 的兼容别名（保留旧调用入口）。

### 4.3 校验复用
- **端口冲突**：扩展 `portConflicts(c)` 遍历 `c.Landings`（enabled 的 port 加入 sbPorts）。
- **SS2022 密钥长度**：复用 `validSS2022Key`（`crypto.go`），update/create 时校验 `remote_type=="ss"` 的 `remote_password`。
- **anytls 密码**：regen 自动生成，update 不改密码（密码只通过 regen 改）。
- **socks 远端凭证**：update 时若 `remote_type=="socks"` 且 `remote_enabled`，校验 `remote_user`/`remote_password` 非空。
- **genconf+restart+verify**：全部复用 `genconfRestartVerify("sing-box")`（v1.5.24 同步事务模式，非 fire-and-forget）。

### 4.4 旧 API 兼容（路由保留）
- `api/landing`（旧远端 SS）和 `api/group2`（旧 enable）路由**保留注册**：
  - GET：返回 `{deprecated: true, message: "已迁移至 api/landings，请刷新页面"}` + 当前迁移状态。
  - POST：返回 410 Gone + 提示用新 API。
- 目的：旧书签/旧前端缓存不会 404；新前端不调旧路由。

### 4.5 `buildURIs` / `nodeHandler` 改动
- `buildURIs`：`anytls2` 单条 → 遍历 `c.Landings`，每个 enabled 生成 `landings[<id>]` URI：
  `anytls://<pass>@<domain>:<port>/?sni=<domain>#ANS-GO-Landing-<name>`
- `nodeHandler`：返回 `landings: [...]` 数组（每个含 uri/port/password/sni/enabled/via），前端按数组渲染卡片。`group2_enabled` 字段移除。

### 4.6 `healthHandler` 改动
- 新增 `target = "landing-<id>"` 形式，返回该落地端口的 systemd active + LISTEN + TCP 握手诊断。
- 旧 `target="anytls2"` 保留兼容（映射到 landing-1）。

### 4.7 `keyHandler` 改动
- 移除 `case "anytls2"`（旧密码保存入口），改为前端调 `api/landings/regen` 重置凭证。
- 其余 ss/anytls/socks/naive 分支不变。

### 4.8 `serviceHandler` / `svcInstallHandler` 改动
- `serviceHandler` 的 sing-box 启停判断条件 `needSB`：从 `c.Group2Enabled == "true"` 改为 `len(enabledLandings(c)) > 0`（新增 helper `enabledLandings(c) []LandingService`）。
- `svcInstallHandler` 同理。

### 4.9 `dashboardHandler` 改动
- 移除 `group2_enabled` 字段；仪表盘落地状态改为遍历 `landings` 统计启用数。

## 5. 前端 UI（`web/index.html`）

### 5.1 落地服务页（`loadLanding` 重写）
从「2 张固定卡片」改为**动态列表 + 新增按钮**：

```
🛬 落地服务
┌──────────────────────────────────────────────────┐
│  说明文字（落地服务 = anytls 入站 + 远端出口…）      │
│  [➕ 新增落地服务]                                 │
├──────────────────────────────────────────────────┤
│  📦 香港落地  [✅已启用]                          │  ← 每个 landing 一张卡片
│  ┌────────────────────────────────────────────┐ │
│  │ 名称 [香港落地    ]   端口 [21112 ]        │ │
│  │ AnyTLS 密码 [••••••]  [🎲随机]              │ │
│  │ ── 远端落地出口 ──                          │ │
│  │ 启用远端 [✓]   类型 [Shadowsocks ▼]        │ │
│  │ 远端地址 [...]  端口 [...]                  │ │
│  │ 加密方式 [...]  密钥 [...]    ← SS 字段     │ │
│  │ [💾保存] [🎲重置凭证] [🔍检测] [🗑删除]     │ │
│  └────────────────────────────────────────────┘ │
│  📦 日本落地  [已关闭]                            │
│  ...                                             │
└──────────────────────────────────────────────────┘
```

**交互细节**：
- 「远端类型」下拉（Shadowsocks / SOCKS5）切换时，动态显隐字段：
  - SS：显示「加密方式」+「密钥」
  - SOCKS5：显示「用户名」+「密码」
- 「➕ 新增」：弹窗填名称（必填）+ 端口（**必填**，前端校验 1-65535 + 非与现有落地冲突）→ POST `api/landings` → 刷新。（不沿用「端口随机」机制，因为多落地下用户必须明确知道每个落地端口，随机易混乱。）
- 每张卡片：「💾 保存」调 `api/landings/update`（全字段）；「🎲 重置凭证」二次确认后调 `api/landings/regen`；「🔍 检测」调 `api/health`（target=`landing-<id>`）；「🗑 删除」二次确认后调 `api/landings/delete`。
- AnyTLS 密码从 `api/landings` 返回的 `password` 填充（只读展示 + 🎲 随机生成填入输入框不自动保存，复用 `genField` helper）。
- 空列表时显示「暂无落地服务，点上方新增」引导。

### 5.2 节点信息页（`loadNode`）
- `group2_enabled` → 改读 `landings` 数组。
- 每个 `enabled` landing 渲染一张节点卡：标题用 `name`，标注「出口：SS 落地 / SOCKS5 落地 / 直连」。
- URI 行、端口行、密码行复用现有 `row()` helper。
- 二维码按钮复用现有浮动展示逻辑。

### 5.3 服务管理页（`loadSvc`）
- 移除底部 AnyTLS-2 旧卡片（v1.5.20 已移除，此处确认无残留）。
- 落地服务管理统一收敛到「落地服务」页（与 v1.5.20 设计一致）。

## 6. 迁移（`upgrade.sh`）

在现有 python 幂等补 `panel.json` 字段段内新增迁移逻辑（**仅当 `landings` 不存在时执行，幂等**）：

```python
# === v1.5.26 多落地服务迁移 ===
if "landings" not in cfg:
    old_g2 = cfg.get("group2_enabled", "false")
    old_port = int(cfg.get("anytls2_port", 0) or 0)
    old_land_en = cfg.get("ss_landing_enabled", "false")
    L = {
        "id": "1",
        "name": "默认落地",
        "enabled": old_g2 == "true",
        "port": old_port,
        "remote_enabled": old_land_en == "true",
        "remote_type": "ss",
        "remote_host": cfg.get("ss_landing_host", ""),
        "remote_port": int(cfg.get("ss_landing_port", 0) or 0),
        "remote_method": cfg.get("ss_landing_method", "2022-blake3-aes-128-gcm"),
        "remote_password": cfg.get("ss_landing_password", ""),
        "remote_user": "",
    }
    cfg["landings"] = [L]
    migrated = True
# 移除旧扁平字段（landings 已存在后旧字段无用，删掉避免 Config 反序列化困惑）
for k in ("group2_enabled", "anytls2_port", "naive2_port",
          "ss_landing_enabled", "ss_landing_host", "ss_landing_port",
          "ss_landing_method", "ss_landing_password"):
    cfg.pop(k, None)
```

`secrets.env` rename（bash 段，幂等）：
```bash
# ANYTLS2_* → LANDING_1_*（仅当 LANDING_1_* 不存在时）
if ! grep -q '^LANDING_1_PASS=' "$SECRETS" 2>/dev/null; then
  if grep -q '^ANYTLS2_PASS=' "$SECRETS" 2>/dev/null; then
    sed -i 's/^ANYTLS2_PASS=/LANDING_1_PASS=/; s/^ANYTLS2_UUID=/LANDING_1_UUID=/' "$SECRETS"
  fi
fi
```

## 7. `ansgo-admin` 改动

- **新增** `regen-landing <id>`（见 §4.2）。
- **保留** `regen2`（改为调 `regen-landing 1` 的别名，向后兼容旧调用/书签）。
- **保留** `group2 enable/disable/status`（标记 deprecated，内部转发为对 `landings[0]` 的操作；迁移期老脚本可能用）。
- `status`/`info` 输出：把「第2组服务」改为「落地服务列表」，遍历 `landings` 打印每个的 name/port/enabled/远端类型。

## 8. Docker（`entrypoint.sh`）

sing-box 自动启停判断（§现有 `SB_NEED` 循环）改为：检测 `landings` 数组里是否有 enabled 项：
```bash
SB_NEED=0
# 旧字段兼容（迁移前的旧镜像）
for K in svc_ss_enabled svc_anytls_enabled svc_socks_enabled; do
  V=$(grep -o "\"$K\": *\"[^\"]*\"" "$CONF" 2>/dev/null | grep -o '"[^"]*"$' | tr -d '"')
  [ "$V" = "true" ] && SB_NEED=1
done
# 新字段：landings 数组里有 enabled=true
if grep -q '"enabled":[[:space:]]*true' "$CONF" 2>/dev/null; then
  # 粗判：landings 数组内 enabled=true（python 迁移后格式固定）
  SB_NEED=1
fi
```
（精确判断用 python -c 解析 landings 数组更稳，实现时确认。）

## 9. 版本号与发版

- **版本**：v1.5.26
- `main.go` `version = "1.5.26"`
- `install.sh` / `upgrade.sh` 的 `VER="v1.5.26"`
- 发版三件套（AGENTS.md §9「只提交源码不发版 = 没修复」最高优先级教训）：
  1. `gh release create v1.5.26` + 上传 6 资产（`ansgo-panel-linux-{amd64,arm64}` + `caddy-naive-linux-{amd64,arm64}` + `sing-box-linux-{amd64,arm64}.tar.gz`；sing-box 资产不变则可复用跳过）
  2. `docker buildx build --push -t ghcr.io/jiasongji/ansgo:latest -t ghcr.io/jiasongji/ansgo:v1.5.26 -f deploy/Dockerfile.allinone .`
  3. 用户升级：`curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/upgrade.sh | bash`

## 10. 验证清单

### 10.1 编译/语法
- [ ] `node --check deploy/panel/web/index.html` 的 `<script>` 抽出部分（或浏览器控制台无报错）
- [ ] `go build ./...`（panel 包）成功
- [ ] `bash -n install.sh upgrade.sh deploy/ansgo-admin deploy/docker/entrypoint.sh`
- [ ] `python3 -c "import ast; ast.parse(open('deploy/ansgo-genconf').read().split('PYEOF')[0])"` （genconf python 段语法）

### 10.2 单元/回归测试（`settings_repro_test.go` 模式扩展）
- [ ] `TestLandingsConfig_LoadsArray`：含 `landings` 数组的 panel.json → loadConfig 后 `Landings` 正确填充
- [ ] `TestLandingsCreate_AssignsIncrementalId`：新建 id 递增（id=max+1）
- [ ] `TestLandingsPortConflict_Detected`：两落地同端口 → update/create 报错
- [ ] `TestLandingsDelete_RemovesSecrets`：删除后 secrets 无 `LANDING_<id>_*`
- [ ] `TestLandingsSSKeyValidation`：SS2022 密钥长度错误 → update 报错

> 注：旧扁平字段 → `landings` 的**迁移测试**属于 `upgrade.sh` python 段逻辑，不在 Go 单元测试覆盖范围（Go 的 loadConfig 不做迁移，迁移由 upgrade.sh 在升级时一次性完成）。迁移的正确性由 §10.3 端到端验证覆盖。

### 10.3 端到端（部署服务器，需用户确认环境）
- [ ] 旧 v1.5.25 服务器执行 upgrade.sh → landings[0] 迁移成功，原落地服务继续可用
- [ ] 新建第 2 个落地服务（走 socks5 远端）→ sing-box config 含 2 inbound + 2 outbound + 2 route
- [ ] 节点信息页显示 2 张落地节点卡
- [ ] 删除某落地 → secrets 清理 + sing-box 重启后该端口不 LISTEN
- [ ] Docker 形态 `docker compose up -d --force-recreate` 后落地服务自动恢复

## 11. 影响范围（文件清单）

| 文件 | 改动 |
|------|------|
| `deploy/panel/main.go` | Config 结构：+Landings 字段、-旧扁平字段；`version="1.5.26"` |
| `deploy/panel/handlers.go` | 新增 landings CRUD handlers；改 buildURIs/nodeHandler/healthHandler/portConflicts/serviceHandler/svcInstallHandler/dashboardHandler；keyHandler 移除 anytls2 case；旧 landing/group2 handler 兼容返回 |
| `deploy/panel/web/index.html` | 重写 loadLanding（动态列表）；loadNode 改读 landings |
| `deploy/panel/settings_repro_test.go` | 新增多落地回归测试 |
| `deploy/ansgo-genconf` | 移除旧 group2/landing 逻辑，新增 landings 遍历生成 |
| `deploy/ansgo-admin` | 新增 regen-landing；regen2/group2 兼容；status/info 输出更新 |
| `deploy/docker/entrypoint.sh` | SB_NEED 判断改读 landings |
| `deploy/upgrade.sh` | 新增 panel.json landings 迁移 + secrets rename；VER=v1.5.26 |
| `install.sh` | VER=v1.5.26 |
| `AGENTS.md` | v1.5.26 教训条目 + §5.5/§6.4/§0 版本摘要更新 |
| `deploy/README.md` | 落地服务章节更新（多落地 + socks5 远端） |

## 12. 风险与回滚

| 风险 | 应对 |
|------|------|
| 迁移失败致 panel.json 损坏 | upgrade.sh 迁移前 `cp panel.json panel.json.bak-pre-v1.5.26`；迁移用 python 临时文件 + rename 原子写 |
| 旧用户升级后落地服务失效 | 迁移幂等 + 端到端验证清单 §10.3；失败可 `cp panel.json.bak-* panel.json` 回滚 + 重装旧版 |
| sing-box socks outbound 配置错误 | genconf 生成后 Go 端 genconfRestartVerify 验证 active；sing-box check 在 do_validate |
| secrets rename 重复执行 | sed 幂等（grep 守卫，LANDING_1_PASS 已存在则跳过） |
| Docker SB_NEED 误判 | python 精确解析 landings 数组（不用粗 grep） |
| 前端列表渲染 JS 语法错误（v1.5.x 教训） | node --check + 浏览器硬刷新验证 |
