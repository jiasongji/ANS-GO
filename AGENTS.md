# ANS-GO 代理服务器方案 (AGENTS.md)

> 本文件是项目的**唯一事实来源**。新窗口执行、GitHub 教程撰写、服务器部署均以本文件为准。
> 敏感凭证见 `.secrets.local`（已 gitignore，不入库）。
>
> **部署状态：✅ 已部署并端到端验证。** 可复现产物在 `deploy/`，一键部署见 §11。
>
> **当前版本：v1.5.26**（install.sh 脚本版本 v1.5.26；**面板 Go 二进制 v1.5.26 + release 资产 + ghcr.io 镜像齐全**）。完整发布历史见 GitHub Releases。
>
> ### 版本演进摘要（一行一版，详细根因见对应 release notes）
>
> | 版本 | 要点 | 关键教训 / 约束 |
> |------|------|----------------|
> | **v1.5.26** | **多落地服务（multi-landing）**：落地服务从硬编码单个 AnyTLS-2 改为**可创建多个的数组**。`panel.json` 新增 `landings: []`（每项 = 一个 anytls 入站 + 可选一个远端出站），移除旧的 `group2_enabled`/`anytls2_port`/`ss_landing_*` 扁平字段。远端出口**每落地独立开关**，支持 **SS + SOCKS5** 两种远端协议（原仅 SS）。`upgrade.sh` 把旧单落地自动迁移为 `landings[0]`（幂等 + 备份），secrets 里 `ANYTLS2_*` rename 为 `LANDING_1_*`。新增 RESTful `api/landings` CRUD（GET/POST/update/delete/regen）+ `ansgo-admin regen-landing <id>`；`regen2`/`group2` 保留兼容别名。前端落地服务页改动态列表 + 新增按钮；节点信息页遍历 landings 渲染 | **「扁平字段 → 数组」迁移必须幂等 + 自动**：升级脚本用「字段不存在才迁移」守卫 + 迁移后删旧字段，避免重复执行破坏数据；secrets key rename 用 grep 守卫（`LANDING_1_PASS` 已存在则跳过 sed）。**用户可控字符串插进 URL fragment 必须转义**：落地名称进 `anytls://...#ANS-GO-Landing-<name>`，`< > & " '` + 空格/`#?/` 会破坏 URI，前端 `escapeHtml` + Go `sanitizeLandingName` 双层处理。**CRUD 配置变更走同步事务**（复用 v1.5.24 的 `genconfRestartVerify`：genconf→restart→verify active），新增落地/改远端/删除/重置凭证全部同步，绝不 fire-and-forget。**sing-box tag 全局唯一**靠 id 后缀（`landing-in-<id>`/`landing-out-<id>`），id 取 max+1 删除不回收避免残留引用。genconf 遍历数组生成多 inbound+outbound+route，`remote_enabled=false` 时不加 outbound/rule（走 direct）。**新部署 panel.json 必须含 `"landings":[]`**（install.sh 已加），否则旧 Config 反序列化虽 nil-safe 但节点页/落地页空列表引导更友好 |
> | **v1.5.25** | **根治 NaiveProxy「检测正常反代正常但代理不能用」**：Caddyfile 同站点块内 `forward_proxy`+`reverse_proxy`（伪装）共存时，caddy 默认指令排序把 `forward_proxy` 放在 `reverse_proxy`【之后】→ NaiveProxy 客户端 CONNECT 请求被 reverse_proxy（伪装）拦截，forward_proxy 永远拿不到代理流量。全局 `order forward_proxy before reverse_proxy` 对此**无效**（caddy 坑）。修复：用 `route {}` 块包裹 naive 站点指令，强制按书写顺序执行（forward_proxy 在前），adapted JSON subroute handlers=`[forward_proxy, reverse_proxy]` | **caddy 同站点块内多 handler 的指令排序是个大坑**：全局 `order` 指令对同站点块内的 handler 共存（如 forward_proxy+reverse_proxy）不生效，caddy 仍按内置默认顺序排（forward_proxy 天然靠后）。**必须用 `route {}` 块显式包裹**才能按书写顺序执行。官方 naiveproxy 示例用 `file_server`（天然排在 forward_proxy 后）规避了此问题；用 `reverse_proxy` 做伪装才会踩坑。**诊断方法**：`caddy adapt --config Caddyfile --adapter caddyfile` 看 JSON 里 naive 站点 route 的 `handle` 数组，forward_proxy 必须排第一。naive padding 协议本身没问题（用真实 naive 客户端 v149 验证 forward_proxy 能正常代理），问题纯在 caddy 配置生成层 |
> | **v1.5.24** | NaiveProxy「反代正常但代理不能用」根治：旧版 keyHandler 改凭证是异步 fire-and-forget（go func genconf+restart 忽略错误）→ secrets.env 已改但 Caddyfile 没跟上 / caddy 重启 deactivating → probe_resistance 认证失败静默走伪装（反代照常打开，极具迷惑性）。修复：①keyHandler 改同步 genconf→restart→验证 active（失败报错不再掩盖）②svcInstall 安装后验证 active ③新增「🔧 修复」按钮（每张服务卡）一键 genconf+restart+验证，强制同步 secrets↔配置 | **「异步 + 忽略错误」是配置一致性的天敌**：写 secrets.env → genconf → systemctl restart 三步必须**同步顺序执行 + 检查每步退出码 + 验证最终服务 active**，任何一步 fire-and-forget 都会留下不一致。**probe_resistance 的副作用**：认证失败静默走伪装（而非 407）是 NaiveProxy 的反探测设计，但它让「凭证不匹配」表现为「反代正常」极具迷惑性——健康检测（端口 LISTEN + TCP 握手）无法发现这种逻辑层故障，必须额外验证「实际代理认证可用」或提供一键修复入口。**systemctl restart 后必须验证 is-active=active**：restart 命令成功不代表服务起来了（caddy 配置错误会进入 deactivating/failed），要轮询等待确认 |
> | **v1.5.23** | NaiveProxy 改密码/重装后无法运行根治（三层防御）：NaiveProxy 凭证写入 Caddyfile `basic_auth`，含空格/`{}` 的密码破坏语法 → caddy 重启失败 → Naive 挂（AnyTLS/SS 走 sing-box 不受影响）。①前端 `svcSave` 拦非法字符 ②`keyHandler` 后端校验拒绝 ③**`genconf` 生成后 `caddy validate` 预检，失败回滚旧配置不重启**（终极兜底）。另：`keyHandler` genconf 失败时不重启服务；`genconf source secrets.env` 加 `\|\|true` 容错 | **配置文件语法注入是代理服务的经典坑**：用户可控的凭证（用户名/密码）被直接插进 Caddyfile/Nginx conf 等结构化配置文件时，必须校验不含该语法的元字符（空格/`{}`/`#` 注释符等），否则一个带空格的密码就让整个服务起不来。**校验要分层**：前端拦截（即时反馈）+ 后端 handler 拒绝（防绕过）+ 生成器 validate-and-rollback（终极兜底，即使前两层都漏了也不会把坏配置喂给进程）。**sing-box(caddy) 进程级隔离**让 AnyTLS/SS 不受 caddy 配置错误影响——这也是「改 Naive 密码挂的是 Naive 不是 AnyTLS」的原因。排查「服务无法运行」先看 `journalctl -u <svc>` 的配置解析错误行 |
> | **v1.5.22** | 面板设置保存无反应根治 + 侧栏版本号：①`settingsHandler` 的 `url_path` 分支改为仅在 `newPath != c.URLPath` 时才 `needRestart`（旧逻辑字段存在即重启，前端每次回传 url_path → 改标题/IP 也重启 → 用户「点保存无反应」）②侧栏底部常驻 `ANS-GO vX.Y.Z` 版本号（`api/auth` 返回 `version` 字段，`checkAuth` 渲染，折叠态自适应）③`saveSet()` 的 `g()` 健壮化：`--no-caddy` 模式下 `s_disguise_panel` 不渲染，旧 `$('#s_x').value` 抛 TypeError 中断保存，改为缺失返回空串 | **「只提交源码不发版」= 没修复**：`upgrade.sh` 裸金属从 release 拉二进制、Docker 从 ghcr.io 拉镜像，二者都依赖发版产物；只 commit 不创建 release + 不 `docker buildx --push`，服务器执行 upgrade 后 md5 不变跳过替换，bug 照旧。**发版必须三件套同步**：①创建 GitHub release ②上传 6 个资产（panel/caddy/sing-box × amd64/arm64）③重建 ghcr 镜像（`:latest` + `:vX.Y.Z`）。版本号必须**前端可见**（侧栏常驻）才能让用户/运维一眼核实升级是否生效，避免「我升级了但不知道成没成」；前端读 DOM 元素的 helper 必须防御 `null`（条件渲染的字段可能不存在） |
> | **v1.5.21** | 面板设置保存无反应修复（源码已修复但**未发版**，合入 v1.5.22 补齐发布） | 教训同 v1.5.22 第一条根因；此版本仅 commit `72fb817` 未创建 release 资产/镜像，是「只改代码不发布」的反例 |
> | **v1.5.21** | 修复面板设置「保存无反应」：`settingsHandler` 只要 POST 体含 `url_path` 就 `needRestart=true`（无条件重启），而前端 `saveSet()` 每次都把当前 `url_path` 原样回传 → **只改标题/IP 也会重启面板**，用户表现为「点保存无反应」。改为仅在 `url_path` 真正变化时才重启（与 `panel_port` 的 `!= 守卫` 一致） | 「重启触发条件」必须用真实**变化判断**而非「字段存在」判断：前端表单常把全量字段回传，后端按 `*ptr != 当前值` 守卫才安全；同函数内 `panel_port` 已是正确范式（`*b.PanelPort != c.PanelPort`），`url_path` 漏写守卫是复制粘贴遗漏。新增 Go 集成回归测试（`TestSettingsSave_UntouchedFieldsDoNotRestart` + `TestSettingsSave_UrlPathChangeStillRestarts`）覆盖两条路径；`confPath`/`secretsPath` 由 const 改 var 以支持测试覆盖路径（运行时零影响） |
> | **v1.5.20** | 仪表盘精简 + AnyTLS-2 管理集中：①移除仪表盘「管理面板」服务卡（与代理服务并列冗余），状态改为**顶栏用户名右侧圆点**（绿=运行中/红=未运行/灰=获取失败），hover 显示状态+端口，登录及每次切 tab 静默刷新（fire-and-forget）②落地服务页集中 AnyTLS-2 全部管理（启用/端口/密码/🎲/💾保存/🔍检测/远端 SS），新增密码输入框 + 操作行（复用 `saveKey('anytls2')`+`checkHealth('anytls2')`，零新增端点）③服务管理页删除底部 AnyTLS-2 卡片（避免与落地服务页重复） | 管理面板状态「全局可见」需求可用顶栏常驻圆点实现（复用 `.dot` 类 + 原生 `title` 属性做 hover 提示，零额外浮层依赖）；同一服务的管理能力分散在多页是 UI 债（服务管理 + 落地服务都显示 AnyTLS-2 卡片），应收敛到单一入口；复用既有 `/api/key`+`/api/health` 端点（已支持 anytls2）而非新增端点，避免扩大回归面；顶栏状态刷新挂在 `showTab()` 末尾 fire-and-forget，不阻塞页面加载 |
> | **v1.5.19** | 面板 UI 细节优化 + Docker 升级根治：①NaiveProxy 节点信息补 SNI 域名 + 密码字段（nodeHandler 缺字段致前端 row 不渲染）②二维码改为点击服务名旁「📱 二维码」按钮浮动展示，点空白关闭（默认不再占版面）③全局服务顺序统一 AnyTLS → NaiveProxy → Shadowsocks → SOCKS5（节点页/服务管理/仪表盘三处）④面板设置页 --no-caddy 模式时隐藏「直访伪装(:443)」⑤Docker 升级「没变化」三根因根治（upgrade.sh 加 `--force-recreate` + entrypoint 补 server_ip 字段 + 版本验证升级为 err）⑥upgrade.sh 升级完成段 echo 改 printf 修颜色码 `\033` 乱码 | 后端返回字段必须与前端模板变量名一一对应（前端取 `n.password`，后端只传 `pass` 则不渲染）；UI 条件渲染须后端先暴露开关字段；二维码浮动用 overlay 复用，点空白关闭靠 `e.target===overlay`；**Docker `compose up -d` 在镜像 digest 未变时会跳过 recreate → entrypoint 不重跑 → 旧二进制继续运行**，升级必须 `--force-recreate`；**bash 的 `echo` 默认不解释 `\033` 转义**（输出字面 `\033[36m` 乱码），含颜色码的输出必须用 `printf '%b'` |
> | **v1.5.18** | 面板 UI 优化四项 + VPC 公网 IP 修正：①节点信息「连接地址」显示服务器 IP（Go 端 UDP 探测公网出口，进程级缓存，失败回退域名）②删「服务控制」菜单，服务管理成唯一总操作页 ③🎲 随机按钮移到每个输入框右侧（纯前端 crypto.getRandomValues，不自动保存）④每张服务卡操作按钮集中一行自适应 `[应用][启停][重启][装卸][检测]` + VPC 修正：公网 IP 三层优先级（手动填写 > 公网探测 > 域名），内网 IP 自动丢弃 + 引导提示 + 主动检测按钮 | UDP 探测公网 IP 在 VPC/NAT 下只能拿内网网卡（公网 IP 在 NAT 网关做 SNAT 本机无从得知），必须 `isPrivateIP()` 过滤 RFC1918/CGNAT 后回退域名；主动调第三方 API（ipify 等）必须用户点击触发不在启动时自动外发；合并操作行时端口「应用」整合进 `svcSave`（串行调 portHandler+keyHandler，零新增端点）；前端随机必须 crypto.getRandomValues 且不自动保存 |
> | **v1.5.17** | Docker manual 证书模式四根因根治：证书页误显示 acme / capability 收窄致读不了宿主证书目录 / 版本号 `vv` 双 v / 容器重建后代理不自动恢复 | systemd `CapabilityBoundingSet` 收窄后 root 不再隐式绕过 DAC；容器重建 ≠ 重启（systemd 状态归零，entrypoint 须幂等重建）；证书路径走卷内 644 副本不依赖宿主原文件 |
> | **v1.5.16** | 新增 SOCKS5（强制鉴权）+ 自定义网页标题 + NaiveProxy 落地简化（移除 naive-2，落地仅 AnyTLS-2→SS） | caddy 与 sing-box 是两个独立进程，跨进程路由物理上不可行；SOCKS5 公网部署必须强制鉴权 |
> | **v1.5.15** | 根治节点信息页一直「加载中」 | JS 模板插值 `${obj.field}` 若 field 是 number/bool 再做 string-only 操作须先 `String()` 强转；async forEach 异常会静默 reject 整个 promise |
> | **v1.5.14** | genconf 端口冲突校验 + svcActive 保留 stdout + xcaddy 磁盘预检 + ldflags 注入 version + LXC 时钟/iptables 线上修复 | `systemctl is-active` 非 active 也返回非 0 但 stdout 仍有效，`err!=nil` 时勿丢弃 stdout；SS2022/WireGuard 依赖时钟，LXC 须单独装 NTP；宝塔会把 iptables policy 改 DROP |
> | **v1.5.13** | 根治 ghcr.io 镜像二进制滞后于源码 | **每次发版必须同步 `docker buildx build --push` 重建镜像**，不能只 commit 源码；排查面板问题先 `docker exec md5sum` 对比镜像内二进制 |
> | **v1.5.12** | 节点信息页重构 + 落地服务单页 + 端口全随机 + 健康检测 + 修 sing-box 路由引用不存在的 `naive-in2` tag | 改前端必须重新编译上传（stop→rm→scp→md5→start）+ 浏览器硬刷新；落地路由只引用 `anytls-in2`（naive 不在 sing-box） |
> | **v1.5.11** | 三页（安装/端口/密钥）合并为「服务管理」单页 + `reloadCurrentTab()` 抽象 | 动作回调统一走「刷新当前 tab」而非硬编码本页函数，避免合并/拆分页面时回调链断裂 |
> | **v1.5.10** | 修 Docker 容器重启后 naive 已装但 caddy 被停 | `--no-caddy` 模式须检查 `svc_naive_enabled`：naive 在则 enable caddy |
> | **v1.5.9** | SS/AnyTLS 状态解耦 + 停 SS 不停 AnyTLS + caddy 智能启停（加 `CaddyEnable` 字段） | `exec.Command(...).CombinedOutput()` 返回 `(output, err)` 两值，不能单值赋值，用 `.Run()` |
> | **v1.5.8** | 修 `--no-caddy`+manual 证书模式 4 根因（auto_https off / 证书改 entrypoint 同步 / mask→disable） | caddy 默认 auto_https on 会抢 80；manual 证书在容器内须走卷内副本 |
> | **v1.5.7** | 修 `--no-caddy` 模式 caddy 被拉起占 443 + manual 证书 bind mount + compose v1/v2 检测 | bash 函数必须先定义后调用；`docker build -f` 相对路径按 cwd 解析，跨目录用绝对路径 |
> | **v1.5.6** | 新增 `--no-caddy` 模式（nginx 共存）+ 发布 ghcr.io 多架构镜像 | 国内 buildx 多架构构建须 `docker-container` driver + `--build-arg HTTP_PROXY` 注入 |
> | **v1.5.5** | 7 个密码/端口 CLI 参数 + `validate_inputs()` 校验 + naive 默认端口改 44333 | bash `$var` 后紧跟全角标点在 `set -u` 下会被当成另一变量名，须 `${var}` 界定 |
> | **v1.5.4** | 修首次安装代理服务必失败（`/etc/sing-box/` 目录未创建被 `cmd\|tail` 吞错） | 写配置工具必须自建父目录；`cmd \| tail` 吞错管道会掩盖致命错误，加 `\|\| warn` |
> | **v1.5.3** | 根治 `curl\|bash` 的 SIGPIPE / 进程替换卡死 / 二进制 404（含 v1.5.1/v1.5.2 被取代的中间尝试） | `curl\|bash` 下 SIGPIPE 由 `exit` 引起；进程替换判断用 `[ -e ]` 非 `[ -f ]`；二进制下载不依赖单一源 |
> | **v1.5.0** | 密钥手动设置 + 手动指定证书路径（`cert_mode=manual`） | — |
> | **v1.4.x** | v1.4.0 移动端+白天主题+Docker all-in-one；v1.4.1 架构判断修复；v1.4.2 `--uninstall`/`--purge`；v1.4.3 左侧可折叠侧边栏 | `bash -n` 只查语法不执行，查不出运行时变量错误，改完务必实际执行 |

