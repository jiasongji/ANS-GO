# ANS-GO 代理服务器方案 (AGENTS.md)

> 本文件是项目的**唯一事实来源**。新窗口执行、GitHub 教程撰写、服务器部署均以本文件为准。
> 敏感凭证见 `.secrets.local`（已 gitignore，不入库）。
>
> **部署状态：✅ 已部署并端到端验证。** 可复现产物在 `deploy/`，一键部署见 §12。
>
> **当前版本：v1.5.13**（install.sh 脚本版本；**面板 Go 二进制 + ghcr.io 镜像同步升级到 v1.5.12**，含 5 项 UX 优化 + 4 个落地服务致命 bug 修复 + 镜像二进制固化修复）。版本历史见 GitHub Releases。
>
> - **v1.5.13**：**根治「重新部署后面板看不到」—— ghcr.io 镜像里固化的二进制版本严重滞后于源码**。**用户场景**：TX-SVC 服务器（43.135.148.135，Docker + 宝塔 nginx + `--no-caddy` + manual 证书），用户昨天能访问面板，今天重新跑 install.sh v1.5.12 部署后面板"看不到"。**诊断结论**：① 面板实际可用（容器内 `https://127.0.0.1:10568/TX_SVC/` HTTP 200 + 外网 `https://svc.giize.com:10568/TX_SVC/` 也 HTTP 200，HTML 含 v1.5.12 全部特征字符串）；② **真正的 bug**：ghcr.io/jiasongji/ansgo:latest 镜像里固化的 ansgo-panel 仍是 v1.5.0 旧二进制（镜像创建于 2026-06-22 16:31，那次只 push 了镜像 shell，没把 v1.5.1~v1.5.12 的 Go 代码改动重新编译进去），用户一旦 `docker compose pull && up -d` 重建容器，二进制回退到 v1.5.0——这就是"昨天好的、今天重新部署就不行"的根因。**修复三件套**：① `main.go:78` 硬编码 `version = "1.5.0"` → `"1.5.12"`（v1.5.1~v1.5.12 连续 11 个版本从未更新版本字符串，日志一直印 `ansgo-panel v1.5.0` 误导排查）；② 重新 `docker buildx build --platform linux/amd64,linux/arm64 --push` 多架构构建并推送 ghcr.io 镜像，把 v1.5.12 Go 二进制固化进镜像层；③ 构建器必须用 `docker-container` driver 并通过 `--driver-opt env.HTTPS_PROXY=...` 注入代理（buildkit 解析 `# syntax=docker/dockerfile:1` 前端镜像时就要访问 docker.io，`--build-arg HTTP_PROXY` 只作用于 Dockerfile 内的 `RUN`，覆盖不到这一层）。⚠️ **经验教训：每次发新版本 release 必须同步重新构建 ghcr.io 镜像**（`docker buildx build ... -t ghcr.io/jiasongji/ansgo:latest -t ghcr.io/jiasongji/ansgo:vX.Y.Z --push`），不能只 commit 源码——Docker 用户拉的是镜像，镜像不更新就等于没更新；版本字符串不要硬编码在 `var version = "..."`，应该用 `-ldflags "-X main.version=vX.Y.Z"` 在构建时注入（本次暂未改，留作后续优化）；排查"面板打不开"先 `docker exec ansgo md5sum /usr/local/bin/ansgo-panel` 对比镜像内 md5（`docker run --rm --entrypoint md5sum <img> /usr/local/bin/ansgo-panel`），md5 不一致就是镜像固化滞后
> - **v1.5.12**：**5 项面板 UX 优化 + 4 个落地服务致命 bug 根治**（裸金属 + Docker 同步生效）。
>   ① **节点信息页重构**（前端 `loadNode`）：未启用的服务不再显示（之前空 URI 误导用户）；每张卡按"连接地址/端口/加密方式/密码/用户名/SNI"分行展示，每行独立「📋 复制」按钮；URI 单独成行也带复制。落地服务启用时 anytls-2/naive-2 才显示（并标注 anytls-2 出口经 SS 落地 / naive-2 走 direct 的架构约束）。后端 `nodeHandler` 同步加 `enabled` 字段让前端据此过滤。
>   ② **「第二组服务」+「出口落地」合并为「落地服务」单页**（前端导航精简一项）：上半部分配置 AnyTLS-2/NaiveProxy-2（启用/端口/伪装），下半部分配置远端 SS 落地服务器（host/port/method/password）。`api/group2` + `api/landing` 两个后端端点保持不变（合并仅是前端表现层）。
>   ③ **端口全部随机生成**：`install.sh` + `entrypoint.sh` 默认端口从 23456/8443/44333/15608 改为**全部随机**（10000-65535，自动避开 80/443/25822/互相冲突/已占用）。`rand_port()` 在 `validate_inputs` 后填补空端口，部署完成横幅用 ╔═══╗ 边框 + ⚠️ 警示标突出显示。用户可通过 `--ss-port` 等参数显式指定（向后兼容）。
>   ④ **落地服务启用时自动生成密钥**：`group2Handler` 启用分支检测到 `ANYTLS2_PASS`/`NAIVE2_USER` 任一为空时**自动调 `ansgo-admin regen2`** 生成（原代码要求用户先手动点「生成密钥」按钮 → 用户反馈启用后无法用）。生成的密钥可在「服务管理」页底部查看/修改。同时启用 sing-box + caddy 显式 `enable`（之前可能被 disable 起不来）。
>   ⑤ **所有服务加端口监听检测**（新 `api/health` + 前端 UI）：每服务检测 ① systemd active ② 端口 LISTEN（`ss -tln`）③ TCP 自连握手（`net.DialTimeout`）。仪表盘/服务管理页每张卡加「🔍 检测」按钮，结果就近渲染（✅/❌ 行级显示 + 综合诊断）。
>   ⑥ **【Bug修复】sing-box 路由规则引用不存在的 `naive-in2` tag**：`ansgo-genconf` 之前在落地 SS outbound 的路由规则里把 `["anytls-in2", "naive-in2"]` 一起塞进去，但 NaiveProxy 由 **caddy** 承载，sing-box 里**根本没有 naive-in2 这个 inbound**。新版 sing-box 配置校验失败/规则被丢弃 → 第二组流量全部走 direct（用户实测"配置了落地但用不了"的核心根因）。修复：路由规则只引用 `anytls-in2`，并删除冗余的 `action: route` 字段（sing-box 1.11+ 默认 action 就是 route）。
>   ⑦ **【Bug修复】naive-2 走 direct 架构约束明确告知**：之前 UI 文档说"naive-2 出口经 SS 落地"，但 caddy 和 sing-box 是**两个独立进程**，caddy 收到 naive-2 流量后**直连目标站点**（无法转发给 sing-box 的 ss-out，这是 caddy/sing-box 分离架构的固有约束）。修复：节点信息页/服务管理页/落地服务页全部明确标注"naive-2 走 direct"，避免用户对"naive-2 出口 IP"产生错误预期。
>   ⑧ **【Bug修复】`landingHandler` 启用时同步 enable sing-box**：原代码落地 SS 配置变化只 `restart sing-box`，但若 sing-box 之前因无启用服务被 `disable` 了，restart 会失败（unit 未 enable）。修复：检测到有启用服务时显式 `enable` + `restart`。同时返回 `note` 字段告知"未启用落地服务时 ss-out 不生效"。
>   ⚠️ **经验教训：caddy（NaiveProxy）与 sing-box（SS/AnyTLS）是两个独立进程，跨进程路由不可能**——任何"naive-2 流量走 sing-box 的 ss-out 落地"的设计在物理上都不可行，UI/文档必须诚实告知。naive-2 永远走 caddy 的 direct 出口（中转机 IP），只有 anytls-2 能经 sing-box ss-out 落地到远端服务器
> - **v1.5.11**：**前端 UX 重构——「服务安装」「端口管理」「密钥管理」三页合并为统一「服务管理」页**（裸金属 + Docker 同步生效，纯前端改动）。① **新增 `loadMgmt()` 函数**（`index.html`）：一次并行拉取 `api/dashboard` + `api/node`，每服务渲染一张卡片——状态标签（未安装/已安装·运行中/已安装·未运行）+ 端口输入 + 密钥/凭证输入 + 安装/卸载 + 启停按钮 + 🎲随机/💾保存密钥，一站式完成原先跨三页的全部操作。② **导航精简**：删除 `install/port/key` 三个 nav button，新增 `🗂️ 服务管理`（data-t="mgmt"）；`showTab` 派发表加 `mgmt:loadMgmt`（保留 `install:loadInstall/port:loadPort/key:loadKey` 映射不破坏潜在引用，但不再有 nav 入口）。③ **`reloadCurrentTab()` 抽象**：`svcInstall/svc/regen/saveKey` 完成回调从硬编码 `loadInstall/loadSvc/loadKey` 改为 `reloadCurrentTab()`（按当前激活 tab 自动刷新），让这些操作在 mgmt 页面也能正确刷新状态/密钥。④ 保留所有原字段 ID（`#p_ss` `#k_at_pass` 等），`setPort/saveKey/regen/svcInstall` 函数签名 0 改动，后端 API 0 改动。⚠️ **经验教训：HTML 经 `//go:embed` 编译进 Go 二进制，改前端必须重新 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` 出包 + stop→rm→scp→md5→start 流程（AGENTS.md §9），浏览器硬刷新清缓存；动作函数完成回调统一走「刷新当前 tab」而非硬编码本页函数，避免后续合并/拆分页面时回调链断裂**
> - **v1.5.10**：修复 v1.5.9 的 Docker 容器重启回归——**容器重启时 naive 已装但 caddy 被停的 bug**。根因：`entrypoint.sh` 第 143 行只看 `caddy_enable=false` 就 `systemctl stop caddy`，没看 `svc_naive_enabled`。容器重启（`docker-compose up -d`）后即使 naive 已装，caddy 仍被停 → NaiveProxy inactive。修复：`--no-caddy` 模式下检查 `svc_naive_enabled`：naive 已装 → `enable + restart caddy`（仅听 naive 端口，不碰 80/443）；naive 未装 → `disable + stop caddy`（80/443 由 nginx 接管）。`handlers.go` 同步把 caddy 启用条件对齐到 `CaddyEnable=="true" || SvcNaiveEnabled=="true"`。**测试**：`docker-compose pull && docker-compose up -d` 后 naive 应自动恢复 active。
> - **v1.5.9**：修复用户反馈的 SS/AnyTLS 状态耦合 + caddy 自启动问题。**首次自 v1.5.0 改动 Go 面板代码**（main.go + handlers.go），Docker 镜像内面板二进制已更新（裸金属用户需等下一个面板 release tag 才能用上）。① **反馈 1：SS/AnyTLS 状态显示解耦**——`dashboardHandler` 服务状态改为 `enabled && 进程active`，未启用的服务显示 inactive（不再因载体进程在跑而误报 active）。② **反馈 1：停止 SS 不停 AnyTLS**——`serviceHandler` 对 ss/anytls 单独处理：设 enabled=false + genconf + restart sing-box（genconf 按 enabled 生成 inbound，停 SS 后 config 只剩 AnyTLS），不再 systemctl stop sing-box 误伤其他服务。只有 SS+AnyTLS 都未启用时才 stop+disable sing-box。③ **反馈 3：caddy 智能启停**——`Config` struct 加 `CaddyEnable` 字段（解析 panel.json `caddy_enable`，默认 true 兼容旧部署）；`svcInstallHandler` caddy 启用条件改为 `CaddyEnable=="true" || SvcNaiveEnabled=="true"`（默认模式始终跑 :443 伪装站；--no-caddy 模式只在 naive 启用时才 enable+start caddy）。⚠️ **Go 编译坑：`exec.Command(...).CombinedOutput()` 返回 `(output, err)` 两个值，不能 `_ = ...` 赋给单值，用 `.Run()` 替代（只返回 err）**
> - **v1.5.8**：根治 v1.5.7 `--no-caddy` + manual 证书模式部署后服务起不来的 4 个根因（实测：硅谷 VPS + 宝塔 nginx + manual 证书 + --no-caddy + --docker，部署后需要大量手动 SSH 修复才能跑通）。① **ansgo-genconf `caddy_enable=false` 加 `auto_https off`**——caddy 默认 auto_https on 会隐式监听 :80 做 ACME 挑战，--no-caddy 模式下 nginx 占着 80 → bind 失败；修复后 global block 自动加 `auto_https off`。② **install.sh manual 证书改由 entrypoint 同步**——v1.5.7 让 genconf 直接读宿主 `/www/server/...` 路径，但容器内 SELinux/权限 denied；改为 install.sh 只注入 bind mount，实际 cp 由 entrypoint 完成。③ **entrypoint.sh manual 证书启动时同步到 `/etc/ssl/ansgo/` + 改 panel.json 为 acme**——启动时 cp 宿主证书 → 卷（644 权限），清理 panel.json 的 `cert_fullchain/cert_privkey`，`cert_mode` 改 acme，让 genconf 用 `/etc/ssl/ansgo/`（容器完全控制，无权限问题）。续期只需 `docker restart ansgo`。④ **entrypoint.sh `--no-caddy` 模式 `mask` → `disable`**——v1.5.7 用 mask 让 systemctl 无法操作，naive 装上时无法 unmask 启动；改 disable + stop（不 mask），允许面板手动 start caddy。**实测验证**：部署后 caddy/sing-box/ansgo-panel 全 active，AnyTLS 21002 + NaiveProxy 21008 + 面板 10568 全部监听，nginx 继续占 80/443 无冲突。
> - **v1.5.7**：修复 v1.5.6 `--no-caddy` + manual 证书模式部署后面板打不开的 3 个根因（实测场景：硅谷 VPS + 宝塔 nginx + manual 证书 + --no-caddy + --docker）。① **entrypoint.sh 在 --no-caddy 模式 mask caddy.service**——原问题：panel.json 写了 `caddy_enable=false`，但镜像里 caddy.service 已 enable，systemd 仍会拉起 caddy 占 443（无证书起不来导致整体 systemd 状态混乱）；修复：NO_CADDY=1 时显式 `systemctl disable + mask caddy.service`，systemd 永远拉不起。② **install.sh manual 证书模式自动注入 bind mount**——原问题：宝塔证书在宿主 `/www/server/...`，容器内看不到 → entrypoint 报「证书文件不存在」；修复：CERT_MODE=manual 时用 awk 在 docker-compose.yml 的 volumes 段追加 `- /宿主证书目录:/容器同路径:ro` 让 entrypoint 能读到（awk 跨平台，避免 macOS sed -i 与 GNU sed 语法差异）。③ **docker compose v1/v2 自动检测**——服务器装的是 `docker-compose` v1（独立二进制）而非 `docker compose` v2（子命令），原代码 `docker compose pull 2>/dev/null` 吞 stderr 误判为「拉取失败 → 本地构建」；修复：检测两个变体选可用那个，compose pull 失败再 docker pull 兜底，不吞 stderr。
> - **v1.5.6**：① **新增 `--no-caddy` 模式**（nginx 共存）——caddy 不监听 `:80`/`:443`，让已装 nginx/宝塔/其它 web 服务器接管 80/443；ANS-GO 面板/SS/AnyTLS/Naive 仍按各自端口跑（naive 装上后 caddy 只听 naive 端口，不碰 80/443）。交互式模式检测到 80/443 占用时主动提示是否跳过 caddy。`panel.json` 新增 `caddy_enable` 字段（`true` 默认；`--no-caddy` 写 `false`），`ansgo-genconf gen_caddy()` 据此条件化生成 `:443`/`:80` 块。端口校验：`--no-caddy` 时 80/443 不再视为保留（用户可自由用）。② **修复 Docker 部署 2 个 bug**：(a) `dl_or_exit: command not found`——`dl_or_exit` 定义在 `do_docker_deploy` 调用之后（bash 单遍解析），移到文件顶部日志函数之后；(b) `lstat /etc/ansgo-docker/deploy`——`docker build -f deploy/Dockerfile.allinone` 用相对路径但 cwd 是 `/etc/ansgo-docker`，改绝对路径 `-f /tmp/ansgo-build/deploy/Dockerfile.allinone`。③ **发布 ghcr.io 多架构公开镜像** `ghcr.io/jiasongji/ansgo:latest`（amd64 + arm64，312MB，含 sing-box v1.13.13 + caddy-naive + 面板 + systemd 单容器）——所有服务器 `install.sh --docker` 直接 `docker compose pull` 成功，不再触发本地构建回退。Dockerfile 加 OCI labels（`org.opencontainers.image.source`）便于未来 GitHub 关联。⚠️ **经验教训：bash 函数必须先定义后调用，跨函数引用要确认定义顺序；docker build 的 `-f` 相对路径按 cwd 解析，跨目录构建必须用绝对路径；国内 docker buildx 多架构构建需要 `docker-container` driver + `--build-arg HTTP_PROXY=...` 注入代理（apt/go 不读环境变量但读 build-arg）；ghcr.io package visibility 修改 API 对 user-owned package 返回 404，必须 web UI 操作**
> - **v1.5.5**：① **带参数安装支持指定端口 + 密码**——新增 7 个 CLI 参数：`--ss-password`（须 base64(16字节)）/`--anytls-password`/`--anytls-uuid`（标准 UUID）/`--naive-user`/`--naive-password`（不含冒号空白）/`--panel-password`（6-64 字符）/`--panel-url-path`（/xxxx/ 形式），全部可选，留空则随机生成（向后兼容）。裸金属 + Docker 双形态都支持（host ansgo.env 用 `SS_KEY_IN`/`ANYTLS_PASS_IN` 等 `_IN` 后缀变量透传给 entrypoint.sh，避免与容器内同名变量冲突）。② **新增 `validate_inputs()` 参数校验**——端口范围（1-65535）+ 互相冲突 + 与 caddy/SSH 固定端口（80/443/25822）冲突检测；SS2022 密钥 base64 解码长度校验；UUID 格式校验；NaiveProxy 用户名/密码字符校验；面板密码长度校验；URL 路径格式校验。校验仅对安装/部署场景生效，`--uninstall`/`--landing` 跳过。③ **顺手修 NaiveProxy 默认端口 443→44333**——项目长期存在 `install.sh`/`entrypoint.sh`/`ansgo-admin` 默认 443、但 AGENTS.md §3 明确警告「naive 不要用 443」的矛盾，本次统一为 44333（与文档/ansgo-genconf 默认一致）。⚠️ **经验教训：bash 的 `$var` 后紧跟非 ASCII 字符（如全角`）`）在 set -u 下会被当成 `var）` 变量名报 unbound，须用 `${var}` 显式界定；heredoc 里的反引号代码示例会被执行，文档示例改用单引号包裹**
> - **v1.5.4**：修复**面板「服务安装」首次安装代理服务必失败**（`生成配置失败: FileNotFoundError: '/etc/sing-box/config.json'`）。**根因**：install.sh 装面板阶段（步骤 1-2）装了 sing-box 二进制和 systemd unit，但**从未创建 `/etc/sing-box/` 目录**；步骤 5/7 调 `ansgo-genconf all` 初始化占位配置时同样会失败，但被 `2>&1 | tail -3` 静默吞掉让部署继续走完，**问题被掩盖直到用户在面板点「安装」**才冒泡给前端（`exec.Command(genconf).CombinedOutput()` 直接把 traceback 返回给浏览器）。修复双保险：① `ansgo-genconf` 的 `gen_singbox()`/`gen_caddy()` 在 `open(...,"w")` 前 `os.makedirs(parent, exist_ok=True)` 治本（脚本自身不再依赖外部预建目录）② `install.sh` 步骤 2/8 预 `mkdir -p /etc/sing-box /etc/caddy /var/www/html` 防御（与 `Dockerfile.allinone:117` / `entrypoint.sh:31` 对齐，裸金属此前漏建）。⚠️ **经验教训：`ansgo-genconf` 这类「写配置」工具必须自建父目录，不能假定上游已建；install.sh 里 `cmd | tail` 这种吞错管道会掩盖致命错误，应在被吞的命令上加 `|| warn` 至少留诊断痕迹**
> - **v1.5.3**：根治 `curl|bash` 系列问题。**三个根因**：① `curl: (23) Failure writing output`——v1.5.1/v1.5.2 都在治 `read` 读 stdin 的症状，但真正根因是 `curl | bash` 下脚本中途任何 `exit`（do_uninstall/--landing/--help/错误退出）让 bash 提前结束 → 管道读端关闭 → curl 写剩余字节收到 **SIGPIPE**；② **进程替换模式 `bash <(curl)` 卡死**——bootstrap 用 `[ -f /dev/fd/NN ]` 判断进程替换，但 fd 在 `-f` 测试中返回 false（非常规文件）→ 误走 `cat`（无参数）从 fd0 读 → fd0 是用户终端 → 吃掉用户输入卡死；③ **sing-box/caddy 二进制 404**——install.sh 从本项目 release 下载二进制，但 release 未上传这些资产。修复：① bootstrap 落地机制（检测管道/进程替换→落地临时文件→exec 重跑）② 进程替换判断改用 `[ -e ]`（fd 通过 `-e` 测试）+ `cat "$_ansi_self"`（从 /dev/fd/NN 读，不碰 fd0）③ sing-box 改从 **SagerNet 官方 release** 下载（与 Dockerfile/ansgo-admin 一致），caddy-naive 加 xcaddy 现场编译回退。同时①新增**交互式主菜单**（无参数运行显示：安装/卸载/彻底卸载/落地）②修复 `--landing` 首次执行下载 + `--port` 参数透传。⚠️ **经验教训：`curl | bash` 下 SIGPIPE 是 `exit` 引起的；进程替换下 `[ -f ]` 对 fd 失败必须用 `[ -e ]`；二进制下载不能依赖单一源**
> - **v1.5.2**：（已被 v1.5.3 取代）曾用 `readtty()` 子 shell 局部 `</dev/tty` 修复 v1.5.1 回归（治对了 `read` 但没治 `exit` 的 SIGPIPE 根因）
> - **v1.5.1**：（已被 v1.5.2 取代）曾尝试用 `exec 0</dev/tty` 修复 `curl|bash --uninstall` 确认失效，但该写法本身在管道下会截断脚本（见 v1.5.2）；文档改动（卸载命令拆分独立代码块）保留
> - **v1.5.0**：①「密钥管理」页支持**手动设置**各服务密码（SS/AnyTLS/Naive + 第二组 AnyTLS-2/Naive-2，与随机生成并存，SS2022 自动校验密钥长度）②**手动指定证书**与私钥的完整路径（`cert_mode=manual`，与 acme 二选一；面板「证书管理」页可切换来源 + 重新加载；install.sh 新增 `--cert-mode/--cert-fullchain/--cert-privkey`）③全参数一键安装支持手动证书
> - **v1.4.3**：面板导航改左侧可折叠侧边栏（桌面可折叠 + 移动端抽屉式，localStorage 记忆）+ 修复白天模式下字体不可读（active 项改蓝底白字 / `<code>` 显式着色 / overlay 阴影双主题适配 / `.logs` 终端风格双主题统一）
> - **v1.4.2**：新增 `--uninstall` / `--purge` 彻底卸载（自动检测 Docker/裸金属，两级清理：默认保留配置/卷，`--purge` 全删）
> - **v1.4.1**：修复 install.sh 架构判断 `x86_64: unbound variable` + 补全 release 二进制（sing-box/caddy-naive/panel 双架构）
> - **v1.4.0**：移动端自适应 + 白天主题文字修复 + Docker all-in-one 一体化镜像

---

## 0. 项目目标

在低配 VPS（LXC 或 KVM）上，部署 **Web 管理面板** + 可选的代理服务，支持两种部署形态：

- **裸金属脚本**（LXC / 低配 256MB 推荐）：`install.sh` 直装，systemd 管理三服务，内存占用最小
- **Docker 一体化**（KVM / 资源充裕推荐）：`install.sh --docker`，单容器（all-in-one：sing-box + caddy + 面板 + systemd）跑全套

要求：

- **面板优先架构**：install.sh 只装面板 + 证书，代理服务在 Web 后台「服务安装」页按需启用
- **三协议代理**（按需）：NaiveProxy + AnyTLS + Shadowsocks（2022）
- **落地服务 + 链式出站**：可选额外 anytls-2 + naive-2（**仅 anytls-2 出口经 SS 走另一台落地服务器**，naive-2 走 direct；caddy/sing-box 分离架构约束，v1.5.12 起明确告知）
- **真实域名证书**：`your-domain.com`（Let's Encrypt），面板与各服务共享
- **Web 管理面板**：中文、暗黑/白天双主题、**移动端自适应**（侧边栏抽屉 / 表单 label 上置 / 网格单列）、**左侧可折叠导航**（桌面端折叠成图标条 + 移动端汉堡抽屉，localStorage 记忆）、可管全部协议参数 + 证书 + 服务安装/卸载 + 自身配置
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
  │  caddy :443  ← 始终运行（域名直访伪装站，反代 soft.xiaoz.org）│
  │  ansgo-panel :15608  ← Web 管理面板                      │
  └──────────────────────────────────────────────────────────┘
                         │
           用户登录面板 →「服务安装」页按需启用
                         ▼
  ┌─ 面板内按需安装（svc_*_enabled 开关）─────────────────────┐
  │  Shadowsocks ──┐                                         │
  │  AnyTLS     ──┼─ sing-box（无启用 inbound 时不启动）        │
  │  NaiveProxy ──┘── caddy（forward_proxy，与 :443伪装同进程）  │
  │                                                          │
  │  第二组（可选）: anytls-2 + naive-2                        │
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
| NaiveProxy (caddy) | **随机** 10000-65535（v1.5.12 前 44333）| HTTPS forward proxy | 按需安装 | ✅ |
| AnyTLS (sing-box) | **随机** 10000-65535（v1.5.12 前 21111）| TLS | 按需安装 | ✅ |
| Shadowsocks (sing-box) | **随机** 10000-65535（v1.5.12 前 33899）| SS2022 | 按需安装 | ✅ |
| 落地服务 anytls-2 / naive-2 | **随机** 10000-65535（v1.5.12 前 21112 / 44334）| TLS / HTTPS | 按需（anytls-2 走 SS 落地，naive-2 走 direct）| ✅ |
| caddy HTTP（重定向）| `80` TCP | HTTP | 固定 | ❌ |
| SSH | `25822` TCP | SSH | **已加固**（公钥+禁密码，见 §15）| ❌ |

> :443 是**域名直访伪装站**（纯反代，不提供代理），随面板一起启动。NaiveProxy 用独立端口，不要用 443。
> v1.5.12 起：所有服务端口**部署时随机生成**（10000-65535，避开 80/443/25822/已占用），可通过 `--ss-port` 等参数显式指定。已部署服务器端口不变（仅新部署默认值改变）。
>
> ⚠️ **SSH 端口已从默认 22 改为 25822**（2026-06-22 加固，见 §15）。`install.sh` 仍装在 22，加固属部署后动作；新部署若需复刻，加固步骤见 §15。

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

## 5. 三协议配置

### 5.1 NaiveProxy（caddy forwardproxy-naive 分支）
- 二进制：`/usr/local/bin/caddy`（klzgrad/forwardproxy release v2.11.2-naive，含 naive padding 层）
- 配置：`/etc/caddy/Caddyfile`（设 `admin off`，故 reload 不可用，改配置用 restart）
- 关键特性：`probe_resistance`（探测伪装）+ `hide_ip` + `hide_via` + naive padding
- **双伪装架构**（caddy 在三个端口上各起一个 site）：
  - `:443` 纯反代伪装站（浏览器直访命中，**不提供代理**）→ 反代到 `disguise_panel` 指定站点
  - `:44333`（naive 端口）NaiveProxy：认证流量走 forward_proxy 隐蔽隧道，未认证流量反代到 `disguise_naive` 指定站点
  - `:80` → 301 重定向到 `https://域名`（443）
