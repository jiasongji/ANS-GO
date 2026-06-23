# ANS-GO

> 在低配 VPS（LXC 或 KVM）上部署 **NaiveProxy + AnyTLS + Shadowsocks** 三协议代理 + **Go Web 管理面板**，共享一张证书（acme.sh 自动签发 **或** 手动指定已有证书）。支持**裸金属脚本**与 **Docker 一体化**两种部署形态，可审计、可回滚、可离线管理（SSH 兜底）。

![status](https://img.shields.io/badge/status-已部署验证-brightgreen) ![version](https://img.shields.io/badge/version-v1.5.12-blue) ![license](https://img.shields.io/badge/license-MIT-blue) ![stack](https://img.shields.io/badge/stack-Go%20%7C%20bash%20%7C%20sing--box%20%7C%20caddy-orange)

> 📌 **所有命令默认以 `root` 用户执行**（非 root 用户请加 `sudo`）。一键命令均可直接复制粘贴。

---

## 📚 目录

- [🚀 快速开始（30 秒看完）](#-快速开始30-秒看完)
- [🆕 全新安装](#-全新安装)
  - [方式 1：交互式安装（推荐首次使用）](#方式-1交互式安装推荐首次使用)
  - [方式 2：裸金属一键安装（LXC / 低配 256MB 推荐）](#方式-2裸金属一键安装lxc--低配-256mb-推荐)
  - [方式 3：Docker 一体化安装（KVM / 资源充裕推荐）⭐](#方式-3docker-一体化安装kvm--资源充裕推荐-)
  - [方式 4：落地机安装独立 SS（中转→落地架构）](#方式-4落地机安装独立-ss中转落地架构)
- [⚙️ 参数全集](#️-参数全集)
- [🔄 更新升级](#-更新升级)
- [🗑 卸载 / 彻底卸载 / 清理](#-卸载--彻底卸载--清理)
- [📖 部署后使用指南](#-部署后使用指南)
- [🛠 故障排查](#-故障排查)
- [✨ 特性](#-特性)
- [🏗 架构](#-架构)
- [📁 仓库结构](#-仓库结构)
- [🔒 安全声明](#-安全声明)

---

## 🚀 快速开始（30 秒看完）

```bash
# ① 部署中转机（任选一种）
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)              # 交互式（推荐）
# 或非交互式（DNS 托管在 Dynu，自动 acme 签发）
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com --dynu-key <KEY> --email you@example.com --non-interactive
# 或 Docker 一体化（加 --docker）
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com --dynu-key <KEY> --email you@example.com --docker --non-interactive

# ② 登录面板 →「服务管理」页按需安装代理服务（默认全部未启动）
#    https://your-domain.com:<随机端口>/<随机URL路径>/

# ③ 更新到最新版
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)    # 选「重新部署」会保留现有配置

# ④ 卸载（保留配置）/ 彻底清理（全删）
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh | bash -s -- --uninstall
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh | bash -s -- --uninstall --purge
```

> 所有资源取自本仓库 GitHub（脚本/源码走 raw，二进制走 Releases，镜像走 ghcr.io），不依赖第三方 CDN。需 root，支持 Debian 11/12 / Ubuntu（含 LXC、KVM）。

---

## 🆕 全新安装

### 方式 1：交互式安装（推荐首次使用）

无参数运行，显示**主菜单**让用户选择操作；选「安装」后逐项交互输入。

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)
```

**主菜单选项**：
1. **安装 / 部署管理面板**（默认）
2. **卸载**（移除服务/容器/二进制，保留配置/卷，可重装不丢参数）
3. **彻底卸载**（删除配置/密钥/证书/卷/镜像/调优，**不可恢复**）
4. **部署落地服务器**（独立 ss-server，供中转机落地服务接入）

选「安装」后，依次提示输入：**域名 → 证书来源（acme/manual）→ Dynu 凭证（仅 acme 模式）→ 各端口（v1.5.12 起默认随机）→ 面板用户名 → 伪装站点**。

> 💡 交互式会**显示每个参数的默认值**（端口默认随机，回车采纳），方便不熟悉参数的用户。

---

### 方式 2：裸金属一键安装（LXC / 低配 256MB 推荐）

systemd 直管 caddy + sing-box + 面板三个独立进程，内存占用最小。

**最简非交互式部署**（DNS 托管在 Dynu，端口/密钥全自动随机生成）：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --dynu-key <DYNU_API_KEY> \
             --email you@example.com \
             --non-interactive
```

**指定端口 + 伪装站点**（其余参数省略用默认）：

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
             --disguise-panel proxy:https://example.com \
             --disguise-naive proxy:https://example.com \
             --non-interactive
```

> ⚠️ **v1.5.12 起：所有端口默认随机生成**（10000-65535，自动避开 80/443/25822/已占用）。未通过 `--ss-port` 等参数指定时，部署完成横幅会**用 ╔═══╗ 边框醒目提示**所有随机端口，请立即记录。

<details>
<summary>🔧 全参数 + 自定义密码示例（v1.5.5+，点击展开）</summary>

需要**预先确定全部凭证**（如自动化部署、配置同步到客户端）时，可指定每个服务的端口和密码。所有密码参数都经过 `validate_inputs()` 校验：

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

**校验规则**（失败即拒）：
- 端口：1-65535 整数 + 互相不重复 + 不占用 caddy/SSH 固定端口（80 / 443 / 25822）
- SS 密钥：base64 解码后恰好 16 字节（`openssl rand -base64 16` 生成的即是）
- AnyTLS UUID：标准格式 `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`
- Naive 用户名/密码：不含冒号和空白（caddy basic_auth 限制）
- 面板密码：6-64 字符
- URL 路径：`/xxxx/` 形式（首尾斜杠，中间仅字母数字 `_-`）

</details>

<details>
<summary>🔐 手动证书模式（v1.5.0+，已有 nginx/宝塔/Caddy 签发的证书）</summary>

服务器上已有证书（其他 ACME 客户端、Caddy 自动签发、商业证书等），可直接引用，跳过 Dynu 凭证与 acme.sh 签发：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --cert-mode manual \
             --cert-fullchain /etc/letsencrypt/live/your-domain.com/fullchain.pem \
             --cert-privkey  /etc/letsencrypt/live/your-domain.com/privkey.pem \
             --email you@example.com \
             --non-interactive
```

| 模式 | 适用场景 | 续期 |
|------|----------|------|
| `acme`（默认）| DNS 托管在 Dynu，全自动签发 | acme.sh 60 天自动续期 + 三服务自动 reload |
| `manual` | 已有证书 / DNS 不在 Dynu / 商业证书 | 用户自行管理，替换文件后面板点「重新加载证书」 |

</details>

<details>
<summary>🌐 nginx 共存模式（v1.5.6+，服务器已装 nginx/宝塔）</summary>

服务器上已装 nginx（或宝塔 BT panel）占用 80/443？加 `--no-caddy` 让 ANS-GO 不部署 caddy 的 80/443 站点，nginx 继续接管 web，ANS-GO 面板/代理按各自端口跑。配合 `--cert-mode manual` 直接用宝塔签发的证书：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --cert-mode manual \
             --cert-fullchain /www/server/panel/vhost/cert/your-domain.com/fullchain.pem \
             --cert-privkey  /www/server/panel/vhost/cert/your-domain.com/privkey.pem \
             --anytls-port 21002 \
             --naive-port 21008 \
             --panel-port 10568 \
             --panel-user ad_admin \
             --no-caddy \
             --non-interactive
```

**行为差异**：
- caddy 不启动 :443/:80（裸金属下 caddy 二进制+unit 仍装好，面板装 naive 时可手动 enable）
- `ansgo-genconf gen_caddy()` 因 `caddy_enable=false` 跳过 `:443`/`:80` 块，只生成 naive 站点
- 端口校验：`--no-caddy` 时 80/443 不再视为保留端口
- 交互式模式：检测到 80/443 占用时主动提示「是否跳过 caddy？」

**nginx 反代到面板（可选）**：nginx 配置加 `proxy_pass https://127.0.0.1:<panel_port>;` 即可。

</details>

---

### 方式 3：Docker 一体化安装（KVM / 资源充裕推荐）⭐

单容器（`ghcr.io/jiasongji/ansgo`，all-in-one：sing-box + caddy + 面板 + systemd）内跑全套，**面板代码 0 改动**。仅加 `--docker`，其余参数与裸金属完全一致：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --dynu-key <DYNU_API_KEY> \
             --email you@example.com \
             --docker \
             --non-interactive
```

> Docker 模式自动装 docker、生成 `/etc/ansgo-docker/ansgo.env`（凭证，600 权限）+ `docker-compose.yml`、`docker compose pull` 拉取公开镜像并 `docker compose up -d`。
>
> **v1.5.6+ 已发布公开镜像 `ghcr.io/jiasongji/ansgo:latest`（amd64+arm64 双架构，312MB）**，正常网络下 `docker compose pull` 直接成功，无需本地构建。需 `privileged` + host 网络（脚本已自动配置）。

**Docker 管理常用命令**：

```bash
cd /etc/ansgo-docker && docker compose logs -f ansgo    # 查日志/签证书进度
docker exec ansgo ansgo-admin status                    # 服务状态
docker exec ansgo ansgo-admin info                      # 节点连接参数
docker exec ansgo ansgo-admin panel-pass                # 重置面板密码
docker restart ansgo                                    # 重启容器（含证书续期后）
docker compose pull && docker compose up -d             # 升级镜像
```

<details>
<summary>🔧 Docker + 手动证书模式（v1.5.0+）</summary>

manual 模式需把证书所在目录挂载进容器，否则容器内看不到证书文件。脚本会自动在 `docker-compose.yml` 的 volumes 段追加 bind mount（去重），无需手动编辑：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --cert-mode manual \
             --cert-fullchain /etc/letsencrypt/live/your-domain.com/fullchain.pem \
             --cert-privkey  /etc/letsencrypt/live/your-domain.com/privkey.pem \
             --docker \
             --non-interactive
```

> 续期后操作：`docker restart ansgo`（重跑 entrypoint 自动同步宿主证书到卷）。

</details>

---

### 方式 4：落地机安装独立 SS（中转→落地架构）

在**另一台服务器**部署独立 ss-server，供中转机**落地服务**（AnyTLS-2）经 SS 接入，隐藏中转 IP：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --landing --port 8388 --non-interactive
```

落地机 SS 信息（host / port / method / password）随后填入中转机面板**「落地服务」页**下半部分的「远端 SS 落地服务器」。

> ⚠️ **架构约束**（v1.5.12 明确告知）：只有 **AnyTLS-2** 经 SS 落地（隐藏中转 IP）；**NaiveProxy-2 走 direct**（caddy 与 sing-box 是两个独立进程，跨进程路由不可行）。

---

## ⚙️ 参数全集

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--domain` | —（必填）| 你的域名（A/AAAA 已解析到本机）|
| `--dynu-key` | — | Dynu API Key（路径 A，推荐）|
| `--dynu-client-id` | — | Dynu OAuth Client ID（路径 B，需配 `--dynu-secret`）|
| `--dynu-secret` | — | Dynu OAuth Secret（路径 B）|
| `--email` | `admin@<域名>` | ACME 注册邮箱 |
| `--ss-port` | **随机** ⭐ v1.5.12 | Shadowsocks 端口（10000-65535）|
| `--anytls-port` | **随机** ⭐ v1.5.12 | AnyTLS 端口 |
| `--naive-port` | **随机** ⭐ v1.5.12 | NaiveProxy 端口（**勿用 443**，443 是 caddy 伪装站）|
| `--panel-port` | **随机** ⭐ v1.5.12 | 面板端口 |
| `--panel-user` | `admin` | 面板管理员用户名 |
| `--panel-password` | 随机 | 面板密码（自定义须 6-64 字符）⭐ v1.5.5 |
| `--panel-url-path` | 随机 `/xxxxxxxx/` | 面板 URL 路径（形如 `/my-path/`）⭐ v1.5.5 |
| `--ss-password` | 随机 | Shadowsocks 密钥（须 base64(16字节)，`openssl rand -base64 16` 生成）⭐ v1.5.5 |
| `--anytls-password` | 随机 | AnyTLS 密码（非空即可）⭐ v1.5.5 |
| `--anytls-uuid` | 随机 | AnyTLS 用户 UUID（标准格式）⭐ v1.5.5 |
| `--naive-user` | 随机 | NaiveProxy 用户名（不含冒号和空白）⭐ v1.5.5 |
| `--naive-password` | 随机 | NaiveProxy 密码（不含冒号和空白）⭐ v1.5.5 |
| `--disguise-panel` | `proxy:https://example.com` | `:443` 直访伪装站点（`proxy:<URL>` / `page`）|
| `--disguise-naive` | `proxy:https://example.com` | Naive 端口伪装站点（同上格式）|
| `--cert-mode` | `acme` | 证书来源：`acme` / `manual`（手动指定已有证书路径）⭐ v1.5.0 |
| `--cert-fullchain` | — | manual 模式：证书文件完整绝对路径 ⭐ v1.5.0 |
| `--cert-privkey` | — | manual 模式：私钥文件完整绝对路径 ⭐ v1.5.0 |
| `--docker` | 关 | Docker 一体化形态（KVM 用；否则裸金属）|
| `--no-caddy` | 关 | 不部署 caddy 的 80/443（让已装 nginx/宝塔接管）⭐ v1.5.6 |
| `--non-interactive` | 关 | 非交互模式，缺必填项报错退出（CI / 自动化）|
| `--force-bin` | 关 | 强制重装 sing-box/caddy 二进制（已装则跳过）|
| `--landing` | — | 子命令：落地机部署独立 SS |
| `--uninstall` | — | 子命令：卸载 |
| `--purge` | — | 与 `--uninstall` 同用：彻底删除一切 |

> 💡 **Dynu 凭证双保险**：DNS 托管在 Dynu，证书走 DNS-01（绕开 80 端口依赖）。路径 A（API Key，推荐）失败时自动降级到路径 B（OAuth2）。

---

## 🔄 更新升级

ANS-GO 升级有三种场景，按需选用：

### 场景 1：重新部署（最简单，配置保留）

重新跑 install.sh，**已存在的 `panel.json` / `secrets.env` 会被保留**（脚本检测到则跳过生成），仅刷新脚本/二进制：

```bash
# 交互式
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)

# 非交互式（参数与首次部署一致）
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com --dynu-key <KEY> --email you@example.com --non-interactive
```

> 此方式会拉取最新 `install.sh`、最新二进制（若已装则跳过，加 `--force-bin` 强制重装）。

### 场景 2：升级单个组件（面板 / sing-box / caddy）

通过 `ansgo-admin update` 升级单个二进制：

```bash
# 升级 sing-box（自动从 SagerNet 官方 release 拉取最新版）
ansgo-admin update sing-box

# 升级面板（先在 mac 上交叉编译，scp 上传到服务器，再执行）
# 1. 本地编译（macOS）：
cd deploy/panel && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ansgo-panel .
# 2. scp 上传（必须 stop→rm→scp→md5→start，否则 scp 覆盖运行中二进制会静默失败）
systemctl stop ansgo-panel
rm /usr/local/bin/ansgo-panel
scp ansgo-panel root@<server>:/usr/local/bin/ansgo-panel
ssh root@<server> 'md5sum /usr/local/bin/ansgo-panel'  # 校验 md5
ssh root@<server> 'systemctl start ansgo-panel'

# caddy-naive 分支需 xcaddy 现场编译，推荐重跑 install.sh --force-bin
```

### 场景 3：升级 Docker 镜像

```bash
cd /etc/ansgo-docker
docker compose pull                              # 拉取最新镜像
docker compose up -d                             # 重建容器（配置在卷里，不丢失）
docker image prune -f                            # 清理旧镜像（可选）
```

### 升级前必备份

```bash
ansgo-admin backup                               # 裸金属：备份到 /etc/ansgo-backup-{ts}/
docker exec ansgo ansgo-admin backup             # Docker：容器内备份
```

---

## 🗑 卸载 / 彻底卸载 / 清理

`install.sh --uninstall` 自动检测部署模式（Docker / 裸金属）。两个级别：

### 默认卸载（保留配置/卷，可重装不丢参数）

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh | bash -s -- --uninstall
```

### 彻底卸载（删除一切，不可恢复）

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh | bash -s -- --uninstall --purge
```

### 交互式卸载

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)
# 主菜单选 ② 卸载 或 ③ 彻底卸载
```

<details>
<summary>📋 两种卸载级别详细对比</summary>

| 项 | 默认卸载（`--uninstall`）| 彻底卸载（`--uninstall --purge`）|
|----|------------------------|-------------------------------|
| 服务/容器 | ✅ 停止并移除 | ✅ 停止并移除 |
| 二进制 / systemd unit | ✅ 删除 | ✅ 删除 |
| 配置/密钥/证书（`/etc/ansgo` `/etc/ssl/ansgo`）| ⬜ 保留 | ✅ 删除 |
| Docker 数据卷 / 镜像 | ⬜ 保留 | ✅ 删除 |
| acme.sh 状态 / 备份 / 调优 | ⬜ 保留 | ✅ 删除 |
| docker 本体 | ⬜ 保留（可能被其他服务使用）| ⬜ 保留 |

**Docker 分支额外清理**（`--purge`）：
- `docker compose down -v` 删除卷
- `docker rm -f ansgo` 兜底删容器
- 卷名模式匹配（`*_ansgo_(etc|ssl|caddy|sb|acme)`）兜底删残留卷
- `docker image rm ghcr.io/jiasongji/ansgo:latest` 删镜像
- 先删容器再删镜像，避免 "image is being used" 错误

**裸金属分支额外清理**（`--purge`）：
- `/etc/ansgo` `/etc/ssl/ansgo` `/etc/sing-box` `/etc/caddy` `/var/www/html`
- `/root/.acme.sh` `/etc/ansgo-deploy` `/etc/ansgo-backup-*`
- `/etc/sysctl.d/99-ansgo-tune.conf` `/etc/security/limits.d/99-ansgo.conf`
- sing-box / caddy 二进制

</details>

> 卸载前会有二次确认（输入 `yes`）。`--purge` 不可恢复，请确保已备份所需配置。
>
> ⚠️ **`curl|bash` 卸载历史问题已在 v1.5.3 根治**（旧版本 `curl: (23) Failure writing output`）。根因是 `curl|bash` 下脚本中途 exit 触发 SIGPIPE；v1.5.3 用 bootstrap 落地机制解耦，所有 `curl|bash` 形式（卸载/部署/落地）均正常工作。

---

## 📖 部署后使用指南

### 访问面板

部署完成横幅会打印：

```
╔═══════════════════════════════════════════════════╗
║  ⚠️  本次部署端口均为随机生成，请立即记录！       ║
║  Web 面板:   https://your-domain.com:<端口>/<路径> ║
║  用户名:     admin                                ║
║  密码(仅此一次): <随机密码>                       ║
║  代理服务端口：ss=<端口> anytls=<端口> naive=<端口>║
╚═══════════════════════════════════════════════════╝
```

**忘记密码？**
```bash
ansgo-admin panel-pass                  # 裸金属：打印新密码（仅一次）
docker exec ansgo ansgo-admin panel-pass  # Docker
```

**忘记 URL 路径或端口？**
```bash
ansgo-admin info                        # 裸金属：打印完整面板地址
docker exec ansgo ansgo-admin info      # Docker
# 或直接查配置：
cat /etc/ansgo/panel.json | python3 -c 'import json,sys;d=json.load(sys.stdin);print(f"https://{d["domain"]}:{d["panel_port"]}{d["url_path"]}")'
```

### 启用代理服务

> **代理服务默认不启动**——登录面板后到「**服务管理**」页按需开启 Shadowsocks / AnyTLS / NaiveProxy。

每张服务卡支持：① 安装/卸载 ② 改端口 ③ 改密钥（手动输入或 🎲 随机）④ 启停 ⑤ **🔍 检测**（v1.5.12：systemd 状态 + 端口监听 + TCP 握手三合一诊断）。

### 节点信息（v1.5.12 重构）

「节点信息」页**只显示已启用的服务**（未启用不显示，避免空 URI 误导）。每张卡按"连接地址/端口/加密方式/密码/用户名/SNI"分行展示，每行独立「📋 复制」按钮 + URI + 客户端二维码。

### 启用落地服务（中转→落地架构）

「**落地服务**」页（v1.5.12 起由原「第二组服务」+「出口落地」合并）：
1. **上半部分**：勾选启用 → 填 anytls-2 / naive-2 端口 → 保存（密钥未生成时**自动生成**，无需手动点按钮）
2. **下半部分**：填远端 SS 落地服务器（host/port/method/password）→ 保存
3. 生成的密钥可在「服务管理」页底部 AnyTLS-2 / NaiveProxy-2 卡片查看/修改

> ⚠️ **架构约束**：只有 AnyTLS-2 经 SS 落地（隐藏中转 IP）；NaiveProxy-2 走 direct（caddy/sing-box 跨进程不可路由）。

### 离线管理（面板全挂也能用）

`ansgo-admin` bash 脚本零依赖，常用命令：

```bash
ansgo-admin status                # 服务状态 / 端口 / 资源 / 证书
ansgo-admin info                  # 节点连接参数 + URI + 面板信息
ansgo-admin restart [ss|anytls|naive|panel|all]
ansgo-admin stop    [服务]
ansgo-admin logs    [服务]
ansgo-admin regen   [ss|anytls|naive]   # 重置密钥
ansgo-admin regen2                        # 重新生成落地服务密钥
ansgo-admin cert status | renew          # 证书状态 / 手动续期
ansgo-admin panel-pass                    # 重置面板密码
ansgo-admin panel-path                    # 重置面板 URL 路径
ansgo-admin panel-port [PORT]             # 重置面板端口
ansgo-admin firewall [list|open PORT|close PORT]
ansgo-admin backup                        # 备份所有配置
ansgo-admin restore [备份目录]             # 回滚配置
ansgo-admin update  [sing-box|caddy|panel]
```

Docker 用户加前缀：`docker exec ansgo ansgo-admin <命令>`。

---

## 🛠 故障排查

| 症状 | 排查步骤 |
|------|---------|
| 面板打不开 | `ansgo-admin status` 看 ansgo-panel 是否 active；检查防火墙 `ansgo-admin firewall list`；端口是否被占用 `ss -tln \| grep <端口>` |
| 证书签发失败 | `cat /root/ansgo-cert-issue.log` 看详细日志；Dynu 凭证是否正确；DNS 是否已解析到本机 |
| 代理服务连不上 | 面板「服务管理」点「🔍 检测」三合一诊断；`ansgo-admin logs <服务>` 看日志；密钥是否一致 |
| 落地服务 anytls-2 走 direct（未走 SS 落地）| 确认「落地服务」页**远端 SS** 已启用且 host/port/password 正确；密钥长度校验（2022-blake3-aes-128 需 base64(16字节)）|
| 改面板端口后失联 | SSH 进去 `ansgo-admin panel-port <新端口>` 重置；或改 `/etc/ansgo/panel.json` 后 `systemctl restart ansgo-panel` |
| 改配置后服务起不来 | `ansgo-admin restore /etc/ansgo-backup-<最近的>/` 回滚；再 `ansgo-admin restart all` |
| Docker 容器反复重启 | `docker logs ansgo` 看启动日志；检查是否 `privileged: true` + `cgroup: host` |
| `curl\|bash` 报 `curl: (23)` | v1.5.3+ 已根治；旧版本升级请先 `wget -O install.sh <URL> && bash install.sh` |

更多故障场景与历史 bug 修复记录见 [AGENTS.md §11 风险与回滚](AGENTS.md)。

---

## ✨ 特性

### v1.5.12 最新特性 ⭐

- **节点信息页重构**：未启用服务不显示；每条信息分行 + 独立复制按钮
- **落地服务合并页**：「第二组服务」+「出口落地」合并为单页
- **端口全部随机生成**：10000-65535 自动避让，部署横幅醒目提示
- **落地服务启用自动生成密钥**：无需手动点「生成密钥」按钮
- **服务健康检测**：systemd + 端口监听 + TCP 握手三合一诊断
- **【Bug修复】** 落地服务 4 个致命 bug 根治（路由规则引用不存在的 inbound / naive-2 架构约束 / 启用流程 / sing-box enable）

### 核心特性

- **面板优先架构**：install.sh 只装面板 + 证书 + :443 伪装站；代理服务面板内按需启用（不装不占端口）
- **三协议代理**：NaiveProxy（caddy forwardproxy-naive 分支）/ AnyTLS / Shadowsocks-2022（sing-box 双 inbound）
- **Web 管理面板**：Go 单二进制（~15-20MB 运行内存），中文 UI，管全部协议参数 / 端口 / 证书 / 服务安装卸载 + 客户端二维码
- **一张证书共享**：acme.sh 自动签发 或 手动指定已有证书；caddy / sing-box / 面板三服务共享
- **域名双伪装**：`:443` 纯反代伪装站 + naive 端口独立伪装；两个伪装站点均可在 Web 后台独立配置
- **落地服务 + 链式出站**：AnyTLS-2 经 SS 走另一台落地服务器（隐藏中转 IP）
- **完全解耦**：caddy / sing-box / 面板三个独立进程、端口、systemd unit，**改协议端口永远不会断面板**
- **离线兜底**：`ansgo-admin` bash 脚本零依赖，面板全挂也能 SSH 管理一切
- **nginx 共存模式**：`--no-caddy` 让 caddy 不监听 80/443，已装 nginx/宝塔的服务器可共存
- **暗黑/白天双主题 + 移动端自适应**：左侧可折叠侧边栏（桌面折叠 + 移动端抽屉，localStorage 记忆）
- **安全**：管理员密码 bcrypt、按 IP 登录锁定、8 小时会话、随机 URL 路径、全程 TLS

---

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
   caddy :443      sing-box :<随机>         面板(Go) :<随机>
   NaiveProxy      AnyTLS + Shadowsocks    Web 管理面板
   + 域名伪装反代   + 落地 anytls-2         (含密钥/证书/检测)
      ▲                 ▲                     ▲
      └─────── ansgo-admin (bash) ───────────┘   SSH 离线兜底
```

> v1.5.12 起：所有服务端口**部署时随机生成**，caddy :443/:80 仍固定（域名伪装基础设施）。

---

## 📁 仓库结构

```
.
├── install.sh              # ⭐ 一键部署（交互式 + 带参数 + 卸载）
├── AGENTS.md               # ⭐ 唯一事实来源（方案/架构/部署顺序/约束/历史 changelog）
├── README.md               # ⭐ 本文档（用户教程入口）
├── deploy/                 # 全部部署产物
│   ├── README.md           #    手动部署 / 复现指南
│   ├── Dockerfile.allinone #    ⭐ all-in-one 镜像（sing-box+caddy+面板+systemd，推 ghcr.io/jiasongji/ansgo）
│   ├── Dockerfile          #    面板单镜像（兼容用，推 ghcr.io/jiasongji/ansgo-panel）
│   ├── docker/             #    docker-compose.yml + entrypoint.sh
│   ├── dns_dynukey.sh      #    acme.sh Dynu DNS-01 钩子（API Key）
│   ├── ansgo-cert-issue.sh #    装 acme.sh + 签发证书（A/B 双保险）
│   ├── ansgo-cert-reload   #    续期/替换后 reload 三服务
│   ├── ansgo-genconf       #    从 config + secrets 重新生成服务配置
│   ├── ansgo-admin         #    离线管理脚本
│   ├── ansgo-panel.service #    面板 systemd unit
│   ├── systemd/            #    sing-box / caddy 的 systemd unit
│   └── panel/              #    Go 面板源码（main/handlers/crypto.go + web/）
└── .secrets.local          # 🔒 敏感凭证（已 gitignore，不入库）
```

## 📖 文档

| 文档 | 内容 |
|------|------|
| [README.md](README.md) | **用户教程入口**：安装 / 交互 / 带参数 / 更新 / 卸载 / 故障排查 |
| [AGENTS.md](AGENTS.md) | **唯一事实来源**：项目目标、架构、端口、证书方案、三协议配置、面板设计、部署顺序、风险回滚、约束原则、版本历史 changelog |
| [deploy/README.md](deploy/README.md) | 手动部署 / 复现指南：文件清单、各步骤命令、配置模板、实战注意事项 |

---

## 🔒 安全声明

- 本仓库**不含任何密钥、密码、域名凭证、服务器 IP**（敏感值已全部用占位符替代）。真实值在服务器 `/etc/ansgo/`、`/etc/ssl/ansgo/`、`/root/.acme.sh/`，本地镜像在 `.secrets.local`（已 gitignore）。
- 部署后请自行验证：客户端连通、面板功能走查、IP 锁定机制、证书真实性。
- 推荐配套 SSH 加固：公钥登录 + 禁密码 + 改非标端口（参考 [AGENTS.md §15](AGENTS.md)）。

## 📄 License

MIT — 见 [LICENSE](LICENSE)。