---

## 0. 项目目标

在低配 VPS（LXC 或 KVM）上，部署 **Web 管理面板** + 可选的代理服务，支持两种部署形态：

- **裸金属脚本**（LXC / 低配 256MB 推荐）：`install.sh` 直装，systemd 管理三服务，内存占用最小
- **Docker 一体化**（KVM / 资源充裕推荐）：`install.sh --docker`，单容器（all-in-one：sing-box + caddy + 面板 + systemd）跑全套

要求：

- **面板优先架构**：install.sh 只装面板 + 证书，代理服务在 Web 后台「服务安装」页按需启用
- **多协议代理**（按需）：Shadowsocks（2022）+ AnyTLS + SOCKS5（sing-box 三 inbound）+ NaiveProxy（caddy forwardproxy-naive 分支）
- **落地服务 + 链式出站**：可选额外 anytls-2（**仅 anytls-2 出口经 SS 走另一台落地服务器**）。NaiveProxy / SOCKS5 不参与任何远端落地（caddy/sing-box 分离架构约束）；v1.5.12 起 naive-2 已从落地中移除
- **真实域名证书**：`your-domain.com`（Let's Encrypt），面板与各服务共享
- **Web 管理面板**：中文、暗黑/白天双主题、**移动端自适应**（侧边栏抽屉 / 表单 label 上置 / 网格单列）、**左侧可折叠导航**（桌面端折叠成图标条 + 移动端汉堡抽屉，localStorage 记忆）、可管全部协议参数 + 证书 + 服务安装/卸载 + 自身配置 + **自定义网页标题**
- **性能最大化**：网络内核调优、清理垃圾软件、内存占用最小
- **可审计、可回滚、可离线管理**（SSH 兜底脚本）

---

## 1. 服务器与域名信息

| 项 | 值 |
|----|----|
| IPv4 | `<服务器IP>` |
| IPv6 | `<服务器IPv6>`（注：宿主防火墙拦截入站，仅 IPv4 可用）|
| 系统 | Debian 12 (bookworm) / Proxmox LXC 容器 |
| 规格 | 1 vCPU / 256MB RAM + 256MB swap / 3.86GB disk |
| 域名 | `your-domain.com`（已 A + AAAA 解析到本机）|
| DNS 服务商 | Dynu Systems（NS = ns0~6.dynu.com，zone id = <Dynu_zone_id>）|
| SSH | `root@<服务器IP>`（**公钥登录 + 端口 25822**，密码登录已禁用，密钥/路径见 `.secrets.local`）|

### 已完成的基础优化（第一阶段，无需重复）
- 移除 postfix / mailcap / reportbug / tasksel 等垃圾包
- `apt update && upgrade`，0 待升级包
- 内核网络调优写入 `/etc/sysctl.d/99-proxy-tune.conf`（BBR / TFO=3 / MTU 探测 / 快速 TIME_WAIT 回收等）
- 文件描述符上限 1048576（`/etc/security/limits.d/99-proxy.conf`）
- journald 限 50M（`/etc/systemd/journald.conf.d/size.conf`）
- 注：LXC 内 `rmem_max`/`wmem_max`/`file-max`/`default_qdisc` 为宿主控制只读，已是最优

