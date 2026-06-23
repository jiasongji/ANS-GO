# ANS-GO 部署文件

本目录是 ANS-GO 代理方案的**全部部署产物**（脚本 + 面板源码 + Docker）。

> **推荐用根目录 [`../install.sh`](../install.sh) 一键部署**（交互式 / 带参数），资源全部取自本仓库 GitHub。
> 本文档面向**手动部署 / 复现 / 二次开发 / 二进制构建**。完整方案见上级 [`AGENTS.md`](../AGENTS.md)（唯一事实来源）。
>
> 📌 **所有命令默认以 `root` 用户执行**。

---

## 📚 目录

- [文件清单](#文件清单)
- [一键部署（推荐）](#一键部署推荐)
- [Docker 一体化部署](#docker-一体化部署)
- [手动部署（裸金属）](#手动部署裸金属对应-agentsmd-9)
- [面板 Go 二进制构建](#面板-go-二进制构建)
- [Docker 镜像构建](#docker-镜像构建)
- [更新升级](#更新升级)
- [卸载](#卸载)
- [资源来源](#资源来源全部自有-github)
- [⚠️ 实战注意事项](#️-实战注意事项复现者必读)

---

## 文件清单

| 文件 | 安装到 | 作用 |
|------|--------|------|
| [`../install.sh`](../install.sh) | 服务器执行 | ⭐ 一键部署（交互式 + 带参数 + 卸载）|
| `dns_dynukey.sh` | `/root/.acme.sh/dnsapi/` | acme.sh Dynu DNS-01 钩子（API Key） |
| `ansgo-cert-issue.sh` | 一次性执行 | 装 acme.sh + 签发 ECDSA 证书（A 默认 / B 降级），支持 vendored acme.sh |
| `ansgo-cert-reload` | `/usr/local/bin/` | 证书续期/替换后 reload 三服务（v1.5.0+ 改为「配置存在即 reload」，兼容手动证书）|
| `ansgo-genconf` | `/usr/local/bin/` | 从 `panel.json` + `secrets.env` 重新生成 sing-box / caddy 配置（v1.5.12 修复落地服务路由规则）|
| `ansgo-admin` | `/usr/local/bin/` | 离线管理脚本（面板全挂也能 SSH 管理一切）|
| `ansgo-panel.service` | `/etc/systemd/system/` | 面板 systemd unit |
| `systemd/{sing-box,caddy}.service` | `/etc/systemd/system/` | 代理服务 unit |
| `Dockerfile.allinone` / `docker-compose.yml` / `docker/entrypoint.sh` | — | ⭐ all-in-one 一体化镜像（sing-box+caddy+面板+systemd，→ `ghcr.io/jiasongji/ansgo`）|
| `Dockerfile` | — | 面板单镜像构建（仅面板，兼容用 → `ghcr.io/jiasongji/ansgo-panel`）|
| `panel/` | 本地交叉编译 | Go 面板源码 |

---

## 一键部署（推荐）

**交互式**（逐项确认，推荐首次使用）：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)
```

**带参数一键**（CI / 自动化，v1.5.12 起端口默认随机）：

```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com --dynu-key <KEY> --email you@example.com --non-interactive
```

完整参数全集和所有安装方式见根目录 [README.md](../README.md#-全新安装)。

---

## Docker 一体化部署

`install.sh --docker` 自动装 docker、生成 `ansgo.env`（凭证，600 权限）+ `docker-compose.yml`、拉 `ghcr.io/jiasongji/ansgo:latest`，`docker compose up -d` 拉起单容器：

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
cd /etc/ansgo-docker && docker compose logs -f ansgo    # 查日志 / 签证书进度
docker exec ansgo ansgo-admin status                    # 服务状态
docker exec ansgo ansgo-admin info                      # 节点连接参数
docker exec ansgo ansgo-admin panel-pass                # 重置面板密码
docker restart ansgo                                    # 重启容器
docker compose pull && docker compose up -d             # 升级镜像
```

> 手动构建镜像：`docker build -t ghcr.io/jiasongji/ansgo:latest -f deploy/Dockerfile.allinone .`（国内需配 `HTTPS_PROXY`）。多架构构建命令见下文 [Docker 镜像构建](#docker-镜像构建)。

---

## 手动部署（裸金属，对应 AGENTS.md §9）

### 前置（第一阶段优化，详见 AGENTS.md §1）

清理垃圾包、系统升级、`sysctl` 网络调优、`limits` 文件描述符、journald 限 50M；sing-box 与 caddy(naive) 就位（可由 `install.sh` 自动从 Releases 拉取）。

```bash
# 系统调优（幂等，install.sh 已自动执行）
cat > /etc/sysctl.d/99-ansgo-tune.conf <<'EOF'
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.ipv4.tcp_fastopen = 3
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.tcp_mtu_probing = 1
net.ipv4.tcp_max_tw_buckets = 1048576
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fin_timeout = 15
net.ipv4.tcp_keepalive_time = 300
net.ipv4.tcp_keepalive_intvl = 30
net.ipv4.tcp_keepalive_probes = 3
net.ipv4.ip_local_port_range = 10000 65535
EOF
sysctl --system

cat > /etc/security/limits.d/99-ansgo.conf <<'EOF'
* soft nofile 1048576
* hard nofile 1048576
root soft nofile 1048576
root hard nofile 1048576
EOF

mkdir -p /etc/systemd/journald.conf.d
cat > /etc/systemd/journald.conf.d/size.conf <<'EOF'
[Journal]
SystemMaxUse=50M
EOF
systemctl restart systemd-journald
```

### 步骤 1：装管理脚本

```bash
mkdir -p /etc/ansgo /etc/ansgo-deploy /etc/sing-box /etc/caddy /var/www/html
for f in ansgo-admin ansgo-genconf ansgo-cert-reload ansgo-cert-issue.sh dns_dynukey.sh; do
  curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/$f -o /etc/ansgo-deploy/$f
done
install -m 0755 /etc/ansgo-deploy/ansgo-admin       /usr/local/bin/ansgo-admin
install -m 0755 /etc/ansgo-deploy/ansgo-genconf     /usr/local/bin/ansgo-genconf
install -m 0755 /etc/ansgo-deploy/ansgo-cert-reload /usr/local/bin/ansgo-cert-reload
```

### 步骤 2：装 sing-box + caddy(naive) 二进制

```bash
# sing-box：SagerNet 官方 release（推荐）
SB_VER=1.13.13; ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -fsSL https://github.com/SagerNet/sing-box/releases/download/v${SB_VER}/sing-box-${SB_VER}-linux-${ARCH}.tar.gz | tar xz -C /tmp
install -m 0755 /tmp/sing-box-*/sing-box /usr/local/bin/sing-box

# caddy-naive：本项目 release 预编译产物（推荐）
VER=v1.5.12
curl -fsSL https://github.com/jiasongji/ANS-GO/releases/download/${VER}/caddy-naive-linux-${ARCH} -o /usr/local/bin/caddy
chmod 0755 /usr/local/bin/caddy
# 失败则现场 xcaddy 编译（需 Go 1.22+ + git，约 3-5 分钟）：
#   go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
#   git clone -b naive --depth 1 https://github.com/klzgrad/forwardproxy.git /tmp/fp
#   xcaddy build --with github.com/caddyserver/forwardproxy=/tmp/fp --output /usr/local/bin/caddy
```

### 步骤 3：生成配置 + 密钥

```bash
# secrets.env（v1.5.12 起随机端口）
SS_PORT=$(shuf -i 10000-65535 -n 1)
ANYTLS_PORT=$(shuf -i 10000-65535 -n 1)
NAIVE_PORT=$(shuf -i 10000-65535 -n 1)
PANEL_PORT=$(shuf -i 10000-65535 -n 1)
URLPATH="/$(openssl rand -hex 4)/"

cat > /etc/ansgo/secrets.env <<EOF
SS_METHOD=2022-blake3-aes-128-gcm
SS_KEY=$(openssl rand -base64 16)
ANYTLS_PASS=$(openssl rand -hex 16)
ANYTLS_UUID=$(cat /proc/sys/kernel/random/uuid)
NAIVE_USER=$(openssl rand -hex 6)
NAIVE_PASS=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)
EOF
chmod 600 /etc/ansgo/secrets.env

cat > /etc/ansgo/panel.json <<EOF
{
  "domain": "your-domain.com",
  "panel_port": ${PANEL_PORT},
  "url_path": "${URLPATH}",
  "admin_user": "admin",
  "admin_pass_hash": "PLACEHOLDER",
  "session_hours": 8,
  "login_lock_threshold": 5,
  "login_lock_minutes": 10,
  "ss_port": ${SS_PORT},
  "ss_method": "2022-blake3-aes-128-gcm",
  "anytls_port": ${ANYTLS_PORT},
  "naive_port": ${NAIVE_PORT},
  "disguise_panel": "proxy:https://soft.xiaoz.org",
  "disguise_naive": "proxy:https://soft.xiaoz.org",
  "svc_ss_enabled": "false",
  "svc_anytls_enabled": "false",
  "svc_naive_enabled": "false",
  "caddy_enable": "true",
  "cert_mode": "acme",
  "cert_dir": "/etc/ssl/ansgo",
  "db_path": "/etc/ansgo/sessions.db"
}
EOF
chmod 600 /etc/ansgo/panel.json
echo "⚠️ 随机端口：ss=${SS_PORT} anytls=${ANYTLS_PORT} naive=${NAIVE_PORT} panel=${PANEL_PORT}"
```

### 步骤 4：签发证书

**方式 A：acme 自动签发**（需 Dynu 凭证）：

```bash
DOMAIN=your-domain.com EMAIL=you@example.com \
  DYNU_API_KEY=... [DYNU_CLIENT_ID=... DYNU_SECRET=...] \
  nohup bash /etc/ansgo-deploy/ansgo-cert-issue.sh > /root/ansgo-cert-issue.log 2>&1 &
# 结果：/etc/ansgo-cert.status (SUCCESS_A | SUCCESS_B | FAILED)
# 证书落点：/etc/ssl/ansgo/{fullchain,privkey}.pem
```

**方式 B：手动指定已有证书**（v1.5.0+，跳过 acme）：

已有证书时，在 `panel.json` 改 `cert_mode` 为 `manual` 并填 `cert_fullchain` / `cert_privkey`（服务器上的绝对路径）：

```json
{
  "cert_mode": "manual",
  "cert_fullchain": "/etc/letsencrypt/live/your-domain.com/fullchain.pem",
  "cert_privkey":   "/etc/letsencrypt/live/your-domain.com/privkey.pem"
}
```

caddy / sing-box / 面板会直接引用上述绝对路径（不复制到 `/etc/ssl/ansgo/`）。替换证书后：面板「证书管理」点「重新加载证书」，或 SSH 执行 `ansgo-cert-reload`。

### 步骤 5：生成服务配置

```bash
ansgo-genconf all && ansgo-genconf validate
```

`disguise_panel` / `disguise_naive` 字段：`:443` 直访伪装站 与 naive 端口伪装站，各自可设 `proxy:<URL>`（反代，默认 `soft.xiaoz.org`）或 `page`（静态默认页）。两者均可在 Web 后台「面板设置」页独立配置，修改后 caddy 自动重载。

### 步骤 6：部署 systemd unit + 启动

```bash
# 装 sing-box / caddy unit
for s in sing-box caddy; do
  curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/systemd/$s.service \
    -o /etc/systemd/system/$s.service
done
systemctl daemon-reload
systemctl enable caddy && systemctl restart caddy   # :443 伪装站
# sing-box 暂不启动（无代理服务安装前不需要）
```

### 步骤 7：部署面板

```bash
# 上传二进制后（构建见下文「面板 Go 二进制构建」）：
install -m 0755 ansgo-panel /usr/local/bin/ansgo-panel

# 设置管理员密码（bcrypt 写入 panel.json，打印明文仅一次）
PW=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)
/usr/local/bin/ansgo-panel -setpass "$PW" && echo "面板密码: $PW"

curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/ansgo-panel.service \
  -o /etc/systemd/system/ansgo-panel.service
systemctl daemon-reload
systemctl enable --now ansgo-panel
```

---

## 面板 Go 二进制构建

面板源码在 `panel/`，需 Go 1.26+（`go.mod` 要求）。**前端 HTML 经 `//go:embed` 编译进二进制**，改 `web/index.html` 后必须重新构建。

### 本地交叉编译（macOS → linux/amd64）

```bash
cd deploy/panel
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ansgo-panel .
# 或 arm64（树莓派 / ARM 服务器）：
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o ansgo-panel .
```

> `CGO_ENABLED=0` 生成纯静态二进制（无 libc 依赖），sqlite 用 modernc.org 纯 Go 驱动。

### 通过 Docker 构建（无 Go 环境时）

```bash
cd deploy/panel
docker run --rm -v "$PWD":/src -w /src -e GOMODCACHE=/tmp/gomodcache \
  golang:1.26 bash -c 'CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ansgo-panel .'
```

### 上传到服务器（必须 stop→rm→scp→md5→start）

⚠️ **scp 覆盖运行中二进制会静默失败**（sftp `dest open Failure`），且后续 restart 只重启旧文件。md5 是判断"是否真更新"的唯一可靠手段：

```bash
systemctl stop ansgo-panel
rm /usr/local/bin/ansgo-panel
scp deploy/panel/ansgo-panel root@<server>:/usr/local/bin/ansgo-panel
ssh root@<server> 'md5sum /usr/local/bin/ansgo-panel'   # 与本地 md5sum 对比，必须一致
ssh root@<server> 'systemctl start ansgo-panel'
```

---

## Docker 镜像构建

### 单架构本地构建

```bash
docker build -t ghcr.io/jiasongji/ansgo:latest -f deploy/Dockerfile.allinone .
```

### 多架构构建 + 推送 ghcr.io（开发机执行）

国内首次构建需要 `docker-container` driver + 代理注入：

```bash
# 创建 builder（首次）
docker buildx create --driver docker-container --name ansgo-builder

# 多架构构建 + 推送（amd64 + arm64）
docker buildx build --builder ansgo-builder \
  --platform linux/amd64,linux/arm64 \
  --build-arg HTTP_PROXY=http://host.docker.internal:1666 \
  --build-arg HTTPS_PROXY=http://host.docker.internal:1666 \
  -t ghcr.io/jiasongji/ansgo:latest \
  -t ghcr.io/jiasongji/ansgo:v1.5.12 \
  -f deploy/Dockerfile.allinone . --push
```

> ⚠️ apt/go 不读环境变量但读 build-arg，必须用 `--build-arg HTTP_PROXY=...` 注入代理。
>
> ⚠️ ghcr.io package visibility 修改 API 对 user-owned package 返回 404，必须 web UI 操作。

---

## 更新升级

### 重新部署（最简单，配置保留）

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)
# 已存在的 panel.json / secrets.env 会被保留
```

### 升级单个组件

```bash
ansgo-admin update sing-box                          # 升级 sing-box（自动拉 SagerNet 最新版）
# 面板/caddy 升级见上文「面板 Go 二进制构建」和「上传到服务器」
```

### 升级 Docker 镜像

```bash
cd /etc/ansgo-docker
docker compose pull && docker compose up -d
docker image prune -f                                # 清理旧镜像（可选）
```

### 升级前必备份

```bash
ansgo-admin backup                                   # 裸金属
docker exec ansgo ansgo-admin backup                 # Docker
```

---

## 卸载

`install.sh --uninstall` 自动检测部署模式（Docker / 裸金属），两级清理：

```bash
# 默认卸载（保留配置/卷，可重装不丢参数）
bash install.sh --uninstall

# 彻底卸载（删除一切，不可恢复）
bash install.sh --uninstall --purge
```

完整对比见根目录 [README.md 卸载章节](../README.md#-卸载--彻底卸载--清理)。

- **Docker 分支**：`docker compose down -v` + `docker rm -f ansgo` 兜底 + 卷名模式匹配(`*_ansgo_*`)兜底删卷，先删容器再删镜像避免占用错误
- **裸金属分支**：停 systemd 服务 + 删二进制/unit；`--purge` 额外删 `/etc/ansgo` `/etc/ssl/ansgo` `/root/.acme.sh` + sysctl/limits 调优
- docker 本体保留（可能被其他服务使用）；卸载前二次确认

---

## 资源来源（全部自有 GitHub）

- 脚本/源码：`raw.githubusercontent.com/jiasongji/ANS-GO/main/deploy/...`
- sing-box：**SagerNet 官方 release**（`github.com/SagerNet/sing-box/releases/download/v1.13.13/...`），本项目 release vendored 兜底
- caddy-naive：本项目 release 预编译产物（双架构），失败回退 xcaddy 现场编译
- ansgo-panel 二进制：`github.com/jiasongji/ANS-GO/releases/download/v1.5.12/ansgo-panel-linux-<arch>`
- acme.sh：本仓库 vendored 快照（可选），或官方 `https://get.acme.sh`
- 面板镜像：`ghcr.io/jiasongji/ansgo:latest`（all-in-one）/ `ghcr.io/jiasongji/ansgo-panel:latest`（面板单镜像）

> ⚠️ **release 资产维护**：每次发新版本 release 必须上传全部 6 个资产（`ansgo-panel-linux-{amd64,arm64}` + `caddy-naive-linux-{amd64,arm64}` + `sing-box-linux-{amd64,arm64}.tar.gz`）。

---

## ⚠️ 实战注意事项（复现者必读）

1. **更新二进制必须 stop→rm→scp→md5→start**：scp 覆盖运行中二进制会静默失败（sftp `dest open Failure`），restart 只重启旧文件。md5 是判断"是否真更新"的唯一可靠手段。
2. **caddy 用 restart 不用 reload**：Caddyfile 设 `admin off`，reload 必失败。
3. **改前端 HTML 必须重新编译 Go 二进制**：`web/index.html` 经 `//go:embed` 编译进 ansgo-panel，改完 `go build` 重传，用户硬刷新浏览器。
4. **前端 SPA"点击无反应"=JS 语法错误**：提取 `<script>` 用 `node --check` 查语法。
5. **长任务用后台守护**：SSH 长连接易超时，`nohup ... > log 2>&1 &`。
6. **改配置前必备份**：`ansgo-admin backup` → `/etc/ansgo-backup-{ts}/`，失败 `ansgo-admin restore` 回滚。
7. **Docker 一体化**（v1.4.0+）：`--docker` 用 all-in-one 镜像，容器内 systemd 作 PID 1，systemctl/journalctl/ansgo-admin 原生可用，面板 0 改动。需 `privileged` + `cgroup: host` + host 网络。LXC 低配（256MB）仍推荐裸金属。
8. **响应式 CSS**：响应式 `@media` 块必须置于 `<style>` 末尾（所有基础规则之后），否则同特异性会被后面的基础规则覆盖。
9. **彻底卸载**（v1.4.2+）：`install.sh --uninstall --purge` 自动检测 Docker/裸金属并彻底清理（含 docker 卷/镜像）；Docker 分支必须先删容器再删镜像。`bash -n` 只查语法不执行，改完脚本务必实际执行关键路径验证。
10. **前端改动后必须重新编译上传**（v1.4.3+）：HTML 经 `//go:embed` 编译进二进制，改 `web/index.html` 必须重新 `go build` 上传并硬刷新浏览器。改前端后建议用 headless Chrome 截图多主题 × 多视口 × 状态交叉验证。
11. **手动设置密钥**（v1.5.0+）：手动设置走 Go 面板的 `setSecret()`（原子 tmp+rename 写 `secrets.env`，避开旧版 `_setsecret` 的 sed `|` 分隔符对特殊字符的坑）；SS2022 自动复用 `validSS2022Key()` 校验长度。随机生成仍走 `ansgo-admin regen/regen2`。
12. **手动指定证书路径**（v1.5.0+）：`panel.json` 新增 `cert_mode`（`acme`|`manual`）+ `cert_fullchain` + `cert_privkey`。manual 模式下 caddy/sing-box/面板直接引用绝对路径。⚠️ manual 模式续期需用户自行管理。
13. **Docker + 手动证书**（v1.5.0+）：`--docker --cert-mode manual` 部署时，证书路径必须挂载进容器，否则容器内 `entrypoint.sh` 会报 `ERROR: 证书文件不存在`。v1.5.7+ install.sh 自动注入 bind mount。
14. **落地服务架构约束**（v1.5.12+）：caddy（NaiveProxy）与 sing-box（SS/AnyTLS）是两个独立进程，**跨进程路由不可能**。任何"naive-2 流量走 sing-box 的 ss-out 落地"的设计在物理上都不可行，naive-2 永远走 caddy 的 direct 出口（中转机 IP），只有 anytls-2 能经 sing-box ss-out 落地到远端服务器。
15. **bash 函数必须先定义后调用**（v1.5.6 教训）：跨函数引用要确认定义顺序；docker build 的 `-f` 相对路径按 cwd 解析，跨目录构建必须用绝对路径。

更多历史 bug 修复与教训见 [AGENTS.md §11 风险与回滚](../AGENTS.md)。
