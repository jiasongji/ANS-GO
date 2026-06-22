# ANS-GO 部署文件

本目录是 ANS-GO 代理方案的**全部部署产物**（脚本 + 面板源码 + Docker）。

> **推荐用根目录 [`../install.sh`](../install.sh) 一键部署**（交互式 / 带参数），资源全部取自本仓库 GitHub。
> 本文档面向手动部署 / 复现 / 二次开发。完整方案见上级 [`AGENTS.md`](../AGENTS.md)（唯一事实来源）。

## 文件清单

| 文件 | 安装到 | 作用 |
|------|--------|------|
| `../install.sh` | 服务器执行 | ⭐ 一键部署（交互式 + 带参数） |
| `dns_dynukey.sh` | `/root/.acme.sh/dnsapi/` | acme.sh Dynu DNS-01 钩子（API Key） |
| `ansgo-cert-issue.sh` | 一次性执行 | 装 acme.sh + 签发 ECDSA 证书（A 默认 / B 降级），支持 vendored acme.sh |
| `ansgo-cert-reload` | `/usr/local/bin/` | 证书续期/替换后 reload 三服务（v1.5.0+ 改为「配置存在即 reload」，不再 grep 固定路径，兼容手动证书） |
| `ansgo-genconf` | `/usr/local/bin/` | 从 `panel.json` + `secrets.env` 重新生成 sing-box / caddy 配置（v1.5.0+ 证书路径按 `cert_mode` 解析：manual 用绝对路径，acme 回退 `cert_dir`） |
| `ansgo-admin` | `/usr/local/bin/` | 离线管理脚本（面板全挂也能 SSH 管理一切） |
| `ansgo-panel.service` | `/etc/systemd/system/` | 面板 systemd unit |
| `systemd/{sing-box,caddy}.service` | `/etc/systemd/system/` | 代理服务 unit |
| `Dockerfile.allinone` / `docker-compose.yml` / `docker/entrypoint.sh` | — | ⭐ all-in-one 一体化镜像（sing-box+caddy+面板+systemd，→ `ghcr.io/jiasongji/ansgo`） |
| `Dockerfile` | — | 面板单镜像构建（仅面板，兼容用 → `ghcr.io/jiasongji/ansgo-panel`） |
| `panel/` | 本地交叉编译 | Go 面板源码 |

## 一键部署（推荐）

**交互式**（逐项确认，推荐首次使用）：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)
```

**带参数一键**（CI / 自动化）：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com --dynu-key <KEY> --email you@example.com --non-interactive
```

## Docker 一体化部署（KVM / 资源充裕推荐）

`install.sh --docker` 自动装 docker、生成 `ansgo.env`（凭证，600 权限）+ `docker-compose.yml`、拉 `ghcr.io/jiasongji/ansgo:latest`（失败则 clone 仓库本地构建），`docker compose up -d` 拉起单容器：

- 容器内 **systemd 作 PID 1**，复用裸金属全部 unit / 脚本 / 面板代码（systemctl / journalctl / ansgo-admin 原生可用，**面板 0 改动**）
- **host 网络 + privileged + cgroup:host**，代理端口 / 面板端口直连宿主
- 配置 / 密钥 / 证书 / acme 状态持久化到 docker volume，容器重建不丢失
- 首次启动容器内自动：生成 `panel.json` + `secrets.env` → 设置管理员密码（`PANEL_PASS`）→ 自签占位证书 → 后台 acme.sh DNS-01 签发真实证书（覆盖自签）