---

## 2. 架构（面板优先 + 一张证书共享 + 按需装服务）

```
  ┌─ install.sh 阶段（装面板+证书）─────────────────────────┐
  │  acme.sh + Dynu DNS-01 → /etc/ssl/ansgo/*.pem            │
  │  一张 ECDSA 证书 → 面板 / caddy伪装 / 各代理服务共享      │
  │                                                          │
  │  caddy :443  ← 始终运行（域名直访伪装站，反代 example.com）│
  │  ansgo-panel :15608  ← Web 管理面板                      │
  └──────────────────────────────────────────────────────────┘
                         │
           用户登录面板 →「服务安装」页按需启用
                         ▼
  ┌─ 面板内按需安装（svc_*_enabled 开关）─────────────────────┐
  │  Shadowsocks ──┐                                         │
  │  AnyTLS     ──┼─ sing-box（无启用 inbound 时不启动）        │
  │  SOCKS5     ──┘                                          │
  │  NaiveProxy ──── caddy（forward_proxy，与 :443伪装同进程）  │
  │                                                          │
  │  落地服务（可选）: anytls-2（仅它经 ss-out 落地）            │
  │      └─ outbound: ss-out ──▶ 落地服务器 ss-server ──▶ 目标  │
  └──────────────────────────────────────────────────────────┘
                         ▲
  ┌─ ansgo-admin (bash) ──── 零依赖离线兜底（面板全挂也能管理）┘
```

### 核心设计原则
1. **解耦**：caddy / sing-box / 面板是三个独立进程、独立端口、独立 systemd unit。改任一个不影响另两个，**改协议端口永远不会断面板**。
2. **共享证书**：一张 `your-domain.com` 证书同时喂三个服务（TLS 证书按域名签发，不限端口，8443/15608 自动覆盖）。续期一次，三服务一起 reload。
3. **离线兜底**：`ansgo-admin` bash 脚本不依赖面板，面板全挂也能 SSH 管理一切。
4. **自指安全**：面板端口可改，但改的是"自己的端口"，机制见 §6.5。

---

## 3. 端口分配

| 服务 | 默认端口 | 协议 | 类别 | 面板可改 |
|------|---------|------|------|---------|
| caddy :443 伪装站 | `443` TCP | HTTPS（反代伪装）| **面板必需**（域名直访）| ❌ 固定（伪装站）|
| Web 面板 (ansgo-panel) | **随机** 10000-65535（v1.5.12 前 15608）| HTTPS | **面板必需** | ✅（改后重启）|
| NaiveProxy (caddy) | **随机** 10000-65535（v1.5.12 前 44333）| HTTPS forward proxy | 按需安装（不参与落地）| ✅ |
| AnyTLS (sing-box) | **随机** 10000-65535（v1.5.12 前 21111）| TLS | 按需安装 | ✅ |
| Shadowsocks (sing-box) | **随机** 10000-65535（v1.5.12 前 33899）| SS2022 | 按需安装 | ✅ |
| SOCKS5 (sing-box) | **随机** 10000-65535（默认 10808）| SOCKS5（强制鉴权）| 按需安装（不参与落地）| ✅ |
| 落地服务 anytls-2 | **随机** 10000-65535（v1.5.12 前 21112）| TLS | 按需（经 SS 落地）| ✅ |
| caddy HTTP（重定向）| `80` TCP | HTTP | 固定 | ❌ |
| SSH | `25822` TCP | SSH | **已加固**（公钥+禁密码，见 §14）| ❌ |

> :443 是**域名直访伪装站**（纯反代，不提供代理），随面板一起启动。NaiveProxy 用独立端口，不要用 443。
> v1.5.12 起：所有服务端口**部署时随机生成**（10000-65535，避开 80/443/25822/已占用），可通过 `--ss-port` 等参数显式指定。已部署服务器端口不变（仅新部署默认值改变）。
>
> ⚠️ **SSH 端口已从默认 22 改为 25822**（加固属部署后动作，见 §14）。`install.sh` 仍装在 22；新部署若需复刻，加固步骤见 §14。

防火墙（nftables）policy=accept 全放行；部署时仅确保新端口可达，不加 drop 规则（避免锁死 SSH，LXC 安全由宿主负责）。

---

## 4. 证书方案（acme 自动 / 手动指定，二选一）

支持两种证书来源，由 `panel.json` 的 `cert_mode` 字段控制（部署后可在面板「证书管理」页切换）：

- **`acme`（默认）**：acme.sh + Dynu DNS-01 自动签发到固定路径，60 天自动续期（详见下文）
- **`manual`**：直接引用用户提供的证书+私钥**完整绝对路径**（如已有 Let's Encrypt/Caddy/其他 ACME 客户端签发的证书，或商业证书），跳过 Dynu 凭证与 acme.sh 签发。续期由用户自行管理（在服务器替换文件后点面板「重新加载证书」即可）

> `cert_mode` 字段统一驱动三个服务（caddy / sing-box / 面板自身）的证书引用。Go 端 `certPaths()` 与 `ansgo-genconf`（python）按同一语义解析：manual 且两路径齐全 → 用绝对路径；否则回退 `cert_dir/fullchain.pem + privkey.pem`（兼容旧部署）。

### 签发工具（acme 模式）
**acme.sh**（curl 安装，~200KB，不走 apt）。不用 caddy 自带 ACME——因为 caddy 内部证书存储路径深、版本化命名，sing-box 引用困难且续期后需手动 reload。acme.sh 可签发到固定路径并通过 `--reloadcmd` 续期后自动重启三服务。

### 验证方式（acme 模式）
**DNS-01**（绕开 80 端口依赖，可签泛域名）。

### Dynu 凭证双保险（A 默认，A 失败降级 B；仅 acme 模式需要）
两套都已实测可用（HTTP 200，能读写 zone <Dynu_zone_id>）：

| 路径 | 凭证 | 机制 | 用法 |
|------|------|------|------|
| **A（默认）** | API Key | `Api-Key` 请求头 | 自定义钩子 `dns_dynukey.sh`（~60行，直接调 Dynu REST API 加删 TXT 记录）|
| **B（降级）** | Client ID + Secret | OAuth2 `client_credentials` 换 bearer token | acme.sh 官方 `dns_dynu` 插件 |

**降级逻辑**：部署时先尝试 A 签发；若 A 返回非 0 退出码，自动切换到 B 重试。两套凭证均存 `/root/.acme.sh/` 下，root 独占可读。

> acme.sh 官方插件用 OAuth2（要 `Dynu_ClientId` + `Dynu_Secret`），而 API Key 是另一套凭证。这是 Dynu 平台同时提供的两种鉴权，互不冲突。

### 证书落点与续期
```
acme 模式  : /etc/ssl/ansgo/fullchain.pem + privkey.pem   # 证书链 + 私钥
manual 模式: cert_fullchain + cert_privkey 字段指定的绝对路径（用户原位置，不复制）
```
- 续期周期（acme 模式）：acme.sh 默认 60 天（实际由 ARI 窗口驱动，约 60 天）
- 续期 reload：统一走 `ansgo-cert-reload` 脚本（`--install-cert --reloadcmd "/usr/local/bin/ansgo-cert-reload"`）。v1.5.0 起该脚本改为「配置文件存在即重载」（不再 grep 固定路径，兼容 manual 模式的自定义路径）
- ⚠️ **caddy 用 restart 不用 reload**：Caddyfile 设了 `admin off`，无 admin API 通道，`systemctl reload caddy` 会失败。续期/改配置统一 `systemctl restart caddy`（naive 闪断 1-2s 可接受）。sing-box（ss+anytls）和 ansgo-panel 用 restart
- `--keylength ec-256`（ECDSA，体积小、握手快）
- **manual 模式续期**：用户在服务器替换证书文件后，登录面板「证书管理」页点「🔄 重新加载证书」即可（调 `ansgo-cert-reload` 重启三服务，含面板自身）；也可 SSH 执行 `/usr/local/bin/ansgo-cert-reload`

---

## 5. 协议配置

### 5.1 NaiveProxy（caddy forwardproxy-naive 分支）
- 二进制：`/usr/local/bin/caddy`（klzgrad/forwardproxy release v2.11.2-naive，含 naive padding 层）
- 配置：`/etc/caddy/Caddyfile`（设 `admin off`，故 reload 不可用，改配置用 restart）
- 关键特性：`probe_resistance`（探测伪装）+ `hide_ip` + `hide_via` + naive padding
- **双伪装架构**（caddy 在三个端口上各起一个 site）：
  - `:443` 纯反代伪装站（浏览器直访命中，**不提供代理**）→ 反代到 `disguise_panel` 指定站点
  - `:44333`（naive 端口）NaiveProxy：认证流量走 forward_proxy 隐蔽隧道，未认证流量反代到 `disguise_naive` 指定站点
  - `:80` → 301 重定向到 `https://域名`（443）
- **两个伪装站点均可在 Web 后台「面板设置」页独立配置**（`panel.json` 字段）：
  - `disguise_panel` — `:443` 直访伪装（默认 `proxy:https://example.com`）
  - `disguise_naive` — naive 端口伪装（默认 `proxy:https://example.com`）
  - 值格式：`proxy:<URL>` 反代指定站点；或 `page` 用 `/var/www/html` 默认页
  - 后台修改后 caddy 自动重载（无需 SSH）
- 证书换真实证书后，浏览器直访 `https://your-domain.com` 显示绿色锁 + 伪装站内容
- ⚠️ **NaiveProxy 是普通代理服务，不参与任何远端落地**（caddy 与 sing-box 是独立进程，跨进程路由不可行）
- ⚠️ **NaiveProxy 凭证不能含空格/制表符/换行/花括号 `{}`**（v1.5.23）：凭证写入 Caddyfile `basic_auth user pass` 指令，含这些字符会破坏 Caddyfile 语法 → caddy 无法启动。面板「服务管理」页改凭证时前端 + 后端均校验拦截；`ansgo-genconf` 生成后用 `caddy validate` 预检兜底。🎲 随机生成的凭证是纯字母数字（安全）

> ⚠️ **NaiveProxy 端口不要用 443**：443 留给纯反代伪装站（保证域名直访有效）。naive 用非标准端口（默认 44333），客户端带端口连接。

### 5.2 AnyTLS（sing-box inbound）
- sing-box v1.13.13，`/etc/sing-box/config.json` 内的 `type: anytls` inbound
- TLS 用共享真实证书，SNI = `your-domain.com`，客户端**去掉 `insecure=1`**
- `padding_scheme` 用 sing-box 内置默认

