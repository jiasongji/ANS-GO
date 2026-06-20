# ANS-GO 代理服务器方案 (AGENTS.md)

> 本文件是项目的**唯一事实来源**。新窗口执行、GitHub 教程撰写、服务器部署均以本文件为准。
> 敏感凭证见 `.secrets.local`（已 gitignore，不入库）。
>
> **部署状态：✅ 已部署并端到端验证。** 可复现产物在 `deploy/`，一键部署见 §12。
>
> **当前版本：v1.3.0**（面板优先架构 + 服务按需安装 + 双主题）。版本历史见 GitHub Releases。

---

## 0. 项目目标

在一台低配 LXC VPS 上，部署 **Web 管理面板** + 可选的代理服务，要求：

- **面板优先架构**：install.sh 只装面板 + 证书，代理服务在 Web 后台「服务安装」页按需启用
- **三协议代理**（按需）：NaiveProxy + AnyTLS + Shadowsocks（2022）
- **第二组服务 + 链式出站**：可选额外 anytls-2 + naive-2，出口经 SS 走另一台落地服务器
- **真实域名证书**：`your-domain.com`（Let's Encrypt），面板与各服务共享
- **Web 管理面板**：中文、暗黑/白天双主题、可管全部协议参数 + 证书 + 服务安装/卸载 + 自身配置
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
| SSH | `root@<服务器IP>`（密码见 `.secrets.local`）|

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
| Web 面板 (ansgo-panel) | `15608` TCP | HTTPS | **面板必需** | ✅（改后重启）|
| NaiveProxy (caddy) | `44333` TCP | HTTPS forward proxy | 按需安装 | ✅ |
| AnyTLS (sing-box) | `21111` TCP | TLS | 按需安装 | ✅ |
| Shadowsocks (sing-box) | `33899` TCP | SS2022 | 按需安装 | ✅ |
| 第二组 anytls-2 / naive-2 | `21112` / `44334` | TLS / HTTPS | 按需（走 SS 落地）| ✅ |
| caddy HTTP（重定向）| `80` TCP | HTTP | 固定 | ❌ |
| SSH | `22` TCP | SSH | 固定 | ❌ |

> :443 是**域名直访伪装站**（纯反代，不提供代理），随面板一起启动。NaiveProxy 用独立端口（默认 44333），不要用 443。

防火墙（nftables）policy=accept 全放行；部署时仅确保新端口可达，不加 drop 规则（避免锁死 SSH，LXC 安全由宿主负责）。

---

## 4. 证书方案（双保险）

### 签发工具
**acme.sh**（curl 安装，~200KB，不走 apt）。不用 caddy 自带 ACME——因为 caddy 内部证书存储路径深、版本化命名，sing-box 引用困难且续期后需手动 reload。acme.sh 可签发到固定路径并通过 `--reloadcmd` 续期后自动重启三服务。

### 验证方式
**DNS-01**（绕开 80 端口依赖，可签泛域名）。

### Dynu 凭证双保险（A 默认，A 失败降级 B）
两套都已实测可用（HTTP 200，能读写 zone <Dynu_zone_id>）：

| 路径 | 凭证 | 机制 | 用法 |
|------|------|------|------|
| **A（默认）** | API Key | `Api-Key` 请求头 | 自定义钩子 `dns_dynukey.sh`（~60行，直接调 Dynu REST API 加删 TXT 记录）|
| **B（降级）** | Client ID + Secret | OAuth2 `client_credentials` 换 bearer token | acme.sh 官方 `dns_dynu` 插件 |

**降级逻辑**：部署时先尝试 A 签发；若 A 返回非 0 退出码，自动切换到 B 重试。两套凭证均存 `/root/.acme.sh/` 下，root 独占可读。

> acme.sh 官方插件用 OAuth2（要 `Dynu_ClientId` + `Dynu_Secret`），而 API Key 是另一套凭证。这是 Dynu 平台同时提供的两种鉴权，互不冲突。

### 证书落点与续期
```
/etc/ssl/ansgo/fullchain.pem   # 证书链
/etc/ssl/ansgo/privkey.pem     # 私钥
```
- 续期周期：acme.sh 默认 60 天（实际由 ARI 窗口驱动，约 60 天）
- 续期 reload：统一走 `ansgo-cert-reload` 脚本（`--install-cert --reloadcmd "/usr/local/bin/ansgo-cert-reload"`）。该脚本按需重载——只有**当前配置引用了 `/etc/ssl/ansgo/` 证书**的服务才重载，签名发阶段是 no-op，切换证书后才会生效
- ⚠️ **caddy 用 restart 不用 reload**：Caddyfile 设了 `admin off`，无 admin API 通道，`systemctl reload caddy` 会失败。续期/改配置统一 `systemctl restart caddy`（naive 闪断 1-2s 可接受）。sing-box（ss+anytls）和 ansgo-panel 用 restart
- `--keylength ec-256`（ECDSA，体积小、握手快）

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