- **两个伪装站点均可在 Web 后台「面板设置」页独立配置**（`panel.json` 字段）：
  - `disguise_panel` — `:443` 直访伪装（默认 `proxy:https://soft.xiaoz.org`）
  - `disguise_naive` — naive 端口伪装（默认 `proxy:https://soft.xiaoz.org`）
  - 值格式：`proxy:<URL>` 反代指定站点；或 `page` 用 `/var/www/html` 默认页
  - 后台修改后 caddy 自动重载（无需 SSH）
- 证书换真实证书后，浏览器直访 `https://your-domain.com` 显示绿色锁 + 伪装站内容

> ⚠️ **NaiveProxy 端口不要用 443**：443 留给纯反代伪装站（保证域名直访有效）。naive 用非标准端口（默认 44333），客户端带端口连接。
- 证书换真实证书后，浏览器直访 `https://your-domain.com` 显示绿色锁

### 5.2 AnyTLS（sing-box inbound）
- sing-box v1.13.13，`/etc/sing-box/config.json` 内的 `type: anytls` inbound
- TLS 用共享真实证书，SNI = `your-domain.com`，客户端**去掉 `insecure=1`**
- `padding_scheme` 用 sing-box 内置默认

### 5.3 Shadowsocks（sing-box inbound）
- 同一 sing-box 进程的 `type: shadowsocks` inbound
- 加密：`2022-blake3-aes-128-gcm`，密钥 base64(16 bytes)

