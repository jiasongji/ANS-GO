# ANS-GO

> 在低配 VPS（LXC 或 KVM）上部署 **NaiveProxy + AnyTLS + Shadowsocks** 三协议代理 + **Go Web 管理面板**，共享一张证书（acme.sh 自动签发 **或** 手动指定已有证书）。支持**裸金属脚本**与 **Docker 一体化**两种部署形态，可审计、可回滚、可离线管理（SSH 兜底）。

![status](https://img.shields.io/badge/status-已部署验证-brightgreen) ![license](https://img.shields.io/badge/license-MIT-blue) ![stack](https://img.shields.io/badge/stack-Go%20%7C%20bash%20%7C%20sing--box%20%7C%20caddy-orange)

---

## 🚀 一键部署

> 所有资源取自本仓库 GitHub（脚本/源码走 raw，二进制走 Releases，镜像走 ghcr.io），不依赖第三方 CDN。需 root，支持 Debian 11/12 / Ubuntu（含 LXC、KVM）。

三个入口：**中转机部署**（裸金属 / Docker）、**落地机部署**（独立 SS）、**交互式**。

### 方式一：交互式（主菜单选择，推荐首次使用）

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)
```

> 先显示主菜单选择操作：① 安装/部署（默认）② 卸载（保留配置/卷）③ 彻底卸载 ④ 部署落地服务器。选安装后依次输入：域名、Dynu 凭证（路径A API Key 或 路径B OAuth）、各端口、面板用户名、伪装站点。

### 方式二：中转机 — 裸金属一键（LXC / 低配 256MB 推荐）

systemd 直管 caddy + sing-box + 面板三个独立进程，内存占用最小。以下为**全参数示例**（可按需删减，省略项用默认值）：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --dynu-key <DYNU_API_KEY> \
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

### 方式三：中转机 — Docker 一体化一键（KVM / 资源充裕推荐）⭐

单容器（`ghcr.io/jiasongji/ansgo`，all-in-one：sing-box + caddy + 面板 + systemd）内跑全套，面板代码 0 改动。**仅加 `--docker`，其余参数与裸金属完全一致**：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --dynu-key <DYNU_API_KEY> \
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

> Docker 模式自动装 docker、生成 `/etc/ansgo-docker/ansgo.env`（凭证，600 权限）+ `docker-compose.yml`、拉取/本地构建镜像并 `docker compose up -d`。需 `privileged` + host 网络（脚本已自动配置）。管理：`cd /etc/ansgo-docker && docker compose logs -f ansgo`、`docker exec ansgo ansgo-admin status`。

### 方式四：落地机 — 独立 Shadowsocks（第二组出口用）

在**另一台服务器**部署独立 ss-server，供中转机第二组服务（anytls-2 / naive-2）经 SS 接入（中转→落地架构）：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --landing --port 8388 --non-interactive
```

落地机 SS 信息（host / port / method / password）随后填入中转机面板「出口落地」页。

### 凭证：Dynu 双保险（路径 A 默认 / B 降级）

DNS 托管在 Dynu，证书走 DNS-01（绕开 80 端口依赖）。两套凭证任选其一：

| 路径 | 参数 | 机制 |
|------|------|------|
| **A（推荐）** | `--dynu-key <API_KEY>` | API Key 请求头，自定义钩子 |
| **B（降级）** | `--dynu-client-id <ID> --dynu-secret <SECRET>` | OAuth2 client_credentials |

路径 B 示例：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --dynu-client-id <CLIENT_ID> \
             --dynu-secret <SECRET> \
             --email you@example.com \
             --non-interactive