### 5.3 Shadowsocks（sing-box inbound）
- 同一 sing-box 进程的 `type: shadowsocks` inbound
- 加密：`2022-blake3-aes-128-gcm`，密钥 base64(16 bytes)

### 5.4 SOCKS5（sing-box inbound，新增）
- 同一 sing-box 进程的 `type: socks` inbound（tag `socks-in`）
- **强制用户名/密码鉴权**（`users` 字段非空），不支持无鉴权模式（避免开放代理风险）
- 凭证存 `secrets.env`（`SOCKS_USER` / `SOCKS_PASS`），面板「服务管理」页可改 / 随机生成
- 默认端口随机生成（v1.5.12 起部署随机），可用 `--socks-port` / `--socks-user` / `--socks-password` 指定
- ⚠️ SOCKS5 走 sing-box direct 出口，**不参与远端落地**（与 SS/AnyTLS 第一组一致）

### 5.5 落地服务（可选；v1.5.26 起支持多落地服务）
- **默认空列表**（`panel.json` 的 `landings: []`），面板「落地服务」页点「➕ 新增」创建。每个落地服务独立配置：
  - 一个 **anytls 入站**（独立端口/凭证），tag = `landing-in-<id>`
  - 可选一个 **远端落地出口**（独立开关）：关闭则该落地走 sing-box direct；启用则经远端服务器落地
- **远端协议（v1.5.26 新增 SOCKS5）**：每个落地的远端出口支持二选一：
  - **Shadowsocks**（`remote_type:"ss"`）：`remote_host`/`remote_port`/`remote_method`/`remote_password`（SS2022 校验密钥长度）
  - **SOCKS5**（`remote_type:"socks"`）：`remote_host`/`remote_port`/`remote_user`/`remote_password`（强制鉴权）
- **生成路由规则**（genconf 遍历 `landings` 数组）：每个启用且 remote_enabled 的落地生成 `{ inbound: ["landing-in-<id>"], outbound: "landing-out-<id>" }`
- **凭证**：`ansgo-admin regen-landing <id>` 生成（`LANDING_<id>_PASS`/`LANDING_<id>_UUID`，存 secrets.env）；面板「落地服务」页「🎲 重置凭证」调用它
- ⚠️ **NaiveProxy / 第一组 SOCKS5 不参与远端落地**（架构约束）：naive 在 caddy 进程、第一组 socks 走 direct。只有落地服务（landings 数组里的）才路由到远端出口。若需隐藏中转 IP，创建落地服务并启用远端。
- **数量不限**：每个落地 = sing-box 一个 inbound+outbound，用户自负端口占用（genconf 端口冲突检测拦截同进程撞端口）
- **向后兼容（v1.5.26）**：旧的硬编码单 AnyTLS-2（`group2_enabled`/`anytls2_port`/`ss_landing_*`）由 `upgrade.sh` 自动迁移为 `landings[0]`，secrets 里 `ANYTLS2_*` rename 为 `LANDING_1_*`，老用户升级无感

### 5.6 落地服务器 Shadowsocks / SOCKS5
- 独立部署在中转机之外的另一台服务器，跑一个 ss-server 或 socks5 服务（direct 出口）
- SS 一键部署：`bash install.sh --landing [--port 8388]`；SOCKS5 用任意 socks5 服务端（如 sing-box socks inbound + users）
- 配置信息（host/port/method/password 或 host/port/user/password）在中转机面板「落地服务」页对应落地卡片填写，保存后中转 sing-box 自动重载
- 密钥校验：面板对 `2022-blake3-aes-128-gcm` 校验密钥长度（base64(16字节)），对 SOCKS5 校验用户名+密码非空

### 5.7 客户端连接参数（部署完成后填充）
> 占位，部署脚本会自动生成并写入 §10 和 `/etc/ansgo/secrets.env`

---

## 6. Web 管理面板（ansgo-panel）

### 6.1 技术栈
- **Go 单二进制**（mac 本地交叉编译 linux/amd64，scp 上传），运行内存 ~15-20MB
- **vanilla JS + 单 HTML 文件**（不引前端框架，离线 qrcode.min.js 生成客户端二维码）
- 配置：`/etc/ansgo/panel.json`；会话+锁定：SQLite `/etc/ansgo/sessions.db`（CGO-free，纯 Go 驱动 modernc.org/sqlite 避免 libc 依赖）
- systemd unit：`/etc/systemd/system/ansgo-panel.service`

### 6.2 访问方式
```
https://your-domain.com:15608/<随机URL路径>/
```
- 随机 URL 路径：部署时自动生成（如 `/x7k2m9q3/`），面板内可改
- 全程 TLS（共享 Let's Encrypt 证书）

### 6.3 认证（安全机制）
| 项 | 默认 | 可改 |
|----|------|------|
| 管理员用户名 | `ad_admin` | ✅ 面板内改 |
| 管理员密码 | 部署时随机生成强密码，**只显示一次** | ✅ 面板内改 |
| 密码存储 | bcrypt hash | — |
| 登录失败锁定 | 连续错 5 次 → 该 IP 锁 10 分钟（**按 IP，非全局**）| 锁定阈值/时长可改 |
| 会话有效期 | 8 小时 | ✅ 面板内改 |
| 忘记密码 | Web「忘记密码？」页显示 `SSH 执行: ansgo-admin panel-pass` 提示；命令打印新密码 | — |

### 6.4 功能模块（中文 UI，支持暗黑/白天双主题切换 + 移动端自适应 + 左侧可折叠导航，localStorage 记忆）
1. **登录页**（含「忘记密码？」命令提示）
2. **仪表盘**：各代理服务状态灯 + 开关 / 端口 / 内存 / TCP 连接数 / 负载 / 运行时长 / 证书倒计时 + **每服务「🔍 检测」按钮**（v1.5.12，调 `api/health` 三合一诊断：systemd active + 端口 LISTEN + TCP 握手）。**v1.5.20 起仅显示 4 个主要代理服务**（AnyTLS / NaiveProxy / Shadowsocks / SOCKS5），管理面板状态移至顶栏圆点（见 #9）
3. **节点信息**（v1.5.12 重构，v1.5.18 连接地址改 IP，v1.5.19 二维码改浮动）：**只显示已启用的服务**（未启用不渲染卡片，避免空 URI 误导）；每张卡按"连接地址/端口/加密方式/密码/用户名/SNI"分行展示（**v1.5.19 起 NaiveProxy 补全 SNI + 密码**），**每行独立「📋 复制」按钮**；**v1.5.18 起「连接地址」优先显示服务器公网出口 IP（Go 端 UDP 探测，进程级缓存，探测失败回退域名），URI 仍是域名**（TLS 协议 SNI 需域名）；URI 单独成行带复制；**v1.5.19 起二维码默认隐藏**，服务名旁加「📱 二维码」按钮，点击浮动展示（点空白关闭），不再常驻占版面。落地服务启用时仅显示 anytls-2（标注出口经 SS 落地）。**v1.5.19 服务顺序统一 AnyTLS → NaiveProxy → Shadowsocks → SOCKS5**
4. **服务管理** ⭐（v1.5.18 起「服务控制」菜单已删，服务管理成为唯一总操作页；v1.5.11 起由「服务安装」+「端口管理」+「密钥管理」三页合并；v1.5.12 加检测）：每服务一张卡片，一站式完成：① 状态标签（未安装/已安装·运行中/已安装·未运行）② Shadowsocks/AnyTLS/SOCKS5/NaiveProxy 独立安装/卸载 ③ 各服务端口 + 面板端口均可改（v1.5.12 起部署默认随机）④ 手动输入自定义密钥（SS/AnyTLS/SOCKS5/Naive，SS2022 自动校验 base64(16字节) 长度），**v1.5.18 起每个密钥/凭证输入框右侧带独立 🎲 按钮（纯前端 crypto.getRandomValues 生成，不自动保存）** ⑤ **v1.5.18 起操作按钮集中一行自适应**（按可用性）：未安装 `[📥安装]`；已安装 `[💾应用][▶️启动/⏹停止][🔄重启][📤卸载][🔍检测][🔧修复]`，「💾应用」串行调 portHandler+keyHandler 一次保存端口+密钥（零新增端点）⑥ **「🔍 检测」按钮**（v1.5.12，v1.5.18 起并入操作行末位）。**v1.5.20 起移除底部 AnyTLS-2 卡片**（AnyTLS-2 管理统一移至「落地服务」页）。手动设置走 Go 直接写 secrets.env（原子 tmp+rename，避开 sed 特殊字符坑）；整服务随机重置走 ansgo-admin regen/regen2（单字段随机已改前端 genField 不调后端）
5. **落地服务** ⭐（**v1.5.26 重写为多落地服务动态列表**，替代旧的单 AnyTLS-2 两张固定卡片）：
   - **动态列表 + 新增按钮**：页面顶部「➕ 新增落地服务」按钮，弹窗填名称+端口创建（后端分配 id + 生成 anytls 凭证 + genconf+restart+verify）
   - **每张落地卡片**（同一区配置入站 + 远端）：名称 / 端口 / 启用开关 / AnyTLS 密码（🎲随机）+ **远端落地出口区**（启用远端开关 / 类型下拉 Shadowsocks▼/SOCKS5▼ / 远端地址 / 端口 / 按类型显隐字段：SS=加密方式+密钥，SOCKS5=用户+密码）+ 操作行 `[💾保存][🎲重置凭证][🔍检测][🗑删除]`
   - **远端类型切换**动态显隐字段（SS/SOCKS5），`toggleRemoteType()` JS 切换 sswrap/sockswrap 显隐
   - **同步事务**：所有写操作（新增/保存/删除/重置凭证）走 v1.5.24 的 `genconfRestartVerify`（genconf→restart→verify active），非 fire-and-forget
   - **校验**：端口冲突（sing-box 同进程）+ SS2022 密钥长度 + SOCKS5 凭证非空，失败即时反馈
   - ⚠️ **架构约束告知**：只有落地服务（landings）才路由到远端出口；NaiveProxy / 第一组 SOCKS5 不参与远端落地