### 5.4 落地服务（可选，原"第二组服务"，v1.5.12 起与"出口落地"合并）
- **默认关闭**，面板「落地服务」页勾选启用才部署。启用后额外起：
  - `anytls-in2`（sing-box，独立端口，**默认随机**，v1.5.12 前为 21112）
  - `naive-2`（caddy forward_proxy，独立端口，**默认随机**，v1.5.12 前为 44334）+ 独立 basic_auth 凭证
- **链式出站（仅 AnyTLS-2）**：AnyTLS-2 的出口流量经 `ss-out`（sing-box shadowsocks outbound）转发到另一台落地服务器的 ss-server（中转→落地架构）。第一组仍走 direct。
- ⚠️ **NaiveProxy-2 走 direct（架构约束，v1.5.12 明确告知）**：naive-2 在 caddy 进程中，caddy 与 sing-box 是两个独立进程，naive-2 流量**无法跨进程转发**到 sing-box 的 ss-out，只能走 caddy 的 direct 出口（中转机 IP）。若需隐藏中转 IP，请使用 AnyTLS-2。
- 生成路由规则（v1.5.12 修复）：`{ inbound: ["anytls-in2"], outbound: "ss-out" }`（之前错误地引用了 `naive-in2` 导致 sing-box 配置校验失败/规则被丢弃）
- 密钥：`ansgo-admin regen2` 生成（ANYTLS2_* / NAIVE2_*，存 secrets.env）；面板「落地服务」页启用时若密钥未生成会**自动调 regen2**（v1.5.12 改进），生成后可在「服务管理」页底部查看/修改