```

部署时自动先尝试 A，A 失败降级 B。

### 证书来源：acme 自动 / 手动指定（二选一）⭐ v1.5.0+

除默认的 acme.sh 自动签发外，v1.5.0 起支持**手动指定已有证书**的完整路径（例如你已经用其他 ACME 客户端、Caddy、或商业证书），跳过 Dynu 凭证与 acme.sh 签发。两种模式由 `--cert-mode` 控制，部署后也可在面板「证书管理」页随时切换。

| 模式 | 参数 | 适用场景 | 续期 |
|------|------|----------|------|
| **`acme`（默认）** | `--dynu-key`（或 OAuth B 套）| DNS 托管在 Dynu，全自动签发 | acme.sh 60 天自动续期 + 三服务自动 reload |
| **`manual`** | `--cert-mode manual --cert-fullchain <PATH> --cert-privkey <PATH>` | 已有证书（其他 ACME 客户端 / Caddy / 商业证书），或 DNS 不在 Dynu | 用户自行管理，替换文件后面板点「重新加载证书」 |

**手动证书模式示例（裸金属）**——无需 Dynu 凭证，直接指定服务器上已有的证书与私钥完整路径：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --cert-mode manual \
             --cert-fullchain /etc/letsencrypt/live/your-domain.com/fullchain.pem \
             --cert-privkey /etc/letsencrypt/live/your-domain.com/privkey.pem \
             --email you@example.com \
             --ss-port 33899 \
             --anytls-port 21111 \
             --naive-port 44333 \
             --panel-port 15608 \
             --non-interactive
```

**手动证书模式 + Docker 一体化**——同样只需加 `--docker`，其余参数一致；但**必须把证书所在目录挂载进容器**（在 `deploy/docker-compose.yml` 的 `volumes` 添加 `- /etc/letsencrypt:/etc/letsencrypt:ro`），否则容器内看不到证书文件：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --cert-mode manual \
             --cert-fullchain /etc/letsencrypt/live/your-domain.com/fullchain.pem \
             --cert-privkey /etc/letsencrypt/live/your-domain.com/privkey.pem \
             --email you@example.com \
             --docker \
             --non-interactive
```

部署后编辑 `/etc/ansgo-docker/docker-compose.yml` 把证书目录挂载进容器，再 `docker compose up -d`：

> manual 模式下 caddy / sing-box / 面板三个服务都直接引用你指定的绝对路径（不复制到 `/etc/ssl/ansgo/`）。替换证书后登录面板「证书管理」页点「🔄 重新加载证书」即可让三服务重新读取（含面板自身重启）。

### 参数全集

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--domain` | —（必填） | 你的域名（A/AAAA 已解析到本机，DNS 托管在 Dynu）|
| `--dynu-key` | — | Dynu API Key（路径 A，推荐）|
| `--dynu-client-id` | — | Dynu OAuth Client ID（路径 B，需配 `--dynu-secret`）|
| `--dynu-secret` | — | Dynu OAuth Secret（路径 B）|
| `--email` | admin@<域名> | ACME 注册邮箱 |
| `--ss-port` | `23456` | Shadowsocks 端口 |
| `--anytls-port` | `8443` | AnyTLS 端口 |
| `--naive-port` | `443` | NaiveProxy 端口 |
| `--panel-port` | `15608` | 面板端口 |
| `--panel-user` | `admin` | 面板管理员用户名 |
| `--disguise-panel` | `proxy:https://soft.xiaoz.org` | `:443` 直访伪装站点（`proxy:<URL>` 反代 / `page` 静态页）|
| `--disguise-naive` | `proxy:https://soft.xiaoz.org` | Naive 端口伪装站点（同上格式）|
| `--cert-mode` | `acme` | 证书来源：`acme`（Dynu DNS-01 自动签发，需 Dynu 凭证）/ `manual`（手动指定已有证书路径，跳过 Dynu）⭐ v1.5.0 |
| `--cert-fullchain` | — | manual 模式：证书文件完整绝对路径（如 `/etc/letsencrypt/live/x.com/fullchain.pem`）⭐ v1.5.0 |
| `--cert-privkey` | — | manual 模式：私钥文件完整绝对路径 ⭐ v1.5.0 |
| `--docker` | 关 | Docker 一体化形态（KVM 用；否则裸金属）|
| `--non-interactive` | 关 | 非交互模式，缺必填项报错退出（CI / 自动化）|
| `--force-bin` | 关 | 强制重装 sing-box/caddy 二进制（已装则跳过）|
| `--landing` | — | 子命令：落地机部署独立 SS（仅 `--port` / `--non-interactive` 有效）|

### 部署完成后

脚本会打印：**面板访问地址 + 随机 URL 路径 + 一次性管理员密码**。