**部署命令**：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com --dynu-key <KEY> --docker --non-interactive
```

**管理命令**：

```bash
# 查日志 / 签证书进度
cd /etc/ansgo-docker && docker compose logs -f ansgo
# 服务状态
docker exec ansgo ansgo-admin status
# 节点连接参数
docker exec ansgo ansgo-admin info
```

> 手动构建镜像：`docker build -t ghcr.io/jiasongji/ansgo:latest -f deploy/Dockerfile.allinone .`（国内需配 `HTTPS_PROXY`）

## 卸载（v1.4.2+）

`install.sh --uninstall` 自动检测部署模式（Docker / 裸金属），两级清理。**默认保留配置/卷**（可重装不丢参数），加 `--purge` 彻底删除。

**默认卸载**（移除服务/容器/二进制/unit，保留配置/卷）：

```bash
bash install.sh --uninstall
```

**彻底卸载**（删除配置/密钥/证书/acme/备份/卷/镜像/调优，**不可恢复**）：

```bash
bash install.sh --uninstall --purge
```

- **Docker 分支**：`docker compose down -v` + `docker rm -f ansgo` 兑底 + 卷名模式匹配(`*_ansgo_*`)兑底删卷，先删容器再删镜像避免占用错误
- **裸金属分支**：停 systemd 服务 + 删二进制/unit；`--purge` 额外删 `/etc/ansgo` `/etc/ssl/ansgo` `/root/.acme.sh` + sysctl/limits 调优
- docker 本体保留（可能被其他服务使用）；卸载前二次确认

## 手动部署（裸金属，对应 AGENTS.md §9）

### 前置（第一阶段优化，详见 AGENTS.md §1）
清理垃圾包、系统升级、`sysctl` 网络调优、`limits` 文件描述符、journald 限 50M；sing-box 与 caddy(naive) 就位（可由 `install.sh` 自动从 Releases 拉取）。

### 步骤 1-3 证书

**方式 A：acme 自动签发（默认，需 Dynu 凭证）**
```bash
# 凭证经环境变量传入；可选 ACME_TARBALL 指向 vendored acme.sh 快照
DOMAIN=your-domain.com EMAIL=you@example.com \
  DYNU_API_KEY=... [DYNU_CLIENT_ID=... DYNU_SECRET=...] \
  nohup bash ansgo-cert-issue.sh > /root/ansgo-cert-issue.log 2>&1 &