### 5.5 落地服务器 Shadowsocks
- 独立部署在中转机之外的另一台服务器，仅跑一个 ss-server（direct 出口）
- 一键部署：`bash install.sh --landing [--port 8388]`
- 配置信息（host/port/method/password）在中转机面板「出口落地」页填写，保存后中转 sing-box 自动重载
- 密钥校验：面板会对 `2022-blake3-aes-128-gcm` 校验密钥长度（base64(16字节)），错误密钥会被拒

### 5.6 客户端连接参数（部署完成后填充）
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
2. **仪表盘**：各服务状态灯 + 开关 / 端口 / 内存 / TCP 连接数 / 负载 / 运行时长 / 证书倒计时 + **每服务「🔍 检测」按钮**（v1.5.12，调 `api/health` 三合一诊断：systemd active + 端口 LISTEN + TCP 握手）
3. **节点信息**（v1.5.12 重构）：**只显示已启用的服务**（未启用不渲染卡片，避免空 URI 误导）；每张卡按"连接地址/端口/加密方式/密码/用户名/SNI"分行展示，**每行独立「📋 复制」按钮**；URI 单独成行带复制 + 客户端二维码。落地服务启用时显示 anytls-2/naive-2（标注 anytls-2 出口经 SS 落地 / naive-2 走 direct 架构约束）
4. **服务控制**：start / stop / restart（二次确认）
5. **服务管理** ⭐（v1.5.11 起由「服务安装」+「端口管理」+「密钥管理」三页合并；v1.5.12 加检测）：每服务一张卡片，一站式完成：① 状态标签（未安装/已安装·运行中/已安装·未运行）② Shadowsocks/AnyTLS/NaiveProxy 独立安装/卸载 ③ 各服务端口 + 面板端口均可改（v1.5.12 起部署默认随机）④ 手动输入自定义密钥（SS/AnyTLS/Naive + 落地服务 AnyTLS-2/Naive-2，SS2022 自动校验 base64(16字节) 长度）+ 🎲 随机生成 ⑤ 启停按钮（start/stop/restart）⑥ **「🔍 检测」按钮**（v1.5.12）。落地服务（AnyTLS-2/Naive-2）在本页底部当 group2 启用时显示（端口仍走「落地服务」页配置）。手动设置走 Go 直接写 secrets.env（原子 tmp+rename，避开 sed 特殊字符坑）；随机生成走 ansgo-admin regen/regen2
6. **落地服务** ⭐（v1.5.12 起由「第二组服务」+「出口落地」合并为单页）：
   - **上半部分**：AnyTLS-2 + NaiveProxy-2 启用开关 / 端口 / Naive-2 伪装 / 🎲 重新生成密钥 + 💾 保存。启用时若密钥未生成**自动调 `ansgo-admin regen2`**（v1.5.12 改进，原要求用户先手动点「生成密钥」）
   - **下半部分**：远端 SS 落地服务器配置（host/port/method/password + 开关），含 2022-blake3 密钥长度校验
   - ⚠️ **架构约束告知**：只有 AnyTLS-2 经 SS 落地（隐藏中转 IP）；NaiveProxy-2 在 caddy 中走 direct 出口（caddy/sing-box 分离架构约束，物理上无法跨进程转发）