> **代理服务默认不启动**——登录面板后到「服务安装」页按需开启 Shadowsocks / AnyTLS / NaiveProxy。架构上 install.sh 只装**面板 + 证书 + `:443` 伪装站**，三个代理服务改为面板内按需安装/卸载。
>
> 忘记密码？SSH 执行 `ansgo-admin panel-pass`（裸金属）或 `docker exec ansgo ansgo-admin panel-pass`（Docker）。

### 卸载

一键卸载，自动检测部署模式（Docker / 裸金属）。**默认保留配置/卷**（可重装不丢参数），加 `--purge` 彻底删除一切。

**默认卸载**（移除服务/容器/二进制，保留配置/卷）：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh | bash -s -- --uninstall
```

**彻底卸载**（删除配置/密钥/证书/数据卷/镜像/系统调优，**不可恢复**）：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh | bash -s -- --uninstall --purge
```

> 也可直接运行 `bash <(curl ...)` 进入交互式主菜单选择「卸载」或「彻底卸载」。
>
> ⚠️ **curl\|bash 卸载历史问题已在 v1.5.3 根治**（旧版本 `curl: (23) Failure writing output`）。根因是 curl\|bash 下脚本中途 exit 触发 SIGPIPE；v1.5.3 用 bootstrap 落地机制解耦，所有 curl\|bash 形式（卸载/部署/落地）均正常工作。

<details>
<summary>📋 两种卸载级别对比</summary>

| 项 | 默认卸载（`--uninstall`）| 彻底卸载（`--uninstall --purge`）|
|----|------------------------|-------------------------------|
| 服务/容器 | ✅ 停止并移除 | ✅ 停止并移除 |
| 二进制 / systemd unit | ✅ 删除 | ✅ 删除 |
| 配置/密钥/证书（`/etc/ansgo` `/etc/ssl/ansgo`）| ⬜ 保留 | ✅ 删除 |
| Docker 数据卷 / 镜像 | ⬜ 保留 | ✅ 删除 |
| acme.sh 状态 / 备份 / 调优 | ⬜ 保留 | ✅ 删除 |
| docker 本体 | ⬜ 保留（可能被其他服务使用）| ⬜ 保留 |

</details>

> 卸载前会有二次确认（输入 `yes`）。`--purge` 不可恢复，请确保已备份所需配置。

---

## ✨ 特性

- **面板优先架构**：install.sh 只装面板 + 证书，代理服务在 Web 后台「服务安装」页按需启用（不装不占端口）
- **curl\|bash 全面兼容** ⭐ v1.5.3：所有一键命令（交互式主菜单 / 全参数部署 / 卸载 / 彻底卸载 / 落地）均可在 `curl | bash` 管道形式下正常工作（v1.5.2 及之前卸载会报 `curl: (23)`，已根治）
- **交互式主菜单** ⭐ v1.5.3：无参数运行时显示操作选择（安装/卸载/彻底卸载/落地），不再只能部署
- **三协议**：NaiveProxy（caddy forwardproxy-naive 分支）/ AnyTLS / Shadowsocks-2022（sing-box 双 inbound）
- **Web 管理面板**：Go 单二进制（运行内存 ~12MB），中文 UI，管全部协议参数 / 端口 / 证书 / 自身配置 + 服务安装卸载，含客户端二维码
- **密钥可手动设置** ⭐ v1.5.0：「密钥管理」页支持手动输入 SS / AnyTLS / Naive + 第二组任意自定义密钥（SS2022 自动校验长度），与随机生成并存
- **证书 acme / 手动二选一** ⭐ v1.5.0：默认 acme.sh + Dynu DNS-01 自动签发 ECDSA；也可 `--cert-mode manual` 直接引用已有证书路径（其他 ACME 客户端 / Caddy / 商业证书），面板内可随时切换来源
- **一张证书共享**：无论 acme 还是 manual，caddy / sing-box / 面板三服务都共享同一证书，续期/替换后一次重载三服务一起生效
- **域名双伪装**：`:443` 纯反代伪装站（域名直访命中，不提供代理）+ naive 端口独立伪装；两个伪装站点均可在 Web 后台独立配置（默认反代 `soft.xiaoz.org`）
- **第二组服务 + 链式出站**：可选启用额外的 anytls-2 + naive-2，出口经 Shadowsocks 走另一台落地服务器（中转→落地）
- **暗黑/白天双主题 + 移动端自适应**：顶栏一键切换主题（localStorage 记忆）；手机端导航横向滚动 / 表单 label 上置 / 网格自适应单列；白天模式文字对比度已修正
- **完全解耦**：caddy / sing-box / 面板是三个独立进程、端口、systemd unit，**改协议端口永远不会断面板**
- **离线兜底**：`ansgo-admin` bash 脚本零依赖，面板全挂也能 SSH 管理一切（含密码/路径/端口重置、备份回滚）
- **安全**：管理员密码 bcrypt、按 IP 登录锁定、8 小时会话、随机 URL 路径、全程 TLS