6. **证书管理**：⭐ **证书来源切换**（acme 自动 / manual 手动指定证书+私钥完整路径）+ 到期时间 + 手动续期（acme）/ 重新加载（manual）+ 上次续期结果
7. **面板设置**：网页标题 / URL 路径 / 会话 / 管理员账号密码 / 面板端口 / 锁定阈值 / 服务器公网 IP（v1.5.18，VPC 必填 + 🔍 自动检测按钮）/ 伪装站点。**v1.5.19 起 `--no-caddy` 模式（caddy_enable=false）时隐藏「直访伪装(:443)」**（该站点由 nginx 等接管，caddy 不再生成，改了也不会生效）；Naive 伪装始终显示。**v1.5.22 起保存逻辑修复**：仅 url_path/panel_port 真正变化才重启（旧版每次保存都重启）；`saveSet()` 防御 disguise_panel 输入缺失（`--no-caddy` 场景）
8. **日志查看**：tail 最近 N 行
9. **顶栏管理面板状态圆点**（v1.5.20）：顶栏用户名右侧一个小圆点，绿=运行中 / 红=未运行 / 灰=获取失败，hover 显示「管理面板：运行中 :端口」。登录时 + 每次切换页面时静默刷新（`refreshPanelDot()` 拉 `api/dashboard`，fire-and-forget 不阻塞页面加载）。**管理面板状态从仪表盘移出**（避免与代理服务并列显示冗余），全局常驻顶栏可见
10. **侧栏底部版本号**（v1.5.22）：左侧导航栏底部常驻显示 `ANS-GO vX.Y.Z`（`api/auth` 返回后端 `-ldflags` 注入的 `version` 字段，`checkAuth` 填入 `#verTag`）。用户升级后一眼可核实是否生效（折叠态自适应为居中小字）。**解决「我升级了但不知道成没成」**——之前版本号只在启动日志 `journalctl` 里，用户不一定会查

### 6.5 面板端口"可改"的技术机制（重要，必须诚实告知用户）
面板端口写在 `config.json`，Go 二进制启动时读取。Web 改端口的流程：
1. 面板写新端口到 `config.json`
2. 同步放行防火墙新端口
3. 弹明确提示：「面板将在 3 秒后重启到新端口 XXXX，请用 `https://your-domain.com:XXXX/路径` 重新访问」
4. `systemctl restart ansgo-panel`（当前会话断开是必然的，因为端口换了）
5. 用户用新端口重新登录（会话因重启清空，需重新登录）

这不是"自指灾难"——是一次性受控重启换端口，有清晰提示。**部署文档和 Web 界面都要写明这一点。**

---

## 7. ansgo-admin 离线管理脚本

位置：`/usr/local/bin/ansgo-admin`（bash，零依赖，面板全挂也能用）

```bash
ansgo-admin status              # 各服务+面板状态一览
ansgo-admin info                # 打印连接参数 + URI
ansgo-admin restart [ss|anytls|socks|naive|panel|all]
ansgo-admin stop [服务]
ansgo-admin logs [服务]          # tail journalctl
ansgo-admin regen [ss|anytls|socks|naive]   # 重置密钥（提示确认）
ansgo-admin regen-landing <id>  # 生成/重置单个落地服务凭证（LANDING_<id>_*，v1.5.26）
ansgo-admin regen2              # [已弃用] 转发到 regen-landing 1
ansgo-admin group2 [status]     # [已弃用] 落地服务请在面板「落地服务」页管理
ansgo-admin cert status         # 证书到期
ansgo-admin cert renew          # 手动续期
ansgo-admin panel-pass          # 重置面板密码（打印新密码）
ansgo-admin panel-path          # 重置面板 URL 路径（兜底）
ansgo-admin panel-port          # 重置面板端口（兜底）
ansgo-admin firewall [list|open PORT|close PORT]
ansgo-admin update [sing-box|caddy|panel]   # 升级二进制
ansgo-admin backup              # 备份所有配置到 /etc/ansgo-backup-{ts}/
ansgo-admin restore [备份目录]   # 回滚
ansgo-admin uninstall           # 卸载面板管理组件（保留配置备份）；彻底卸载用 install.sh --uninstall --purge
```

---

## 8. 服务器文件清单（部署后产物）

```
/usr/local/bin/
  ├── sing-box              # 代理服务载体（ss + anytls + socks5 + 落地 anytls-2，按需启用）
  ├── caddy                 # naive 分支（:443 伪装站始终跑 + naive 按需）
  ├── ansgo-admin           # bash 离线管理脚本
  ├── ansgo-genconf         # python3 配置生成器（按服务开关生成）
  ├── ansgo-cert-reload     # 证书续期重载脚本
  ├── ansgo-cert-issue.sh   # 证书签发脚本（可重跑）
  └── ansgo-panel           # Go Web 管理面板二进制

/etc/ansgo/
  ├── panel.json            # 端口/URL路径/网页标题/账号/会话/锁定/服务开关/伪装/落地配置
  ├── secrets.env           # 所有协议密钥（SS/ANYTLS/SOCKS/NAIVE + 落地 ANYTLS2）
  └── sessions.db           # 会话 + 锁定计数 (sqlite)

/etc/ssl/ansgo/
  ├── fullchain.pem         # Let's Encrypt 真实证书（续期自动覆盖）
  └── privkey.pem

/root/.acme.sh/
  ├── dnsapi/dns_dynukey.sh # 路径 A 钩子（API Key）
  ├── account.conf          # 含路径 B 的 Dynu_ClientId/Secret（降级用）
  └── your-domain.com_ecc/  # acme.sh 证书存储

/etc/sing-box/config.json   # genconf 生成（按 svc_*_enabled 开关）
/etc/caddy/Caddyfile        # genconf 生成（:443伪装 + naive按需）
/var/www/html/              # page 模式伪装默认页

/etc/systemd/system/
  ├── sing-box.service      # 代理服务（按需启动）
  ├── caddy.service         # :443伪装始终启动
  └── ansgo-panel.service   # 面板

/etc/ansgo-deploy/          # install.sh 下载的脚本副本（含 ansgo-landing.sh 等）
/etc/ansgo-backup-{ts}/     # 每次改配置前的备份
/etc/sysctl.d/99-proxy-tune.conf   # stage1 网络调优（+ SSH 加固 sysctl 段，见 §14）
/etc/security/limits.d/99-proxy.conf  # stage1 fd 上限
/etc/ssh/sshd_config.d/10-hardening.conf   # SSH 加固 drop-in（见 §14）
```

---

---

## 9. 部署架构：面板优先 + 代理服务面板内按需安装

> v1.3.0 架构变更：install.sh **只装面板 + 证书**，代理服务（NaiveProxy/AnyTLS/Shadowsocks）改为**登录面板后在「服务安装」页按需启用**。
>
> v1.4.0 新增 **Docker 一体化形态**：`install.sh --docker` 在 KVM 主机用单容器（`ghcr.io/jiasongji/ansgo:latest`，all-in-one）跑全套——容器内 systemd 作 PID 1，复用裸金属全部 unit/脚本/面板代码（systemctl/journalctl/ansgo-admin 原生可用，**面板 Go 代码与 bash 脚本 0 改动**），配置/密钥/证书持久化到 docker volume，host 网络 + privileged。LXC/低配仍走裸金属。

### install.sh 阶段（装面板 + 证书）

| 步骤 | 动作 |
|------|------|
| 0 | stage1 系统调优（BBR/TFO/fd/journald，幂等）|
| 1 | 下载脚本 + 装 ansgo-admin/genconf/cert-reload |
| 2 | 下载 sing-box / caddy-naive 二进制（就位，不启动）|
| 3 | 生成面板配置（panel.json，**代理服务默认 svc_*_enabled=false**）+ 密钥 |
| 4 | 签发证书（acme.sh DNS-01，A 默认 B 降级）|
| 5 | 生成配置（代理服务关闭，仅占位）|
| 6 | 部署 systemd unit + **启动 caddy（:443 伪装站）**|
| 7 | 部署 + 启动 ansgo-panel |

### 面板内阶段（按需装服务）

用户登录 `https://域名:15608/路径/` 后：

1. **「服务安装」页**：逐个安装 Shadowsocks / AnyTLS / NaiveProxy（点「安装」即生成配置 + 启动；「卸载」则停止 + 移配置，密钥保留可重装）
2. **「端口管理」页**：调整各服务端口
3. **「面板设置」页**：配置 :443 直访伪装站 / naive 伪装站（默认反代 example.com）
4. **「落地服务」页**（v1.5.12 起由「第二组服务」+「出口落地」合并）：可选启用额外 anytls-2 + naive-2 + 远端 SS 落地服务器配置。⚠️ 只有 AnyTLS-2 经 SS 落地（隐藏中转 IP），NaiveProxy-2 走 direct（caddy/sing-box 分离架构约束）
5. **「出口落地」页** → 已合并入「落地服务」页（v1.5.12）

### 为什么这样设计
- **面板与代理解耦**：面板和 :443 伪装站随安装立即可用；代理服务按需开启，不装不占端口
- **caddy 始终运行**：:443 伪装站 + :80 跳转是域名基础设施，不随代理服务卸载而停（sing-box 无 inbound 时才会停）
- **服务开关持久化**：`panel.json` 的 `svc_ss_enabled` / `svc_anytls_enabled` / `svc_naive_enabled` 字段控制 genconf 生成对应配置