7. **证书管理**：⭐ **证书来源切换**（acme 自动 / manual 手动指定证书+私钥完整路径）+ 到期时间 + 手动续期（acme）/ 重新加载（manual）+ 上次续期结果
8. **面板设置**：URL 路径 / 会话 / 管理员账号密码 / 面板端口 / 锁定阈值 / 两个伪装站点
9. **日志查看**：tail 最近 N 行

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
ansgo-admin restart [ss|anytls|naive|panel|all]
ansgo-admin stop [服务]
ansgo-admin logs [服务]          # tail journalctl
ansgo-admin regen [ss|anytls|naive]   # 重置密钥（提示确认）
ansgo-admin regen2              # 生成第二组密钥（ANYTLS2_*/NAIVE2_*）
ansgo-admin group2 [enable|disable|status] [anytls2_port] [naive2_port]
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
  ├── sing-box              # 代理服务载体（ss + anytls + 第二组 anytls-2，按需启用）
  ├── caddy                 # naive 分支（:443 伪装站始终跑 + naive 按需 + naive-2 按需）
  ├── ansgo-admin           # bash 离线管理脚本
  ├── ansgo-genconf         # python3 配置生成器（按服务开关生成）
  ├── ansgo-cert-reload     # 证书续期重载脚本
  ├── ansgo-cert-issue.sh   # 证书签发脚本（可重跑）
  └── ansgo-panel           # Go Web 管理面板二进制

/etc/ansgo/
  ├── panel.json            # 端口/URL路径/账号/会话/锁定/服务开关/伪装/第二组/落地配置
  ├── secrets.env           # 所有协议密钥（SS/ANYTLS/NAIVE + 第二组 ANYTLS2/NAIVE2）
  └── sessions.db           # 会话 + 锁定计数 (sqlite)

/etc/ssl/ansgo/
  ├── fullchain.pem         # Let's Encrypt 真实证书（续期自动覆盖）
  └── privkey.pem

/root/.acme.sh/
  ├── dnsapi/dns_dynukey.sh # 路径 A 钩子（API Key）
  ├── account.conf          # 含路径 B 的 Dynu_ClientId/Secret（降级用）
  └── your-domain.com_ecc/  # acme.sh 证书存储

/etc/sing-box/config.json   # genconf 生成（按 svc_*_enabled 开关）
/etc/caddy/Caddyfile        # genconf 生成（:443伪装 + naive按需 + naive-2按需）
/var/www/html/              # page 模式伪装默认页

/etc/systemd/system/
  ├── sing-box.service      # 代理服务（按需启动）
  ├── caddy.service         # :443伪装始终启动
  └── ansgo-panel.service   # 面板

/etc/ansgo-deploy/          # install.sh 下载的脚本副本（含 ansgo-landing.sh 等）
/etc/ansgo-backup-{ts}/     # 每次改配置前的备份
/etc/sysctl.d/99-proxy-tune.conf   # stage1 网络调优（+ 2026-06-22 安全加固段）
/etc/security/limits.d/99-proxy.conf  # stage1 fd 上限
/etc/ssh/sshd_config.d/10-hardening.conf  # 2026-06-22 SSH 加固 drop-in（见 §15）
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
3. **「面板设置」页**：配置 :443 直访伪装站 / naive 伪装站（默认反代 soft.xiaoz.org）
4. **「落地服务」页**（v1.5.12 起由「第二组服务」+「出口落地」合并）：可选启用额外 anytls-2 + naive-2 + 远端 SS 落地服务器配置。⚠️ 只有 AnyTLS-2 经 SS 落地（隐藏中转 IP），NaiveProxy-2 走 direct（caddy/sing-box 分离架构约束）
5. **「出口落地」页** → 已合并入「落地服务」页（v1.5.12）

### 为什么这样设计
- **面板与代理解耦**：面板和 :443 伪装站随安装立即可用；代理服务按需开启，不装不占端口
- **caddy 始终运行**：:443 伪装站 + :80 跳转是域名基础设施，不随代理服务卸载而停（sing-box 无 inbound 时才会停）
- **服务开关持久化**：`panel.json` 的 `svc_ss_enabled` / `svc_anytls_enabled` / `svc_naive_enabled` 字段控制 genconf 生成对应配置

### 实战备注（历次部署沉淀）
- **scp 覆盖运行中二进制会失败**（sftp报 `dest open Failure`），且后续 restart 只重启旧文件。正确流程：`systemctl stop` → `rm` → `scp` → `md5sum` 对比 → `systemctl start`。md5 是判断"是否真更新"的唯一手段。
  - v1.4.3 升级实战范例（2026-06-22 验证）：旧 md5 `9379fe9a...`（v1.4.0 期），新 md5 `8113f1b2...`，按上述流程上传后远端 md5 精确匹配，服务秒级 active。
- **caddy reload 必失败**（Caddyfile 设 `admin off`），改配置用 restart。
- **前端 SPA"点击无反应"=JS 语法错误**：整块 `<script>` 不解析→所有函数未定义→静默。诊断：`node --check` 查语法。
- **长任务用后台守护**：SSH 长连接易超时，`nohup ... > log 2>&1 &`。
- **改配置前必备份**：`ansgo-admin backup` → `/etc/ansgo-backup-{ts}/`。升级二进制前也手动备份：`cp /usr/local/bin/ansgo-panel /etc/ansgo-backup-update-vX.Y.Z-{ts}/ansgo-panel.old`（v1.4.3 升级时即用此方式）。
- **前端改动后必须重新编译 + 上传**：HTML 经 `//go:embed` 编译进 Go 二进制，改 `deploy/panel/web/index.html` 后须 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` 重新出包 + 上面的 stop→rm→scp→md5→start 流程，浏览器硬刷新。改完前端用 headless Chrome 截图多主题 × 多视口 × 多状态交叉验证（v1.4.3 即用此法验证 6 场景）。

---

## 10. 部署后产物（线上现状回填）

> ⚠️ 密钥与密码等敏感值不写本文件（§13 约束，本文件会进 git）。
> 完整凭证见服务器 `/etc/ansgo/secrets.env`、`/etc/ansgo/panel.json`，
> 本地镜像见 `.secrets.local`（已 gitignore）的「部署产出」段。

### 客户端连接 URI（结构，密钥见 `.secrets.local`）
```
[1] Shadowsocks : ss://<base64url(method:key)>@your-domain.com:33899#ANS-GO-SS
                  method = 2022-blake3-aes-128-gcm