## 🏗 架构

```
   证书来源（二选一，--cert-mode）
   ├─ acme（默认）: acme.sh + Dynu DNS-01  路径A: API Key / B: OAuth
   │      │ 自动签发 → /etc/ssl/ansgo/{fullchain,privkey}.pem
   │      │ 60 天自动续期 + 三服务 reload
   └─ manual（v1.5.0+）: 直接引用用户已有证书
          │ --cert-fullchain / --cert-privkey 指定绝对路径
          │ 替换文件后面板点「重新加载证书」
                        │
                        ▼  一张证书三服务共享
      ┌─────────────────┼─────────────────────────────┐
      ▼                 ▼                     ▼
 caddy :443       sing-box :8443        面板(Go) :15608
 NaiveProxy       AnyTLS + Shadowsocks   Web 管理面板
 + 域名伪装反代                           (含密钥/证书来源切换)
      ▲                 ▲                     ▲
      └─────── ansgo-admin (bash) ───────────┘   SSH 离线兜底
```

## 📁 仓库结构

```
.
├── install.sh           # ⭐ 一键部署（交互式 + 带参数）
├── AGENTS.md            # ⭐ 唯一事实来源（方案 / 架构 / 部署顺序 / 约束）
├── deploy/              # 全部部署产物
│   ├── README.md        #    手动部署 / 复现指南
│   ├── Dockerfile.allinone #  ⭐ all-in-one 镜像（sing-box+caddy+面板+systemd，推 ghcr.io/jiasongji/ansgo）
│   ├── Dockerfile       #    面板单镜像（兼容用，推 ghcr.io/jiasongji/ansgo-panel）
│   ├── docker/          #    docker-compose.yml + entrypoint.sh（容器初始化 + systemd）
│   ├── dns_dynukey.sh   #    acme.sh Dynu DNS-01 钩子（API Key）
│   ├── ansgo-cert-issue.sh  # 步骤 1-3：装 acme.sh + 签发证书（A/B 双保险）
│   ├── ansgo-cert-reload   #    续期/替换后 reload 三服务（v1.5.0+ 不再依赖固定路径）
│   ├── ansgo-genconf       #    从 config + secrets 重新生成服务配置（v1.5.0+ 支持手动证书路径）
│   ├── ansgo-admin         #    离线管理脚本
│   ├── ansgo-panel.service #    面板 systemd unit
│   └── panel/              #    Go 面板源码（main/handlers/crypto.go + web/）
└── .secrets.local       # 🔒 敏感凭证（已 gitignore，不入库）
```

## 📖 文档

| 文档 | 内容 |
|------|------|
| [AGENTS.md](AGENTS.md) | **唯一事实来源**：项目目标、架构、端口、证书方案、三协议配置、面板设计、部署顺序、一键部署、风险回滚、约束原则 |
| [deploy/README.md](deploy/README.md) | 手动部署 / 复现指南：文件清单、各步骤命令、配置模板、实战注意事项 |

## 🔒 安全声明

- 本仓库**不含任何密钥、密码、域名凭证、服务器 IP**（敏感值已全部用占位符替代）。真实值在服务器 `/etc/ansgo/`、`/etc/ssl/ansgo/`、`/root/.acme.sh/`，本地镜像在 `.secrets.local`（已 gitignore）。
- 部署后请自行验证：客户端连通、面板功能走查、IP 锁定机制、证书真实性。

## 📄 License

MIT — 见 [LICENSE](LICENSE)。