### 5.4 第二组服务（可选，走 SS 落地）
- **默认关闭**，面板「第二组服务」页勾选启用才部署。启动后额外起：
  - `anytls-in2`（sing-box，独立端口，默认 21112）
  - `naive-2`（caddy forward_proxy，独立端口，默认 44334）+ 独立 basic_auth 凭证
- **链式出站**：第二组的出口流量经 `ss-out`（sing-box shadowsocks outbound）转发到另一台落地服务器的 ss-server（中转→落地架构）。第一组仍走 direct。
- 生成路由规则：`{ inbound: [anytls-in2, naive-in2], outbound: ss-out }`
- 密钥：`ansgo-admin regen2` 生成（ANYTLS2_* / NAIVE2_*，存 secrets.env）；面板「密钥管理」或「第二组服务」页可触发

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

### 6.4 功能模块（中文 UI，支持暗黑/白天双主题切换，localStorage 记忆）
1. **登录页**（含「忘记密码？」命令提示）
2. **仪表盘**：各服务状态灯 + 开关 / 端口 / 内存 / TCP 连接数 / 负载 / 运行时长 / 证书倒计时
3. **节点信息**：各启用服务连接参数 + URI 一键复制 + 客户端二维码（启用第二组时额外显示 anytls-2/naive-2）
4. **服务控制**：start / stop / restart（二次确认）
5. **服务安装**：⭐ Shadowsocks / AnyTLS / NaiveProxy 独立安装/卸载（代理服务面板内按需启用）
6. **端口管理**：各服务端口 + 面板自身端口均可改
7. **密钥管理**：重新生成某协议密钥（二次确认）+ 生成第二组密钥
8. **第二组服务**：开关 / 端口 / Naive-2 伪装（启用后额外 anytls-2 + naive-2，走 SS 落地）
9. **出口落地**：落地服务器 SS 配置（host/port/method/password + 开关），含密钥长度校验
10. **证书管理**：到期时间 / 手动续期按钮 / 上次续期结果
11. **面板设置**：URL 路径 / 会话 / 管理员账号密码 / 面板端口 / 锁定阈值 / 两个伪装站点
12. **日志查看**：tail 最近 N 行

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
ansgo-admin uninstall           # 卸载（保留配置备份）
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
/etc/sysctl.d/99-ansgo-tune.conf   # stage1 网络调优
/etc/security/limits.d/99-ansgo.conf  # stage1 fd 上限
```

---

---

## 9. 部署架构：面板优先 + 代理服务面板内按需安装

> v1.3.0 架构变更：install.sh **只装面板 + 证书**，代理服务（NaiveProxy/AnyTLS/Shadowsocks）改为**登录面板后在「服务安装」页按需启用**。

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
4. **「第二组服务」页**：可选启用额外 anytls-2 + naive-2（走 SS 落地）
5. **「出口落地」页**：配置落地服务器 SS（第二组出口）

### 为什么这样设计
- **面板与代理解耦**：面板和 :443 伪装站随安装立即可用；代理服务按需开启，不装不占端口
- **caddy 始终运行**：:443 伪装站 + :80 跳转是域名基础设施，不随代理服务卸载而停（sing-box 无 inbound 时才会停）
- **服务开关持久化**：`panel.json` 的 `svc_ss_enabled` / `svc_anytls_enabled` / `svc_naive_enabled` 字段控制 genconf 生成对应配置

### 实战备注（历次部署沉淀）
- **scp 覆盖运行中二进制会失败**（sftp报 `dest open Failure`），且后续 restart 只重启旧文件。正确流程：`systemctl stop` → `rm` → `scp` → `md5sum` 对比 → `systemctl start`。md5 是判断"是否真更新"的唯一手段。
- **caddy reload 必失败**（Caddyfile 设 `admin off`），改配置用 restart。
- **前端 SPA"点击无反应"=JS 语法错误**：整块 `<script>` 不解析→所有函数未定义→静默。诊断：`node --check` 查语法。
- **长任务用后台守护**：SSH 长连接易超时，`nohup ... > log 2>&1 &`。
- **改配置前必备份**：`ansgo-admin backup` → `/etc/ansgo-backup-{ts}/`。

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

### 当前端口（默认值，可面板内改）
```
caddy :443 伪装站:  443（始终运行，域名直访）
NaiveProxy(caddy):  44333
AnyTLS(sing-box):   21111
Shadowsocks:        33899
第二组(可选):       anytls2=21112 / naive2=44334（走 SS 落地）
面板(ansgo-panel):  15608
caddy HTTP(重定向): 80
SSH:                22
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
/usr/local/bin/{sing-box, caddy, ansgo-admin, ansgo-genconf, ansgo-panel, ansgo-cert-reload}
/etc/ansgo/{config.json, sessions.db}
/etc/ssl/ansgo/{fullchain.pem, privkey.pem}
/root/.acme.sh/{dnsapi/dns_dynukey.sh, account.conf, your-domain.com_ecc/}
/etc/sing-box/config.json   /etc/caddy/Caddyfile   /var/www/html/index.html
/etc/systemd/system/{sing-box, caddy, ansgo-panel}.service
/etc/ansgo/secrets.env  /etc/sysctl.d/99-ansgo-tune.conf  /etc/security/limits.d/99-ansgo.conf
```

---

## 11. 风险与回滚

| 风险 | 应对 |
|------|------|
| 证书签发失败 | acme.sh 详细日志；失败时保留现有自签证书继续服务，不影响运行；A 失败自动 B |
| 改配置导致服务起不来 | 每次改动前 `ansgo-admin backup` 到 `/etc/ansgo-backup-{ts}/`，`ansgo-admin restore` 一键回滚 |
| 面板 Go 二进制崩溃 | systemd `Restart=on-failure` 自动重启；`ansgo-admin` 兜底 |
| 改面板端口后失联 | SSH 进去 `ansgo-admin panel-port` 重置；或改 config.json 后 restart |
| API Key 泄露 | 只存服务器 root 独占文件；可 `ansgo-admin` 旋转（重新填 Dynu 凭证）|
| IPv6 入站不可用 | 宿主防火墙限制，容器侧无解；仅用 IPv4 |
| 面板二进制更新不生效 | scp 覆盖运行中二进制会静默失败；必须 stop→rm→scp→md5 校验→start（见 §9 实战备注）|
| 面板点击无反应 | 前端 JS 语法错误致整块脚本失效；改完 HTML 需重新编译上传，清浏览器缓存硬刷新 |
| 续期 reload 失败 | caddy `admin off` 无法 reload；`ansgo-cert-reload` 已改用 restart，续期闪断 1-2s |
| 第二组落地密钥错误 | sing-box `bad key length` 崩溃；面板对 2022-blake3 密钥做长度校验拒错；SSH 改正确后重启 |
| 服务卸载后 :443 打不开 | 旧版本会误停 caddy；v1.3.0+ caddy 始终运行（:443 伪装与代理解耦），不受服务卸载影响 |
| 服务安装后面板显示未生效 | ansgo-panel HTML 经 `//go:embed` 编译，改前端必须重新编译上传（stop→rm→scp→md5→start）+ 硬刷新 |