[2] AnyTLS      : anytls://<password>@your-domain.com:21111/?sni=your-domain.com#ANS-GO-AnyTLS
[3] NaiveProxy  : naive+https://<user>:<pass>@your-domain.com:44333#ANS-GO-Naive
[4] 第二组(可选) : anytls2 :21111 / naive2 :44334  → 出口走 SS 落地
```
> 一键获取完整 URI：服务器执行 `ansgo-admin info`，或面板「节点信息」页。
> 端口均为默认值，可在面板「端口管理」改。

### Web 面板访问
```
URL:      https://your-domain.com:15608/<随机URL路径>/
用户名:   ad_admin
密码:     （部署时一次性显示，存 .secrets.local；遗忘用 `ansgo-admin panel-pass` 重置）
URL路径:  /<随机URL路径>/  （面板内可改；遗忘用 `ansgo-admin panel-path` 重置）
```
> 线上现状（2026-06-22 升级到 v1.4.3）：URL 路径已自定义，凭证见 `.secrets.local`。HTTPS 直访正常（HTTP 200），新版左侧可折叠侧边栏 + 白天模式字体修复已生效。

### 当前端口（默认值，可面板内改）
```
caddy :443 伪装站:  443（始终运行，域名直访）
NaiveProxy(caddy):  44333
AnyTLS(sing-box):   21111
Shadowsocks:        33899
第二组(可选):       anytls2=21112 / naive2=44334（走 SS 落地）
面板(ansgo-panel):  15608
caddy HTTP(重定向): 80
SSH:                25822（2026-06-22 加固：原 22，已改非标端口 + 公钥登录 + 禁密码，见 §15）
```

### 证书
```
签发机构: Let's Encrypt (CN=YE1)
成功路径: A（Dynu API Key，自定义钩子 dns_dynukey.sh）
有效期:   2026-06-19 ~ 2026-09-17（部署时剩余 89 天）
自动续期: acme.sh cron 每日 23:58，ARI 窗口 2026-08-19
续期后:   ansgo-cert-reload 自动 restart caddy/sing-box/ansgo-panel
```

### 服务器文件清单（与 §8 对照，均已落地）
```
/usr/local/bin/{sing-box, caddy, ansgo-admin, ansgo-genconf, ansgo-panel, ansgo-cert-reload, ansgo-cert-issue.sh}
/etc/ansgo/{panel.json, sessions.db, secrets.env}
/etc/ssl/ansgo/{fullchain.pem, privkey.pem}
/root/.acme.sh/{dnsapi/dns_dynukey.sh, account.conf, your-domain.com_ecc/}
/etc/sing-box/config.json   /etc/caddy/Caddyfile   /var/www/html/index.html
/etc/systemd/system/{sing-box, caddy, ansgo-panel}.service
/etc/sysctl.d/99-proxy-tune.conf   /etc/security/limits.d/99-proxy.conf
/etc/ssh/sshd_config.d/10-hardening.conf   # 2026-06-22 SSH 加固 drop-in（见 §15）
/etc/ansgo-backup-ssh-harden-20260622-064243/   # SSH 加固前备份（sshd_config + sshd_config.d + 99-proxy-tune.conf）
/etc/ansgo-backup-update-v1.4.3-{ts}/   # v1.4.3 升级前的二进制+配置备份
```

---

## 11. 风险与回滚

| 风险 | 应对 |
|------|------|
| 证书签发失败 | acme.sh 详细日志；失败时保留现有自签证书继续服务，不影响运行；A 失败自动 B |
| 改配置导致服务起不来 | 每次改动前 `ansgo-admin backup` 到 `/etc/ansgo-backup-{ts}/`，`ansgo-admin restore` 一键回滚 |
| 面板 Go 二进制崩溃 | systemd `Restart=on-failure` 自动重启；`ansgo-admin` 兜底 |
| 改面板端口后失联 | SSH 进去 `ansgo-admin panel-port` 重置；或改 panel.json 后 restart。**注意 SSH 端口已改 25822（见 §15），走密钥登录** |
| SSH 加固后失联（密钥丢失/端口遗忘）| drop-in 备份在 `/etc/ansgo-backup-ssh-harden-20260622-064243/`；密钥登录走 `~/.ssh/bv_lax_ed25519` + 端口 25822（见 `.secrets.local`）；完全失联只能通过 Proxmox 宿主 LXC console 修复 |
| API Key 泄露 | 只存服务器 root 独占文件；可 `ansgo-admin` 旋转（重新填 Dynu 凭证）|
| IPv6 入站不可用 | 宿主防火墙限制，容器侧无解；仅用 IPv4 |
| 面板二进制更新不生效 | scp 覆盖运行中二进制会静默失败；必须 stop→rm→scp→md5 校验→start（见 §9 实战备注，v1.4.3 升级时已用 md5 `8113f1b2...` 校验通过）|
| 面板点击无反应 | 前端 JS 语法错误致整块脚本失效；改完 HTML 需重新编译上传，清浏览器缓存硬刷新 |
| 续期 reload 失败 | caddy `admin off` 无法 reload；`ansgo-cert-reload` 已改用 restart，续期闪断 1-2s |
| 第二组落地密钥错误 | sing-box `bad key length` 崩溃；面板对 2022-blake3 密钥做长度校验拒错；SSH 改正确后重启 |
| 服务卸载后 :443 打不开 | 旧版本会误停 caddy；v1.3.0+ caddy 始终运行（:443 伪装与代理解耦），不受服务卸载影响 |
| 服务安装后面板显示未生效 | ansgo-panel HTML 经 `//go:embed` 编译，改前端必须重新编译上传（stop→rm→scp→md5→start）+ 硬刷新 |
| Docker 容器 systemd 起不来 | 必须 `privileged: true` + `cgroup: host` + tmpfs `/run`；host 网络下端口与宿主冲突需先释放 |
| 容器改前端/二进制不生效 | 改 `web/index.html` 需重 build 镜像（`docker build -f deploy/Dockerfile.allinone .`）后 `docker compose up -d --build` |
| 移动端/白天主题显示异常 | v1.4.0 全面适配（侧边栏抽屉 / label 上置 / 网格单列 / 白天文字 `var(--txt)`）；v1.4.3 进一步修复 active 项白字（改蓝底白字）、`<code>` 显式着色、overlay 阴影双主题适配。若仍异常，硬刷新清浏览器缓存 |
| 卸载不干净（Docker 卷/镜像残留）| v1.4.2 `--uninstall` 用 `docker compose down -v` + `docker rm -f ansgo` 兑底 + 卷名模式匹配(`*_ansgo_(etc\|ssl\|caddy\|sb\|acme)`)兑底删卷；先删容器再删镜像避免 “image is being used” 错误 |
| install.sh 首行报 `x86_64: unbound variable` | 旧版 `ARCH_MAP=( [x86_64]=amd64 )` 缺 `declare -A`，bash 把它当索引数组，`x86_64` 在算术上下文求值 + `set -u` 触发报错；v1.4.1 改用 `case` 写法（零关联数组）。⚠️ `bash -n` 只查语法不执行，查不出此类运行时变量错误，改完务必**实际执行**开头几行验证 |
| 手动设置密钥含特殊字符破坏配置 | v1.4.x 的 `_setsecret` 用 `sed -i "s\|^...|...|"`，密钥含 `\|` 会破坏分隔符；v1.5.0 「密钥管理」页手动设置走 Go `setSecret()`（读全文→替换/追加→tmp→rename 原子写，绕开 sed），随机生成仍走 ansgo-admin（生成值无特殊字符，安全） |
| 手动证书路径错误致三服务全起不来 | manual 模式 `cert_fullchain/cert_privkey` 指向不存在或不可读文件会让 caddy/sing-box/panel 启动失败；面板「证书来源设置」和 install.sh 在写入前都做 `os.ReadFile`/`[ -f ]` 预校验拒绝错误路径；`certPaths()` 对 manual 但路径缺失的情况安全回退 `cert_dir`（不会崩）。若已在服务器上改坏，SSH 改 `/etc/ansgo/panel.json` 的 `cert_mode` 回 `acme` 后重启 |
| Docker manual 模式证书路径未挂载 | 容器内看不到 host 路径；entrypoint.sh 会记录 `ERROR: 证书文件不存在` 日志。部署前需在 `docker-compose.yml` 的 volumes 把证书目录挂进容器，或改用 cert_dir 卷内路径 |
| 卸载用 `curl ... \| bash -s -- --uninstall` 报 `curl: (23) Failure writing output` / 完全无输出 / 「已取消」 | **v1.5.3 根治**。三层历史问题演进：① v1.5.0 及之前「已取消」：管道下 `read` 读到 curl 输出 → `$a ≠ yes`；② v1.5.1 回归「完全无输出」：`exec 0</dev/tty` 切走 fd0；③ **v1.5.2 仍报 `curl: (23)`（本次用户实测）**：治对了 `read` 但没治根因——真正根因是 `curl \| bash` 下脚本中途任何 `exit`（do_uninstall/--landing/--help）让 bash 提前结束 → 管道读端关闭 → curl 写剩余字节收到 **SIGPIPE** → `(23)`。**v1.5.3 修复**：脚本最开头加 bootstrap，检测到管道/进程替换运行时先落地到临时文件再 `exec` 重跑，bash 从文件读，curl 能完整输出，二者解耦。已通过 PTY 端到端 + 8 项回归测试验证 |
| 面板「服务安装」页首次安装 SS/AnyTLS/Naive 必失败，提示 `生成配置失败: ...FileNotFoundError: [Errno 2] No such file or directory: '/etc/sing-box/config.json'` | **v1.5.4 根治**。根因：install.sh 装面板阶段（步骤 1-2）装了 sing-box 二进制和 systemd unit，但**从未创建 `/etc/sing-box/` 配置目录**；步骤 5/7 调 `ansgo-genconf all` 初始化占位配置时本应同样失败，但被 `ansgo-genconf all 2>&1 \| tail -3` **静默吞掉**让部署继续走完，问题被掩盖，直到用户在面板点「安装」时才冒泡给前端（`exec.Command(genconf).CombinedOutput()` 把 traceback 直接返回浏览器）。**修复双保险**：① `ansgo-genconf` 的 `gen_singbox()`/`gen_caddy()` 在 `open(...,"w")` 前 `os.makedirs(parent, exist_ok=True)` 治本（脚本自身不再依赖外部预建目录）② `install.sh` 步骤 2/8 预 `mkdir -p /etc/sing-box /etc/caddy /var/www/html` 防御（与 `Dockerfile.allinone:117` / `entrypoint.sh:31` 对齐，裸金属此前漏建）③ 步骤 5/7 的吞错管道加 `\|\| warn` 留诊断痕迹。已通过沙盒模拟目录缺失场景验证（sing-box/caddy/all 三模式 + 幂等性 + naive 安装场景全 exit=0；对照组复现原 traceback）。**线上修复方式**：已部署的服务器只需更新 `/usr/local/bin/ansgo-genconf`（重启面板），或 SSH 手动 `mkdir -p /etc/sing-box /etc/caddy` 即可立即恢复 |
| 端口写错导致服务起不来（如 SS 端口=443 与 caddy 冲突、端口超 65535、两个服务用同端口）| **v1.5.5 根治**。install.sh 此前对端口零校验（`--ss-port 99999` 或 `--naive-port 443` 都照单全收，部署后服务起不来才知道）。新增 `validate_inputs()` 函数：① 端口范围 1-65535 整数 ② 四个可配端口（SS/AnyTLS/Naive/Panel）互相不得重复 ③ 不得占用 caddy 固定端口（80 HTTP 跳转、443 伪装站）和 SSH 加固端口 25822。校验在参数解析后立即执行，失败直接 exit 1 列出所有错误，不进入实际部署。**仅对安装/部署场景生效**，`--uninstall`/`--landing`/`--purge` 跳过 |
| NaiveProxy 默认端口 443 与 caddy :443 伪装站冲突 | **v1.5.5 根治**。项目长期存在矛盾：install.sh/entrypoint.sh/ansgo-admin 默认 `NAIVE_PORT=443`，但 AGENTS.md §3 明确警告「NaiveProxy 端口不要用 443，默认 44333」（443 是 caddy 纯反代伪装站）。v1.5.5 统一为 44333（与 ansgo-genconf 默认一致），影响：install.sh L94、usage 文档、entrypoint.sh L50、ansgo-admin L81。**注意：已部署服务器的 panel.json 不受影响**（仅新部署默认值改变） |
| 手动指定 SS/AnyTLS/Naive/Panel 密码含特殊字符破坏配置 | **v1.5.5 防御**。install.sh 新增 `validate_inputs()` 对用户密码做格式校验：SS_KEY 必须 base64(16字节)（`openssl rand -base64 16` 生成）；AnyTLS UUID 须标准格式；NaiveProxy 用户名/密码不含冒号和空白（caddy basic_auth 限制）；面板密码 6-64 字符；URL 路径 `/xxxx/` 形式。secrets.env 用 heredoc 整体写入（不走 sed），避免 v1.4.x `_setsecret` 的 `\|` 分隔符陷阱 |
| 已装 nginx 的服务器上部署 ANS-GO（80/443 冲突）| **v1.5.6 解决**。新增 `--no-caddy` 参数：caddy 不监听 80/443，让现有 nginx/宝塔接管；ANS-GO 面板/SS/AnyTLS/Naive 按各自端口跑。`panel.json` 的 `caddy_enable=false` 让 `ansgo-genconf gen_caddy()` 跳过 `:443`/`:80` 块，caddy 即使启动也只听 naive 端口（如 44333），不冲突 nginx。交互式模式自动检测 80/443 占用并提示是否跳过 caddy。**典型场景**：`--no-caddy --cert-mode manual --cert-fullchain /www/server/panel/vhost/cert/x.com/fullchain.pem --cert-privkey .../privkey.pem --docker`（宝塔签发的证书直接喂给 ANS-GO，nginx 仍跑 443） |
| Docker 部署 `dl_or_exit: command not found` + 本地构建 `lstat .../deploy` 失败 | **v1.5.6 根治**。两个 bug：(a) `dl_or_exit` 函数定义在 `do_docker_deploy` 调用之后（bash 单遍解析，调用时函数未进表）→ 移到文件顶部日志函数之后；(b) `docker build -f deploy/Dockerfile.allinone` 用相对路径，docker 按 cwd（`/etc/ansgo-docker`）解析为不存在 → 改绝对路径 `-f /tmp/ansgo-build/deploy/Dockerfile.allinone`。两个 bug 让 Docker 一体化部署在 ghcr 拉取失败后走本地构建时彻底卡死 |
| Docker `--no-caddy` 模式部署后 caddy 仍占 443 / 面板打不开 | **v1.5.7 根治**。根因：v1.5.6 加了 `caddy_enable=false` 字段，但镜像里 `caddy.service` 已 `systemctl enable`，容器内 systemd 启动时无视 panel.json 仍拉起 caddy → caddy 占 443 又因无证书起不来 → 整体 systemd 状态混乱 → ansgo-panel 也受影响。修复：entrypoint.sh 在 NO_CADDY=1 时显式 `systemctl disable + mask caddy.service`，让 systemd 永远拉不起 caddy（naive 装上后面板手动 unmask + start） |
| Docker manual 证书模式 `ERROR: 证书文件不存在` | **v1.5.7 根治**。根因：宝塔/已有证书在宿主 `/www/server/...`，docker volume 只挂了 `/etc/ssl/ansgo`（命名卷），容器内看不到宿主证书路径。修复：install.sh 在 CERT_MODE=manual 时用 awk 在 docker-compose.yml 的 volumes 段自动追加 `- /宿主证书目录:/容器同路径:ro` bind mount（去重；awk 跨平台避免 macOS sed -i 与 GNU sed 语法差异） |
| Docker 部署 `docker compose pull` 失败但 `docker pull` 手动成功 | **v1.5.7 根治**。根因：服务器装的是 `docker-compose` v1（独立二进制）而非 `docker compose` v2（子命令），原代码 `docker compose pull 2>/dev/null` 吞掉「'compose' is not a docker command」错误 → 误判失败 → 走本地构建 → 本地构建又失败。修复：检测 `docker compose version` / `command -v docker-compose` 自动选用可用那个（COMPOSE 变量）；compose pull 失败用 `docker pull` 直拉兜底；不吞 stderr（用 `tail -N` 保留诊断） |
| 合并/拆分菜单页后操作回调不刷新数据 | **v1.5.11 抽象**。原先 `svcInstall/svc/regen/saveKey` 完成后硬编码 `setTimeout(loadInstall/loadSvc/loadKey, ...)` 回调，一旦操作按钮被搬到别的页面（v1.5.11 合三为一为「服务管理」），原回调刷新的还是旧独立页而不是当前页 → 用户点了「安装」但当前页状态不更新。修复：新增 `reloadCurrentTab()`（按 `.nav button.active` 的 `data-t` 派发对应 `loadXxx`），所有动作函数完成回调统一走它，避免后续合并/拆分页面时回调链断裂 |
| 落地服务配置后能起但实际无法用（naive-2/anytls-2 流量全部走 direct）| **v1.5.12 根治**。**根因1（致命）**：`ansgo-genconf` 落地 SS outbound 的路由规则错误引用了 `["anytls-in2", "naive-in2"]`，但 NaiveProxy 由 caddy 承载，sing-box 里**根本没有 naive-in2 这个 inbound tag**。新版 sing-box 配置校验失败/规则被丢弃 → 第二组流量全部走 direct（用户实测核心问题）。**修复**：路由规则只引用 `anytls-in2`，删除冗余 `action: route` 字段。**根因2（架构性）**：naive-2 在 caddy（独立进程），物理上无法转发给 sing-box 的 ss-out，永远走 direct 出口。**修复**：UI/文档全部明确告知"naive-2 走 direct"，避免错误预期。**根因3**：`group2Handler` 启用时要求用户先手动点「生成密钥」，用户直接启用 → 密钥缺失报错。**修复**：启用分支自动调 `ansgo-admin regen2` 生成密钥。**根因4**：`landingHandler` 改配置只 `restart sing-box`，但 sing-box 之前可能被 `disable` 起不来。**修复**：有启用服务时显式 `enable` + `restart`。⚠️ **教训：caddy（NaiveProxy）与 sing-box（SS/AnyTLS）是两个独立进程，跨进程路由不可能**——任何"naive-2 流量走 sing-box 的 ss-out"的设计在物理上都不可行 |
| 改面板端口/路径后失联 | SSH 进去 `ansgo-admin panel-port` / `panel-path` 重置（走密钥 + 25822 端口，见 §15）。**v1.5.12 起：部署默认面板端口也随机**，新部署务必记下 install.sh 输出的端口（或读 `/etc/ansgo/panel.json`） |
| **重新部署后面板"看不到"/功能回退** | **v1.5.13 根治**。根因：ghcr.io 镜像里固化的 ansgo-panel 二进制滞后于源码版本（历史遗留：v1.5.0 后多次 commit 改 Go 代码，但只 push 了源码，没同步 `docker buildx build --push` 重建镜像）。用户 `docker compose pull && up -d` 拉到的是旧镜像 → 二进制回退到 v1.5.0，所有新功能消失（用户感知为"昨天好的今天重装就不行"）。**诊断法**：`docker exec <容器> md5sum /usr/local/bin/ansgo-panel` vs `docker run --rm --entrypoint md5sum ghcr.io/jiasongji/ansgo:latest /usr/local/bin/ansgo-panel`——md5 一致说明镜像就是旧的，不一致说明已被 docker cp 临时修复但重建会丢。**修复**：v1.5.13 已重新构建多架构镜像并 push（`ghcr.io/jiasongji/ansgo:v1.5.12` + `:latest` 同步更新）。**预防**：发新版本必须同步重建镜像（命令见 §12），不能只 commit 源码。⚠️ 另：`main.go` 的 `version` 变量 v1.5.1~v1.5.12 连续 11 版硬编码为 `1.5.0`（日志误导排查），v1.5.13 已改为 `1.5.12`；根治方案应改用 `-ldflags "-X main.version=vX.Y.Z"` 构建时注入（待后续优化）|