### 实战备注（通用运维规律）
- **scp 覆盖运行中二进制会失败**（sftp 报 `dest open Failure`），且后续 restart 只重启旧文件。正确流程：`systemctl stop` → `rm` → `scp` → `md5sum` 对比 → `systemctl start`。md5 是判断"是否真更新"的唯一手段。
- **caddy reload 必失败**（Caddyfile 设 `admin off`），改配置用 restart。
- **前端 SPA「点击无反应」= JS 语法错误**：整块 `<script>` 不解析 → 所有函数未定义 → 静默。诊断：`node --check` 查语法。
- **长任务用后台守护**：SSH 长连接易超时，`nohup ... > log 2>&1 &`。
- **改配置前必备份**：`ansgo-admin backup` → `/etc/ansgo-backup-{ts}/`。升级二进制前手动备份：`cp /usr/local/bin/ansgo-panel /etc/ansgo-backup-update-vX.Y.Z-{ts}/ansgo-panel.old`。
- **前端改动后必须重新编译 + 上传**：HTML 经 `//go:embed` 编译进 Go 二进制，改 `deploy/panel/web/index.html` 后须 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` 重新出包 + 上面的 stop→rm→scp→md5→start 流程，浏览器硬刷新。
- **公网 IP 获取的三层优先级**（v1.5.18 VPC 修正）：① 用户在「面板设置」手动填写的 `server_ip`（最高，VPC/NAT 下唯一可靠来源）② UDP "连接" 8.8.8.8:80 探测本机出口（`net.Dial` 取 `LocalAddr`，不真正发包只走路由表，进程级缓存 `sync.Once`）③ 空 → 前端回退域名。**UDP 探测需过滤内网**：VPC/NAT 下出口走内网网卡（10./172.16-31./192.168./100.64.），公网 IP 在 NAT 网关做 SNAT 本机无从得知，`isPrivateIP()` 判定为内网则丢弃并回退域名，同时在节点页顶部和设置页显示引导提示。
- **主动检测公网 IP 必须用户点击触发**（v1.5.18）：`/api/detect-public-ip` 调用第三方 echo 服务（ipify/ifconfig/icanhazip 三选一兜底），**仅当用户在面板设置页点「🔍 自动检测」按钮才外发一次**（不在启动时自动外发，避免每次启动把"这台机器存在"告知第三方）。检测结果填入输入框供用户确认后保存，不直接写配置。这与 §13「禁用真实第三方域名作为 disguise 默认值」不冲突——后者限制的是 caddy 反代目标硬编码默认值，这里是用户主动触发的运行时调用。
- **Mac 本机无 Go 时的交叉编译**（v1.5.18）：用 `docker run --rm -v "$PWD":/src -w /src -e GOPROXY=https://goproxy.cn,direct -e HTTP_PROXY= -e HTTPS_PROXY= ... golang:1.26-alpine` 编译。**必须清空 `-e HTTP_PROXY=` 等环境变量**（orbstack/docker daemon 默认透传代理到容器内 `127.0.0.1:1666`，容器内访问不到会 `connection refused`）。go.mod 的 `go 1.26.4` 要求镜像版本 ≥ `golang:1.26`。
- **合并操作按钮时优先复用现有 API 端点**（v1.5.18）：把端口「应用」+ 密钥「保存」整合成单个「💾 应用」按钮时，前端 `svcSave()` 串行调既有 `portHandler`+`keyHandler` 即可，**不要新增 `/api/svc-save` 端点**（重复实现校验 = 扩大回归面）。校验/重启逻辑已在两个 handler 内成熟稳定。
- **Docker 升级必须 `--force-recreate`**（v1.5.19）：`docker compose up -d` 在镜像 digest 未变（或 compose 配置哈希相同）时会**跳过 recreate**，容器不重建 → entrypoint 不重跑 → 旧二进制继续运行，表现为「拉了新镜像但没变化」。upgrade.sh docker 分支改用 `up -d --force-recreate` 强制销毁重建（volume 数据不丢）。版本号验证从不匹配升级为 err（而非 warn），并给出 `md5sum` 对比 + force-recreate 排查命令。
- **bash `echo` 不解释 `\033`，含颜色码必须用 `printf '%b'`**（v1.5.19）：upgrade.sh 末尾「升级完成」段用 `echo "${C_B}..."` 输出颜色码，bash 的 echo 默认把 `\033[36m` 原样输出成字面文本（用户看到的"乱码"）。前面的 `log/ok/hr` 用 `printf` 所以正常。修复：所有含颜色变量的输出统一 `printf '%b%s%b\n' "$C_B" "$val" "$C_0"`，`%b` 显式要求解释反斜杠转义。**教训：脚本里输出 ANSI 颜色一律用 printf，不用 echo。**
- **「重启触发条件」必须用真实变化判断，不能只判「字段存在」**（v1.5.21）：`settingsHandler` 的 `url_path` 分支写成 `if b.URLPath != nil { ... needRestart = true }`，而前端 `saveSet()` 每次都把当前 `url_path` 原样回传（非「改动」）→ **只改网页标题或服务器 IP 也会触发面板重启**。重启覆盖层闪现 + 重定向到正在重启的面板（连接被拒），用户表现为「点保存无反应 / 刷新后状态异常」。正确范式是同函数内 `panel_port` 的 `*b.PanelPort != c.PanelPort` 守卫——所有「副作用动作」（重启/重载/重建）一律按 `新值 != 当前值` 判断，前端表单全量回传字段是常态不能假设「字段出现=用户改动」。修复后新增 Go 集成回归测试（`TestSettingsSave_UntouchedFieldsDoNotRestart` + `TestSettingsSave_UrlPathChangeStillRestarts`）锁死两条路径。
- **「只提交源码不发版」= 没修复**（v1.5.22，最高优先级教训）：v1.5.21 修复了源码并 commit，但**没创建 GitHub release + 没上传二进制资产 + 没 `docker buildx --push` 重建镜像**。用户在服务器执行 `upgrade.sh` 后，裸金属分支从 `releases/download/v1.5.21/` 拉 404（release 不存在）→ md5 对比无变化 → 跳过替换；Docker 分支拉 `ghcr.io/jiasongji/ansgo:latest`（digest 未变）→ `--force-recreate` 重建但镜像内还是旧二进制。**结果：bug 照旧，用户以为修复无效**。发版必须三件套同步：①`gh release create vX.Y.Z` ②上传 6 个资产（`ansgo-panel-linux-{amd64,arm64}` + `caddy-naive-linux-{amd64,arm64}` + `sing-box-linux-{amd64,arm64}.tar.gz`，sing-box 资产可复用上游不变则跳过）③`docker buildx build --push -t ghcr.io/jiasongji/ansgo:latest -t ghcr.io/jiasongji/ansgo:vX.Y.Z`。**自检**：发版后 `gh release view vX.Y.Z --json assets` 确认资产齐 + `docker pull` 后 `docker run --rm ... md5sum` 对比。为了让用户能自验，v1.5.22 起面板侧栏底部常驻显示版本号。
- **前端读 DOM 的 helper 必须防御 null**（v1.5.22）：`saveSet()` 的 `g=id=>$('#s_'+id).value` 在 `--no-caddy` 模式下 `s_disguise_panel` 不渲染 → `$('#s_disguise_panel')` 返回 null → `.value` 抛 TypeError → 整个 `saveSet` 中断，用户「点保存无反应」且无 toast。条件渲染的字段（`disguise_panel` 随 `caddy_enable` 显隐）必须用 `const g=id=>{const e=$('#s_'+id);return e?e.value:''}`。**通用规律：任何 `.value`/`.textContent`/`.checked` 读取前都要确认元素存在**，条件渲染 + 全量读取是经典组合坑。
- **用户可控凭证插进结构化配置文件必须校验元字符**（v1.5.23）：NaiveProxy 用户名/密码被 `ansgo-genconf` 直接插进 Caddyfile 的 `basic_auth myuser mypass` 指令。**含空格/`{}`/`#` 等的密码会破坏 Caddyfile 语法** → `caddy validate` 失败 → caddy 重启失败 → Naive 无法运行（AnyTLS/SS 走 sing-box 不受影响，故「改 Naive 密码只挂 Naive」）。`keyHandler` 旧版对 SOCKS5 做了 `ContainsAny(": \t\r\n")` 校验却**漏了 NaiveProxy**（复制粘贴遗漏，同 v1.5.21 的 url_path 守卫遗漏同源）。修复用**三层防御**：①前端 `svcSave` 正则 `/[ \t\r\n{}]/` 拦截即时反馈 ②后端 `keyHandler` 拒绝（防 API 绕过）③**`genconf` 写完 Caddyfile 后 `caddy validate` 预检，失败回滚旧配置不重启**（终极兜底——即使前两层都漏，也永远不会把坏配置喂给 caddy 让它挂掉）。**通用规律**：任何「用户输入 → 字符串拼接进 Caddyfile/Nginx conf/systemd unit/JSON」的路径，都必须在生成后用对应工具 validate（`caddy validate` / `nginx -t` / `sing-box check` / `jq empty`），失败回滚。go func 里 `genconf && restart` 要检查 genconf 退出码，失败不重启。
- **「异步 fire-and-forget + 忽略错误」是配置一致性的天敌**（v1.5.24，最高优先级）：`keyHandler` 改 NaiveProxy 凭证的旧流程是 `go func { genconf caddy; systemctl restart caddy }()` ——两步都在 goroutine 里且 `_ =` 忽略错误，HTTP 响应立即返回 `{ok:true}`。**问题链**：写 secrets.env（同步成功）→ 异步 genconf（可能失败/被杀）→ 异步 restart（可能 deactivating）→ 用户收到 ok:true 但实际 caddy 没用新配置跑起来 → secrets.env(新)与 Caddyfile(旧)不一致。**NaiveProxy 的 probe_resistance 让这种不一致极具迷惑性**：认证失败时静默走伪装（反代照常打开 200），而非返回 407，用户看到「反代正常」就以为服务好的，实则代理认证全失败。正确做法：**改凭证 = 同步事务**（genconf → restart → 轮询验证 `systemctl is-active` 真的 = active），任一步失败立即报错并给出诊断。**systemctl restart 成功 ≠ 服务起来了**：caddy 配置错误会进 deactivating/failed，restart 命令本身返回 0；必须额外 `is-active` 轮询确认（等待 2-4 秒）。**无法自动验证代理认证可用**时（需要真实客户端），提供「一键修复」入口（genconf+restart+验证）让用户能自助恢复一致状态。
- **caddy 同站点块内多 handler 的指令排序是个大坑**（v1.5.25，NaiveProxy 代理不通的真正根因）：Caddyfile 同一站点块（如 `:44333 { ... }`）内同时写 `forward_proxy`（代理）和 `reverse_proxy`（伪装反代）时，**caddy 默认指令排序把 forward_proxy 放在 reverse_proxy 之后**——即使全局加了 `order forward_proxy before reverse_proxy` 也无效（caddy 对同站点块内 handler 共存的排序有坑，全局 order 不覆盖）。后果：NaiveProxy 客户端的 CONNECT 请求先被 reverse_proxy（伪装站）拦截处理，forward_proxy 永远拿不到代理流量 → 「检测正常、反代正常、但代理不能用」。官方 naiveproxy 示例用 `file_server`（天然排在 forward_proxy 后）规避了此问题，我们用 `reverse_proxy` 做伪装才踩坑。**修复**：用 `route {}` 块包裹站点内指令，强制按书写顺序执行（forward_proxy 写最前）。**诊断方法**：`caddy adapt --config Caddyfile --adapter caddyfile` 输出 JSON，看该站点 route 的 `handle` 数组里 forward_proxy 是否排第一；不是则用 route 块重排。**通用规律**：caddy 任何「多 handler 共存 + 需要特定执行顺序」的场景，用 `route {}` 块显式排序，不要依赖全局 order 指令。

---

## 10. 风险与回滚

> 只列**与当前代码仍相关、操作时仍可能踩**的通用风险。历史 bug 的根因诊断见对应版本的 GitHub Release notes。