# 结果：/etc/ansgo-cert.status (SUCCESS_A | SUCCESS_B | FAILED)
# 证书落点：/etc/ssl/ansgo/{fullchain,privkey}.pem
```

**方式 B：手动指定已有证书（v1.5.0+，跳过 acme）**

已有证书（其他 ACME 客户端 / Caddy / 商业证书）时，无需 Dynu 凭证与 acme.sh，直接在 `panel.json` 填写证书与私钥的完整绝对路径：
```bash
# panel.json 中设置（cert_fullchain / cert_privkey 为服务器上的绝对路径）
# {
#   "cert_mode": "manual",
#   "cert_dir": "/etc/ssl/ansgo",          # acme 模式回退目录（manual 模式下忽略）
#   "cert_fullchain": "/etc/letsencrypt/live/your-domain.com/fullchain.pem",
#   "cert_privkey":   "/etc/letsencrypt/live/your-domain.com/privkey.pem",
#   ...
# }
# caddy / sing-box / 面板会直接引用上述绝对路径（不复制到 /etc/ssl/ansgo/）
# 替换证书后：面板「证书管理」点「重新加载证书」，或 SSH 执行 ansgo-cert-reload
```
> 手动证书模式无需运行 `ansgo-cert-issue.sh`，也不需要 `/etc/ansgo-cert.status`。

### 步骤 4-5 切换真实证书 + 域名伪装
```bash
# 建 /etc/ansgo/panel.json（含 disguise_panel/disguise_naive 字段）+ /etc/ansgo/secrets.env
ansgo-genconf all && ansgo-genconf validate
systemctl restart sing-box && systemctl restart caddy
```
`disguise_panel` / `disguise_naive` 字段：`:443` 直访伪装站 与 naive 端口伪装站，各自可设 `proxy:<URL>`（反代，默认 `example.com`）或 `page`（静态默认页）。两者均可在 Web 后台「面板设置」页独立配置，修改后 caddy 自动重载。

### 步骤 6-8 脚本 + 面板
```bash
install -m 0755 ansgo-admin ansgo-genconf ansgo-cert-reload /usr/local/bin/
# 面板：交叉编译（mac）
cd panel && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ansgo-panel .
# 上传后：setpass + 启动
PW=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)
/usr/local/bin/ansgo-panel -setpass "$PW" && echo "面板密码: $PW"
install -m 0644 ansgo-panel.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now ansgo-panel
```

## 资源来源（全部自有 GitHub）

- 脚本/源码：`raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/...`
- 二进制（sing-box / caddy-naive / ansgo-panel / acme.sh 快照）：`github.com/jiasongji/ANS-GO/releases/download/v1.5.0/...`
- 面板镜像：`ghcr.io/jiasongji/ansgo-panel:latest`（all-in-one 镜像 `ghcr.io/jiasongji/ansgo:latest`）

## ⚠️ 实战注意事项（复现者必读）

1. **更新二进制必须 stop→rm→scp→md5→start**：scp 覆盖运行中二进制会静默失败（sftp `dest open Failure`），restart 只重启旧文件。md5 是判断"是否真更新"的唯一可靠手段。
2. **caddy 用 restart 不用 reload**：Caddyfile 设 `admin off`，reload 必失败。
3. **改前端 HTML 必须重新编译 Go 二进制**：`web/index.html` 经 `//go:embed` 编译进 ansgo-panel，改完 `go build` 重传，用户硬刷新浏览器。
4. **前端 SPA"点击无反应"=JS 语法错误**：提取 `<script>` 用 `node --check` 查语法。
5. **长任务用后台守护**：SSH 长连接易超时，`nohup ... > log 2>&1 &`。
6. **改配置前必备份**：`ansgo-admin backup` → `/etc/ansgo-backup-{ts}/`，失败 `ansgo-admin restore` 回滚。
7. **Docker 一体化已完整支持**（v1.4.0+）：`--docker` 用 all-in-one 镜像（`ghcr.io/jiasongji/ansgo`），容器内 systemd 作 PID 1，systemctl/journalctl/ansgo-admin 原生可用，面板 0 改动。需 `privileged` + `cgroup: host` + host 网络。LXC 低配（256MB）仍推荐裸金属（内存占用最小）。
8. **移动端 / 白天主题**（v1.4.0+，v1.4.3 增强）：面板已全面适配移动端（侧边栏抽屉 / 表单 label 上置 / 网格单列）并修复白天模式下白字不可见问题。v1.4.3 进一步把导航改为**左侧可折叠侧边栏**（桌面端可折叠为图标条 + 移动端汉堡抽屉，localStorage 记忆状态）并统一双主题配色（active 项蓝底白字 / `<code>` 显式着色 / overlay 阴影双主题适配）。响应式 `@media` 块必须置于 `<style>` 末尾（所有基础规则之后），否则同特异性会被后面的基础规则覆盖。
9. **彻底卸载**（v1.4.2+）：`install.sh --uninstall --purge` 自动检测 Docker/裸金属并彻底清理（含 docker 卷/镜像）；Docker 分支必须先删容器再删镜像，否则报 “image is being used”。`bash -n` 只查语法不执行，改完脚本务必实际执行关键路径验证。
10. **前端改动后必须重新编译上传**（v1.4.3+）：HTML 经 `//go:embed` 编译进二进制，改 `web/index.html` 必须重新 `go build` 上传并硬刷新浏览器。改前端后建议用 headless Chrome 截图多主题（暗黑/白天）× 多视口（桌面/移动）× 状态（展开/折叠/抽屉）交叉验证，避免静默 CSS 回归。
11. **手动设置密钥**（v1.5.0+）：「密钥管理」页支持手动输入 SS / AnyTLS / Naive + 第二组的自定义密钥。手动设置走 Go 面板的 `setSecret()`（原子 tmp+rename 写 `secrets.env`，避开旧版 `_setsecret` 的 sed `|` 分隔符对特殊字符的坑）；SS2022 自动复用 `validSS2022Key()` 校验 base64(16/32 字节) 长度。随机生成仍走 `ansgo-admin regen/regen2`（生成值无特殊字符，安全）。
12. **手动指定证书路径**（v1.5.0+）：`panel.json` 新增 `cert_mode`（`acme`|`manual`）+ `cert_fullchain` + `cert_privkey`。manual 模式下 caddy/sing-box/面板直接引用用户指定的绝对路径（不复制到 `/etc/ssl/ansgo/`）。Go 端 `certPaths()` 与 `ansgo-genconf`（python）按同一语义解析：manual 且两路径齐全 → 用绝对路径；否则回退 `cert_dir/fullchain.pem`（兼容旧部署）。⚠️ manual 模式续期需用户自行管理，替换文件后点面板「重新加载证书」或执行 `ansgo-cert-reload`。
13. **cert-reload 不再 grep 固定路径**（v1.5.0+）：`ansgo-cert-reload` 去掉了 `grep '/etc/ssl/ansgo/'` 判断，改为「配置文件存在即 reload」，兼容 manual 模式的自定义证书路径。脚本只在「证书确已变更」时被调用（acme.sh `--reloadcmd` 或面板按钮），所以无条件 reload 是安全的。
14. **Docker + 手动证书**（v1.5.0+）：`--docker --cert-mode manual` 部署时，证书路径必须挂载进容器（在 `docker-compose.yml` 的 `volumes` 加 `- /etc/letsencrypt:/etc/letsencrypt:ro`），否则容器内 `entrypoint.sh` 会报 `ERROR: 证书文件不存在`。