---

## 12. 一键部署（推荐入口）

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
             --disguise-panel proxy:https://soft.xiaoz.org \
             --disguise-naive proxy:https://soft.xiaoz.org \
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
             --disguise-panel proxy:https://soft.xiaoz.org \
             --disguise-naive proxy:https://soft.xiaoz.org \
             --docker \
             --non-interactive
```

参数全集（完整说明见 GitHub README「参数全集」表）：`--domain`（必填）`--dynu-key`（或 `--dynu-client-id`+`--dynu-secret`，acme 模式必填）`--email` `--ss-port`(默认 23456) `--anytls-port`(8443) `--naive-port`(44333) `--panel-port`(15608) `--panel-user`(admin) `--disguise-panel` `--disguise-naive` `--cert-mode`(acme|manual，默认 acme) `--cert-fullchain`(manual 模式证书完整路径) `--cert-privkey`(manual 模式私钥完整路径) `--docker` `--no-caddy`(v1.5.6+，不部署 caddy 的 80/443，让 nginx 等接管) `--non-interactive` `--force-bin`。
>
> **⭐ v1.5.5 新增：密码/密钥参数化（全部可选，留空则随机生成）**：
> - `--ss-password KEY`：Shadowsocks 密钥，须 base64(16字节)，生成命令 `openssl rand -base64 16`
> - `--anytls-password PASS`：AnyTLS 密码（非空即可）
> - `--anytls-uuid UUID`：AnyTLS 用户 UUID，标准格式如 `a1b2c3d4-e5f6-7890-abcd-ef1234567890`
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

---

## 13. 后续流程

1. ✅ **自测审计（已完成）**：`ansgo-admin status` + 面板全功能 + 服务安装/卸载 + 三协议从中国大陆公网连通 + IP 锁定 + 证书真实性（Let's Encrypt）均验证通过
2. ✅ **GitHub 建项（已完成）**：公开仓库 `ANS-GO`，含 AGENTS.md + `deploy/`（脚本 + 面板源码）+ `install.sh`（一键部署），**不含** `.secrets.local`/`.build`
3. ✅ **服务器部署（已完成 + 持续迭代）**：生产服务器已部署并通过 `https://bv-la.giize.com:15608/BV_LA/` 访问。面板版本迭代走 §9「stop→rm→scp→md5→start」流程，已升级至 v1.4.3（左侧可折叠侧边栏 + 白天模式字体修复）
4. **客户端实测（可选）**：用真实客户端（Clash.Meta / sing-box / naive 客户端）测各协议连通与分流