| 风险 | 应对 |
|------|------|
| 证书签发失败 | acme.sh 详细日志；失败时保留现有自签证书继续服务，不影响运行；A（API Key）失败自动降级 B（OAuth2）|
| 改配置导致服务起不来 | 每次改动前 `ansgo-admin backup` 到 `/etc/ansgo-backup-{ts}/`，`ansgo-admin restore` 一键回滚 |
| 面板 Go 二进制崩溃 | systemd `Restart=on-failure` 自动重启；`ansgo-admin` SSH 兜底 |
| 改面板端口/路径后失联 | SSH 进去 `ansgo-admin panel-port` / `panel-path` 重置（**走密钥 + 非标端口，见 §14**）。v1.5.12 起面板端口默认随机，新部署务必记下 install.sh 输出（或读 `/etc/ansgo/panel.json`）|
| SSH 加固后失联 | drop-in 备份在 `/etc/ansgo-backup-ssh-harden-{ts}/`；密钥登录走 `~/.ssh/your_key` + 非标端口（见 `.secrets.local`）；完全失联只能通过宿主 console 修复 |
| 面板二进制更新不生效 | scp 覆盖运行中二进制会静默失败；必须 **stop→rm→scp→md5 校验→start**（见 §9）。容器改前端/二进制须 `docker compose up -d --build` 重 build 镜像 |
| 面板点击无反应 | 前端 JS 语法错误致整块脚本失效；改完 HTML 须重新编译上传 + 浏览器硬刷新清缓存 |
| 续期 reload 失败 | caddy `admin off` 无法 reload；`ansgo-cert-reload` 已改用 restart，续期闪断 1-2s |
| 落地密钥错误 | sing-box `bad key length` 崩溃；面板对 2022-blake3 密钥做长度校验拒错；SSH 改正确后重启 |
| 手动证书路径错误 | manual 模式证书路径不存在/不可读会让三服务启动失败；面板与 install.sh 写入前预校验；改坏可 SSH 把 `panel.json` 的 `cert_mode` 改回 `acme` 后重启。Docker manual 证书路径必须挂进容器（或走卷内副本，见 §4）|
| Docker 容器 systemd 起不来 | 必须 `privileged: true` + `cgroup: host` + tmpfs `/run`；host 网络下端口与宿主冲突需先释放 |
| 落地服务架构约束 | caddy（NaiveProxy）与 sing-box（SS/AnyTLS/SOCKS5）是**两个独立进程**，跨进程路由物理上不可行；只有 anytls-2 经 ss-out 落地，naive/socks5 走 direct |
| IPv6 入站不可用 | 宿主防火墙限制，容器侧无解；仅用 IPv4 |
| 已装 nginx 的服务器（80/443 冲突）| 用 `--no-caddy` 模式让 nginx 接管 80/443，caddy 只听 naive 端口 |
| LXC 时钟不同步致 SS2022 `bad timestamp` | LXC 容器时钟由宿主控制，`timedatectl set-ntp` 无效；须单独装 `openntpd`（SS2022/WireGuard 都依赖时钟）|
| 宝塔/aaPanel iptables policy=DROP | 安全插件会把 INPUT policy 改 DROP，install.sh 的 nft 规则在 iptables-legacy 后端不生效；须 `iptables -I INPUT -p tcp --dport <PORT> -j ACCEPT` 逐端口放行 |

---

## 11. 一键部署（推荐入口）

仓库根目录提供 `install.sh`，支持**交互式**与**带参数一键**两种模式，所有资源取自本仓库 GitHub。

> v1.3.0 起 install.sh **只装面板 + 证书 + :443 伪装站**，代理服务登录面板后到「服务安装」页按需启用。

### 交互式
```bash
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)
# 先显示主菜单：1) 安装/部署  2) 卸载（保留配置/卷）  3) 彻底卸载  4) 部署落地服务器
# 选 1 后依次交互输入：域名、Dynu API Key（或 OAuth Client ID+Secret）、各端口、面板用户名等
```

### 带参数一键（全参数示例）

裸金属（LXC / 低配推荐，systemd 直管三服务）：
```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --dynu-key <API_KEY> \
             --email you@example.com \
             --ss-port 33899 \
             --anytls-port 21111 \
             --naive-port 44333 \
             --panel-port 15608 \
             --panel-user admin \
             --disguise-panel proxy:https://example.com \
             --disguise-naive proxy:https://example.com \
             --non-interactive
```

Docker 一体化（KVM / 资源充裕推荐，仅加 `--docker`，其余参数一致）：
```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --dynu-key <API_KEY> \
             --email you@example.com \
             --ss-port 33899 \
             --anytls-port 21111 \
             --naive-port 44333 \
             --panel-port 15608 \
             --panel-user admin \
             --disguise-panel proxy:https://example.com \
             --disguise-naive proxy:https://example.com \
             --docker \
             --non-interactive
```

参数全集（完整说明见 GitHub README「参数全集」表）：`--domain`（必填）`--dynu-key`（或 `--dynu-client-id`+`--dynu-secret`，acme 模式必填）`--email` `--ss-port`(默认 23456) `--anytls-port`(8443) `--socks-port`(10808) `--naive-port`(44333) `--panel-port`(15608) `--panel-user`(admin) `--disguise-panel` `--disguise-naive` `--cert-mode`(acme|manual，默认 acme) `--cert-fullchain`(manual 模式证书完整路径) `--cert-privkey`(manual 模式私钥完整路径) `--docker` `--no-caddy`(v1.5.6+，不部署 caddy 的 80/443，让 nginx 等接管) `--non-interactive` `--force-bin`。
>
> **⭐ v1.5.5 新增：密码/密钥参数化（全部可选，留空则随机生成）**：
> - `--ss-password KEY`：Shadowsocks 密钥，须 base64(16字节)，生成命令 `openssl rand -base64 16`
> - `--anytls-password PASS`：AnyTLS 密码（非空即可）
> - `--anytls-uuid UUID`：AnyTLS 用户 UUID，标准格式如 `a1b2c3d4-e5f6-7890-abcd-ef1234567890`
> - `--socks-user USER`：SOCKS5 用户名（不含冒号和空白，强制鉴权）
> - `--socks-password PASS`：SOCKS5 密码（不含冒号和空白）
> - `--naive-user USER`：NaiveProxy 用户名（不含冒号和空白，caddy basic_auth 限制）
> - `--naive-password PASS`：NaiveProxy 密码（不含冒号和空白）
> - `--panel-password PASS`：面板管理员密码（6-64 字符，默认随机）
> - `--panel-url-path PATH`：面板 URL 路径（/xxxx/ 形式，默认随机）
>
> 所有参数都经过 `validate_inputs()` 校验，端口额外检查范围（1-65535）+ 互相冲突 + 与 caddy/SSH 固定端口（80/443/25822）冲突。Docker 形态通过 ansgo.env 的 `SS_KEY_IN`/`ANYTLS_PASS_IN` 等 `_IN` 后缀变量透传给容器 entrypoint.sh。

**全参数 + 自定义密码示例**（v1.5.5+，适合需要预先确定全部凭证的场景）：
```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --dynu-key <API_KEY> \
             --email you@example.com \
             --ss-port 33899 --ss-password $(openssl rand -base64 16) \
             --anytls-port 21111 --anytls-password MyAnyTLSPass2026 \
             --anytls-uuid $(cat /proc/sys/kernel/random/uuid) \
             --naive-port 44333 --naive-user myuser --naive-password MyNaivePass2026 \
             --panel-port 15608 --panel-user admin \
             --panel-password MyPanelPass2026 --panel-url-path /my-panel/ \
             --non-interactive
```

> **手动证书模式（与 acme 二选一）**：`--cert-mode manual --cert-fullchain /path/to/fullchain.pem --cert-privkey /path/to/privkey.pem`，无需 Dynu 凭证，跳过 acme 签发。Docker 模式需保证证书路径已挂载进容器（`docker-compose.yml` 的 volumes）。

落地服务器专用：`bash install.sh --landing [--port 8388]`（在该机部署独立 ss-server，供中转机第二组接入）。

卸载（自动检测 Docker / 裸金属）：

**默认卸载**（移除服务/容器/二进制，保留配置/卷）：
```bash
bash install.sh --uninstall
```

**彻底卸载**（删除配置/密钥/证书/卷/镜像/调优，不可恢复）：
```bash
bash install.sh --uninstall --purge
```
- **默认**：停服务/删容器/删二进制与 unit，**保留** `/etc/ansgo` `/etc/ssl/ansgo` 及 docker 卷（可重装不丢参数）
- **`--purge`**：上述全部 + 删配置/密钥/证书/acme 状态/备份/sysctl 调优/docker 卷与镜像（docker 本体保留，可能被其他服务使用）
- 卸载前二次确认；Docker 分支用 `docker compose down -v` + `docker rm -f ansgo` 兑底 + 卷名模式匹配兑底删卷，避免 compose project 名不一致导致遗漏
- ⚠️ `curl ... | bash -s -- --uninstall` 历史问题已 **v1.5.3 根治**（bootstrap 落地机制：检测管道/进程替换运行时先落地临时文件再 exec 重跑，bash 从文件读，curl 能完整输出，二者解耦；详见风险表）。所有 curl|bash 形式（`--uninstall` / `--purge` / `--landing` / `--non-interactive` 全参数部署 / 交互式主菜单）均通过 PTY + 回归测试验证

### 资源来源（全部自有 GitHub / 官方上游）
- 脚本/源码：`raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/...`
- ansgo-panel 二进制：`github.com/jiasongji/ANS-GO/releases/download/vX.Y.Z/ansgo-panel-linux-<arch>`
- sing-box：**v1.5.3 起裸金属优先从 SagerNet 官方 release 下载**（`github.com/SagerNet/sing-box/releases/download/v1.13.13/sing-box-1.13.13-linux-<arch>.tar.gz`，多架构），本项目 release vendored 作为兜底。Docker 在 `Dockerfile.allinone` 内同样从 SagerNet 拉取
- caddy（naive 分支）：从 klzgrad/forwardproxy 源码用 xcaddy 编译。**裸金属 install.sh 优先拉本项目 release 预编译产物**（v1.5.2 release 已上传双架构），**失败则现场 xcaddy 编译**（自动装 Go 官方 1.22 二进制——apt 的 1.19 不支持 xcaddy 的 `toolchain` 指令；需 git，约 3-5 分钟）；Docker 在 `Dockerfile.allinone` 内现场编译
- ⚠️ **release 资产维护**：每次发新版本 release 必须上传全部 6 个资产（`ansgo-panel-linux-{amd64,arm64}` + `caddy-naive-linux-{amd64,arm64}` + `sing-box-linux-{amd64,arm64}.tar.gz`）。v1.5.0 release 漏传 caddy/sing-box 资产导致 v1.5.1/v1.5.2 部署 404（已在 v1.5.2 release 补齐）。caddy-naive 本地交叉编译：`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 xcaddy build --with github.com/caddyserver/forwardproxy=<本地forwardproxy naive分支> --output caddy-naive-linux-amd64`
- Docker 一体化镜像（all-in-one：sing-box + caddy + 面板 + systemd，单容器跑全套）：`ghcr.io/jiasongji/ansgo:latest`（**v1.5.6 已发布公开多架构镜像** amd64+arm64，312MB，含 OCI labels；服务器直接 `docker compose pull` 成功，无需本地构建。多阶段构建定义见 `deploy/Dockerfile.allinone` + `deploy/docker/entrypoint.sh`）。**更新镜像命令（开发机执行）**：`docker buildx build --builder <builder> --platform linux/amd64,linux/arm64 --build-arg HTTP_PROXY=http://host.docker.internal:1666 --build-arg HTTPS_PROXY=http://host.docker.internal:1666 -t ghcr.io/jiasongji/ansgo:latest -t ghcr.io/jiasongji/ansgo:vX.Y.Z -f deploy/Dockerfile.allinone . --push`（国内首次构建需 `docker buildx create --driver docker-container` + 代理注入）
- Docker 面板单镜像（仅面板，兼容用）：`ghcr.io/jiasongji/ansgo-panel:latest`（见 `deploy/Dockerfile`）

