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
| `ansgo-cert-reload` | `/usr/local/bin/` | 证书续期后按需 reload 三服务 |
| `ansgo-genconf` | `/usr/local/bin/` | 从 `panel.json` + `secrets.env` 重新生成 sing-box / caddy 配置 |
| `ansgo-admin` | `/usr/local/bin/` | 离线管理脚本（面板全挂也能 SSH 管理一切） |
| `ansgo-panel.service` | `/etc/systemd/system/` | 面板 systemd unit |
| `systemd/{sing-box,caddy}.service` | `/etc/systemd/system/` | 代理服务 unit |
| `Dockerfile` / `docker-compose.yml` | — | 面板镜像构建（→ ghcr.io） |
| `panel/` | 本地交叉编译 | Go 面板源码 |

## 一键部署（推荐）

```bash
# 交互式
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)
# 带参数
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com --dynu-key <KEY> --email you@example.com --non-interactive
```

## 手动部署（对应 AGENTS.md §9）

### 前置（第一阶段优化，详见 AGENTS.md §1）
清理垃圾包、系统升级、`sysctl` 网络调优、`limits` 文件描述符、journald 限 50M；sing-box 与 caddy(naive) 就位（可由 `install.sh` 自动从 Releases 拉取）。

### 步骤 1-3 证书
```bash
# 凭证经环境变量传入；可选 ACME_TARBALL 指向 vendored acme.sh 快照
DOMAIN=your-domain.com EMAIL=you@example.com \
  DYNU_API_KEY=... [DYNU_CLIENT_ID=... DYNU_SECRET=...] \
  nohup bash ansgo-cert-issue.sh > /root/ansgo-cert-issue.log 2>&1 &
# 结果：/etc/ansgo-cert.status (SUCCESS_A | SUCCESS_B | FAILED)
```

### 步骤 4-5 切换真实证书 + 域名伪装
```bash
# 建 /etc/ansgo/panel.json（含 disguise 字段）+ /etc/ansgo/secrets.env
ansgo-genconf all && ansgo-genconf validate
systemctl restart sing-box && systemctl restart caddy
```
`disguise` 字段：`proxy:https://soft.xiaoz.org`（默认，反代伪装站）或 `page`（静态默认页）。

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
- 二进制（sing-box / caddy-naive / ansgo-panel / acme.sh 快照）：`github.com/jiasongji/ANS-GO/releases/download/v1.0.0/...`
- 面板镜像：`ghcr.io/jiasongji/ansgo-panel:latest`

## ⚠️ 实战注意事项（复现者必读）

1. **更新二进制必须 stop→rm→scp→md5→start**：scp 覆盖运行中二进制会静默失败（sftp `dest open Failure`），restart 只重启旧文件。md5 是判断"是否真更新"的唯一可靠手段。
2. **caddy 用 restart 不用 reload**：Caddyfile 设 `admin off`，reload 必失败。
3. **改前端 HTML 必须重新编译 Go 二进制**：`web/index.html` 经 `//go:embed` 编译进 ansgo-panel，改完 `go build` 重传，用户硬刷新浏览器。
4. **前端 SPA"点击无反应"=JS 语法错误**：提取 `<script>` 用 `node --check` 查语法。
5. **长任务用后台守护**：SSH 长连接易超时，`nohup ... > log 2>&1 &`。
6. **改配置前必备份**：`ansgo-admin backup` → `/etc/ansgo-backup-{ts}/`，失败 `ansgo-admin restore` 回滚。
7. **面板容器化受限**：面板需 exec 宿主 systemctl/ansgo-admin 管理服务，Docker 内服务控制能力受限；LXC 低配推荐裸金属。