---

## 14. 约束与原则（给执行 AI）

- 默认使用简体中文
- 优先最小修改，完成后自行验证并汇报
- 敏感凭证绝不写入会进 git 的文件（AGENTS.md / 脚本 / 教程）
- 每个高风险操作（改配置、重启服务）前自动备份
- 不擅自加防火墙 drop 规则（避免锁死 SSH）
- 所有生成密钥用 `openssl rand`，base64 密钥用标准 base64（不是 urlsafe）
- Go 交叉编译用 `CGO_ENABLED=0`（纯静态，无 libc 依赖）
- 执行前先读本文件 §0-§15：§12（一键部署）为推荐入口，§9（部署架构）说明面板优先设计，§15（SSH 加固）为部署后动作，每步报告进度

---

## 15. SSH 加固（2026-06-22 部署后动作）

> 触发：安全检查发现 `87.251.64.149` 持续暴破 root/admin SSH 字典（`lastb` 显示密集失败）。原配置 `Port 22 + PermitRootLogin yes + PasswordAuthentication yes` 是首要攻击面。
>
> ⚠️ **本章节的加固是"部署后动作"，不在 `install.sh` 流程内。** 新部署仍是默认 22 端口，需手动复刻下述步骤。后续可考虑把 `--ssh-port` 参数并入 install.sh。

### 加固范围（已确认决策）

| 项 | 决策 | 说明 |
|----|------|------|
| 认证方式 | ✅ 公钥登录 + 禁密码 | ED25519，密钥不入 git（见 `.secrets.local`）|
| root 登录 | ✅ 保持直登 | `PermitRootLogin prohibit-password`（仅公钥，禁密码）|
| 防火墙 | ❌ 不动 | nftables 规则集仍空、policy=accept（LXC 安全由宿主负责）|
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

**`ssh.socket` 已 mask**：Debian 12 用 socket activation，`ssh.socket` 配置 `ListenStream=22`——若不 mask，重启后 22 端口会被重新拉起，绕过 drop-in。已 `systemctl mask ssh.socket`（软链到 `/dev/null`），保证 22 永久关闭。

**sysctl 追加**（`/etc/sysctl.d/99-proxy-tune.conf` 末尾）：
```
net.ipv4.conf.all.send_redirects = 0
net.ipv4.conf.default.send_redirects = 0
net.ipv4.conf.all.log_martians = 1
net.ipv4.conf.default.log_martians = 1
```
> `kernel.kptr_restrict` / `kernel.kexec_load_disabled` 因 LXC 宿主只读已移除（容器内 `permission denied`，留着只污染 `sysctl -p` 日志）。

### 下次登录方式（重要）
```bash
ssh ansgo-bv-la                                    # 已配 ~/.ssh/config 别名
# 或
ssh -i ~/.ssh/bv_lax_ed25519 -p 25822 root@<服务器IP>
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
cp /etc/ansgo-backup-ssh-harden-20260622-064243/99-proxy-tune.conf /etc/sysctl.d/
sysctl -p /etc/sysctl.d/99-proxy-tune.conf

# 若密钥也丢了（完全失联）：只能通过 Proxmox 宿主 LXC console 进入容器修复
```

### 安全流程参考（本次实施顺序，可复刻）
1. 本地 `ssh-keygen -t ed25519 -f ~/.ssh/bv_lax_ed25519 -N ""`
2. 上传公钥到 `/root/.ssh/authorized_keys`（**密码仍开**，保命）
3. **新终端验证密钥 + 22 登录成功** → 才能进入下一步（关键防失联）
4. 备份 → 写 drop-in → `sshd -t` 语法校验
5. **当前 22 密码会话不退** → `systemctl restart sshd` → 新终端密钥 + 25822 验证
6. `systemctl mask ssh.socket`（防 socket activation 复活 22）
7. sysctl 加固追加 + `sysctl -p`
8. 负面测试：密码 + 25822 应被拒；22 端口应 `Connection refused`
9. 确认 ansgo 三服务仍 active（加固零影响）

### 备份
- `/etc/ansgo-backup-ssh-harden-20260622-064243/`：含 `sshd_config` + `sshd_config.d/` + `99-proxy-tune.conf`（加固前原状）