---

## 12. 一键部署（推荐入口）

仓库根目录提供 `install.sh`，支持**交互式**与**带参数一键**两种模式，所有资源取自本仓库 GitHub（脚本/面板源码走 raw，二进制走 Releases）。

> v1.3.0 起 install.sh **只装面板 + 证书 + :443 伪装站**，代理服务登录面板后到「服务安装」页按需启用。

### 交互式
```bash
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)
# 依次交互输入：域名、Dynu API Key（或 OAuth Client ID+Secret）、各端口、面板用户名等
```

### 带参数一键
```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --dynu-key <API_KEY> \
             --email you@example.com \
             --non-interactive
```
常用参数：`--domain` `--dynu-key`（或 `--dynu-client-id`+`--dynu-secret`）`--email` `--ss-port` `--anytls-port` `--naive-port` `--panel-port` `--panel-user` `--disguise-panel` `--disguise-naive` `--non-interactive` `--docker`（用 ghcr.io 镜像跑面板）。

落地服务器专用：`bash install.sh --landing [--port 8388]`（在该机部署独立 ss-server，供中转机第二组接入）。

### 资源来源（全部自有 GitHub）
- 脚本/源码：`raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/...`
- 二进制（sing-box / caddy-naive / ansgo-panel / acme.sh 快照）：`github.com/jiasongji/ANS-GO/releases/download/vX.Y.Z/...`
- Docker 面板镜像：`ghcr.io/jiasongji/ansgo-panel:latest`（多阶段自构建，见 `deploy/Dockerfile`）

> LXC 低配（256MB）推荐裸金属；资源充裕或需可移植时可加 `--docker`。

---

## 13. 后续流程

1. ✅ **自测审计（已完成）**：`ansgo-admin status` + 面板全功能 + 服务安装/卸载 + 三协议从中国大陆公网连通 + IP 锁定 + 证书真实性（Let's Encrypt）均验证通过
2. ✅ **GitHub 建项（已完成）**：公开仓库 `ANS-GO`，含 AGENTS.md + `deploy/`（脚本 + 面板源码）+ `install.sh`（一键部署），**不含** `.secrets.local`/`.build`
3. **服务器复现（待验证）**：用 `install.sh` 在干净服务器一键部署并测试
4. **客户端实测**：用真实客户端（Clash.Meta / sing-box / naive 客户端）测各协议连通与分流

---

## 14. 约束与原则（给执行 AI）

- 默认使用简体中文
- 优先最小修改，完成后自行验证并汇报
- 敏感凭证绝不写入会进 git 的文件（AGENTS.md / 脚本 / 教程）
- 每个高风险操作（改配置、重启服务）前自动备份
- 不擅自加防火墙 drop 规则（避免锁死 SSH）
- 所有生成密钥用 `openssl rand`，base64 密钥用标准 base64（不是 urlsafe）
- Go 交叉编译用 `CGO_ENABLED=0`（纯静态，无 libc 依赖）
- 执行前先读本文件 §0-§14：§12（一键部署）为推荐入口，§9（部署架构）说明面板优先设计，每步报告进度