> **两种部署形态**：LXC / 低配（256MB）用裸金属（`install.sh`，systemd 直管三服务，内存最小）；KVM / 资源充裕用 Docker 一体化（`install.sh --docker`，host 网络 + privileged，单容器内 systemd 复用全部 unit/脚本/面板代码，面板 0 改动）。

### 已部署服务器升级（`deploy/upgrade.sh`）

**v1.5.16 新增**专门的跨版本升级脚本，区别于 install.sh（全新部署）和 `ansgo-admin update`（仅更新单个二进制，不更新脚本/不补字段）。`upgrade.sh` 把「更新 3 组件 + 补配置字段 + 备份 + 形态自动检测」打包成一条 `curl | bash`：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/upgrade.sh | bash
# 参数：--docker | --metal（强制形态）/ --ver v1.5.21 / --yes（跳过确认）
```

| 形态 | 升级动作 | 备份位置 |
|------|---------|---------|
| **裸金属** | ① 更新 `ansgo-genconf` + `ansgo-admin` 脚本（raw 拉取）② 更新 `ansgo-panel` 二进制（release，md5 对比相同则跳过重启）③ python3 幂等补 `panel.json`（`socks_port` 随机不冲突 / `svc_socks_enabled:"false"` / `panel_title`）④ 幂等补 `secrets.env`（`SOCKS_USER`/`SOCKS_PASS`）⑤ 重启 ansgo-panel | `/etc/ansgo-backup-upgrade-{TS}/` |
| **Docker** | `$COMPOSE pull` → `$COMPOSE up -d --force-recreate`（复用 volume；**v1.5.19 起必须 `--force-recreate`**，否则镜像 digest 未变时跳过重建 → entrypoint 不重跑 → 旧二进制继续运行）| compose 目录 `ansgo-etc-vol-backup-{TS}.tgz` |

**设计约束（脚本自包含，不依赖服务器旧版 ansgo-admin，避免鸡生蛋）**：
- `VER` 硬编码（与 install.sh 一致），发新版只改这一行 + commit
- bootstrap 落地机制移植自 install.sh v1.5.3（解决 `curl | bash` 的 SIGPIPE + 进程替换卡死）
- `ansgo-panel` 无 `-version` flag（main.go 仅 `-setpass`），靠 **md5 对比**判断是否真更新，启动后用 `journalctl` 日志行 `ansgo-panel v1.5.21 监听...` 验证版本
- SOCKS5 升级后默认 `svc_socks_enabled:"false"`（不启用，符合「面板内按需装服务」架构）
- `--docker`/`--metal` 互斥；同时检测到两种形态标记返回 `ambiguous` 要求显式指定
- ⚠️ **bash set -u 陷阱**（v1.5.5 教训）：变量后紧跟全角标点（如 `$DOCKER_COMPOSE_FILE，`）会被当成另一个变量名，必须用 `${DOCKER_COMPOSE_FILE}` 界定

---

## 12. 后续流程

1. ✅ **自测审计（已完成）**：`ansgo-admin status` + 面板全功能 + 服务安装/卸载 + 多协议连通 + IP 锁定 + 证书真实性（Let's Encrypt）均验证通过
2. ✅ **GitHub 建项（已完成）**：公开仓库 `ANS-GO`，含 AGENTS.md + `deploy/`（脚本 + 面板源码）+ `install.sh`（一键部署），**不含** `.secrets.local`/`.build`
3. ✅ **服务器部署（已完成 + 持续迭代）**：面板版本迭代走 §9「stop→rm→scp→md5→start」流程（裸金属）或 `docker compose up -d`（Docker），当前 v1.5.25
4. **客户端实测（可选）**：用真实客户端（Clash.Meta / sing-box / naive 客户端）测各协议连通与分流

---

## 13. 约束与原则（给执行 AI）

- 默认使用简体中文
- 优先最小修改，完成后自行验证并汇报
- 敏感凭证绝不写入会进 git 的文件（AGENTS.md / 脚本 / 教程）
- 🚫 **严禁泄露真实部署身份信息**（最高优先级硬规则）。凡进 git 的文件（文档/脚本/源码/示例）一律不得出现任何可关联到具体服务器的真实信息，**只能用占位符**：
  - **真实公网 IP**（服务器/中转机/落地机/攻击源）→ `<服务器IP>` / `<中转服务器IP>` / `<落地服务器IP>` / `<攻击者IP>`
  - **真实域名**（主域/中转域/落地域）→ `your-domain.com` / `your-relay-domain.com` / `your-landing-domain.com`
  - **真实端口**（部署实例端口，非 install.sh 默认值）→ `<服务端口>` / `<面板端口>`；文档里列默认端口（15608/33899/21111 等）属脚本默认值不算泄露
  - **面板 URL 路径** / 节点标签 → `<面板URL路径>` / `#ANS-GO-SS`（用项目名，禁用服务器代号）
  - **密钥文件名 / SSH 别名 / 服务器代号** → `~/.ssh/your_key` / `ansgo-server`（禁用真实命名）
  - **第三方伪装反代目标域名**（caddy `disguise_*` 默认值）→ `example.com`（禁用任何真实第三方域名作为代码默认值）
  - **具体部署时间戳/备份目录名/证书有效期** → `<TIMESTAMP>` / `<部署日期>`（禁用真实日期）
  - **云厂商/地理位置**（如腾讯云/硅谷）→ `LXC 容器` / `海外 VPS`（不点名厂商与机房位置）
  - changelog 记录排障经验时**只保留技术结论与触发条件**，抹掉所有可定位服务器的实例信息（IP/域名/端口/路径/时间戳/厂商）。
  - **提交前自检**：对全仓做真实 IP/域名/面板路径/私钥名/伪装域名/时间戳的 grep 扫描，必须 0 命中（§14 规则示例行本身除外）。
  - ⚠️ 历史教训：v1.5.13/14 的 changelog 曾嵌入大量真实 IP/域名/面板路径/实例端口/私钥名/伪装域名/备份时间戳，已用 git-filter-repo 改写全历史清理。新增 changelog 条目严禁重蹈覆辙。
- 每个高风险操作（改配置、重启服务）前自动备份
- 不擅自加防火墙 drop 规则（避免锁死 SSH）
- 所有生成密钥用 `openssl rand`，base64 密钥用标准 base64（不是 urlsafe）
- Go 交叉编译用 `CGO_ENABLED=0`（纯静态，无 libc 依赖）
- 执行前先读本文件 §0-§14：§11（一键部署）为推荐入口，§9（部署架构）说明面板优先设计，§14（SSH 加固）为部署后动作，每步报告进度

---

## 14. SSH 加固（部署后动作）

> ⚠️ **本章节是"部署后动作"，不在 `install.sh` 流程内。** 新部署仍是默认 22 端口，需手动复刻下述步骤。触发原因是原配置 `Port 22 + PermitRootLogin yes + PasswordAuthentication yes` 是首要攻击面（暴破日志密集）。

### 加固范围

| 项 | 决策 | 说明 |
|----|------|------|
| 认证方式 | ✅ 公钥登录 + 禁密码 | ED25519，密钥不入 git（见 `.secrets.local`）|
| root 登录 | ✅ 保持直登 | `PermitRootLogin prohibit-password`（仅公钥，禁密码）|
| 防火墙 | ❌ 不动 | nftables policy=accept（LXC 安全由宿主负责）|
| MACs | ❌ 不限制 | 确保任意 IP + 任意客户端 + 密钥均可登录 |
| 新软件 | ❌ 不装 | 零额外开销，纯 drop-in 配置 + sysctl 追加 |

### 落地配置

**SSH drop-in**：`/etc/ssh/sshd_config.d/10-hardening.conf`（OpenSSH first-match-wins，覆盖主 `sshd_config`）
```sshd_config
Port 25822
PermitRootLogin prohibit-password   # root 仅公钥
PasswordAuthentication no           # 全局禁密码
KbdInteractiveAuthentication no
MaxAuthTries 3                      # 原 6 → 3
LoginGraceTime 30                   # 原 120 → 30
X11Forwarding no
AllowAgentForwarding no
```

**`ssh.socket` 必须 mask**：Debian 12 用 socket activation，`ssh.socket` 配置 `ListenStream=22`——若不 mask，重启后 22 端口会被重新拉起，绕过 drop-in。`systemctl mask ssh.socket`（软链到 `/dev/null`），保证 22 永久关闭。

**sysctl 追加**（`/etc/sysctl.d/99-proxy-tune.conf` 末尾）：
```
net.ipv4.conf.all.send_redirects = 0
net.ipv4.conf.default.send_redirects = 0
net.ipv4.conf.all.log_martians = 1
net.ipv4.conf.default.log_martians = 1
```
> `kernel.kptr_restrict` / `kernel.kexec_load_disabled` 因 LXC 宿主只读无法设置（容器内 `permission denied`），勿加。

### 下次登录方式
```bash
ssh ansgo-server                                    # 已配 ~/.ssh/config 别名
# 或
ssh -i ~/.ssh/your_key -p 25822 root@<服务器IP>
```
- ⚠️ **密码 + 22 端口已永久失效**，必须用密钥 + 25822
- 密钥路径 / 指纹 / config 别名见 `.secrets.local`（不入 git）

### 失联回滚
```bash
# 假设还能通过密钥 + 25822 进去：
rm /etc/ssh/sshd_config.d/10-hardening.conf
systemctl unmask ssh.socket
systemctl restart sshd
# sysctl 回滚：
cp /etc/ansgo-backup-ssh-harden-<TIMESTAMP>/99-proxy-tune.conf /etc/sysctl.d/
sysctl -p /etc/sysctl.d/99-proxy-tune.conf

# 若密钥也丢了（完全失联）：只能通过宿主 LXC console 进入容器修复
```

### 复刻要点（新服务器加固时务必）
1. 本地 `ssh-keygen -t ed25519 -f ~/.ssh/your_key -N ""`
2. 上传公钥到 `/root/.ssh/authorized_keys`（**密码仍开**，保命）
3. **新终端验证密钥 + 22 登录成功** → 才能进入下一步（关键防失联）
4. 备份 → 写 drop-in → `sshd -t` 语法校验
5. **当前 22 密码会话不退** → `systemctl restart sshd` → 新终端密钥 + 25822 验证
6. `systemctl mask ssh.socket`（防 socket activation 复活 22）
7. sysctl 加固追加 + `sysctl -p`
8. 负面测试：密码 + 25822 应被拒；22 端口应 `Connection refused`
9. 确认 ansgo 三服务仍 active（加固零影响）

### 备份
- `/etc/ansgo-backup-ssh-harden-<TIMESTAMP>/`：含 `sshd_config` + `sshd_config.d/` + `99-proxy-tune.conf`（加固前原状）
