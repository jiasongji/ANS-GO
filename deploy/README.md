# ANS-GO 部署文件

本目录是 ANS-GO 代理方案的**全部部署产物**（脚本 + 面板源码），可在干净的 Debian 12 服务器上复现整个部署。
敏感凭证（SSH 密码、Dynu API Key/OAuth、协议密钥、面板密码）**不在此处**，见服务器 `/etc/proxy-secrets.env`、`/etc/bv-panel/config.json`、`/root/.acme.sh/`。

> 完整架构与约束见上级目录 [`AGENTS.md`](../AGENTS.md)（唯一事实来源）。

## 文件清单

| 文件 | 安装到 | 作用 |
|------|--------|------|
| `dns_dynukey.sh` | `/root/.acme.sh/dnsapi/dns_dynukey.sh` | acme.sh Dynu DNS-01 钩子（路径 A，API Key） |
| `bv-cert-reload` | `/usr/local/bin/bv-cert-reload` | 证书续期后按需 reload 三服务（续期 reloadcmd） |
| `bv-deploy-cert.sh` | 一次性执行 | 步骤 1-3：装 acme.sh + 签发 ECDSA 证书（A 默认 / B 降级） |
| `bv-genconf` | `/usr/local/bin/bv-genconf` | 从 `config.json` + `proxy-secrets.env` 重新生成 sing-box / caddy 配置 |
| `bv-admin` | `/usr/local/bin/bv-admin` | 离线管理脚本（面板全挂也能 SSH 管理一切） |
| `bv-panel.service` | `/etc/systemd/system/bv-panel.service` | 面板 systemd unit |
| `panel/` | 本地交叉编译 | Go 面板源码，编译产出 `/usr/local/bin/bv-panel` |

## 部署流程（对应 AGENTS.md §9）

### 前置（第一阶段优化，详见 AGENTS.md §1，本目录不含）
- 清理垃圾包、系统升级、`/etc/sysctl.d/99-proxy-tune.conf`、`/etc/security/limits.d/99-proxy.conf`、journald 限 50M
- 已装 `sing-box`（ss + anytls）与 `caddy`（forwardproxy-naive），用自签证书 `www.bing.com` 跑起来

### 步骤 1-3：证书
```bash
# 凭证经环境变量传入（不落盘额外文件）
nohup env DYNU_API_KEY=... DYNU_CLIENT_ID=... DYNU_SECRET=... \
  bash bv-deploy-cert.sh > /root/bv-deploy-cert.log 2>&1 &
# 结果写入 /etc/bv-la-cert.status（SUCCESS_A | SUCCESS_B | FAILED）
```

### 步骤 4-5：切换真实证书
```bash
# 1) 建 config.json（端口 + 面板设置）与 proxy-secrets.env（密钥）—— 见下方模板
# 2) 重新生成服务配置并校验
bv-genconf all && bv-genconf validate
# 3) 重启服务
systemctl restart sing-box && systemctl reload caddy || systemctl restart caddy
```

### 步骤 6：bv-admin
```bash
install -m 0755 bv-admin /usr/local/bin/bv-admin
install -m 0755 bv-genconf /usr/local/bin/bv-genconf
```

### 步骤 7：交叉编译 bv-panel（在 mac 上）
```bash
cd panel
# 依赖需代理拉取（modernc.org/sqlite + golang.org/x/crypto）
HTTPS_PROXY=http://127.0.0.1:1666 go mod tidy
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bv-panel .
scp bv-panel root@<server>:/usr/local/bin/bv-panel
```

### 步骤 8：启动面板
```bash
install -m 0644 bv-panel.service /etc/systemd/system/bv-panel.service
# 生成管理员密码（bcrypt 写入 config.json，打印明文仅此一次）
PW=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)
/usr/local/bin/bv-panel -setpass "$PW" && echo "面板密码: $PW"
systemctl daemon-reload && systemctl enable --now bv-panel
```

## 配置文件模板（服务器侧，root 独占）

### `/etc/proxy-secrets.env`（协议密钥，600）
```sh
SS_METHOD=2022-blake3-aes-128-gcm
SS_KEY=$(openssl rand -base64 16)          # base64(16 bytes)
ANYTLS_PASS=$(openssl rand -hex 16)
ANYTLS_UUID=$(cat /proc/sys/kernel/random/uuid)
NAIVE_USER=$(openssl rand -hex 6)
NAIVE_PASS=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9' | head -c 20)
```

### `/etc/bv-panel/config.json`（端口 + 面板设置，600）
```json
{
  "domain": "你的域名",
  "panel_port": 15608,
  "url_path": "/随机8位hex/",
  "admin_user": "ad_admin",
  "admin_pass_hash": "由 bv-panel -setpass 填写",
  "session_hours": 8,
  "login_lock_threshold": 5,
  "login_lock_minutes": 10,
  "ss_port": 23456, "ss_method": "2022-blake3-aes-128-gcm",
  "anytls_port": 8443, "naive_port": 443,
  "cert_dir": "/etc/ssl/bv-la",
  "db_path": "/etc/bv-panel/sessions.db"
}
```

## ⚠️ 实战注意事项（复现者必读）

以下是在 2026-06-19 首次部署中踩过并沉淀的坑，复现时务必留意（详见 [AGENTS.md §9 实战备注](../AGENTS.md)）：

1. **更新二进制必须 stop→rm→scp→md5→start**：scp 覆盖服务器上**正在运行**的二进制会静默失败（sftp 报 `dest open Failure`），后续 restart 只是重启旧文件——修复不生效。正确：`systemctl stop` → `rm` 旧文件 → `scp` → `md5sum` 对比本地与服务器一致 → `systemctl start`。**md5 是判断"是否真更新"的唯一可靠手段**（文件大小可能恰好相同）。
2. **caddy 用 restart 不用 reload**：Caddyfile 设了 `admin off`，无 admin API 通道，`systemctl reload caddy` 必失败。改配置/续期统一 `systemctl restart caddy`（naive 闪断 1-2s）。
3. **改前端 HTML 必须重新编译 Go 二进制**：`web/index.html` 经 `//go:embed` 编译进 bv-panel，改完必须 `go build` 重新编译上传，再按第 1 条流程更新。改完用户需硬刷新浏览器（Cmd/Ctrl+Shift+R）清缓存。
4. **前端 SPA"点击无反应"=JS 语法错误**：若面板按钮无任何反应，首要怀疑 `<script>` 块有语法错误致整块脚本失效、所有函数未定义（静默）。诊断：提取 `<script>` 内容 `node --check` 查语法。
5. **长任务用后台守护**：本机在中国大陆时，SSH 长连接易超时。证书签发等耗时操作用 `nohup ... > log 2>&1 & disown`，SSH 立即返回，再轮询日志。
6. **改配置前必备份**：`bv-admin backup` 或手工 `cp -a` 到 `/etc/bv-la-backup-{ts}/`，失败 `bv-admin restore` 回滚。

## 关键设计点

- **三服务完全解耦**：caddy / sing-box / bv-panel 独立进程、端口、unit，改任一个不影响另两个。
- **共享证书**：一张 ECDSA 证书喂三服务，续期一次三服务一起重载（由 `bv-cert-reload` 脚本按需触发：caddy restart / sing-box+panel restart）。
- **改端口不锁死**：面板端口在 `config.json`，改完 3 秒倒计时自重启；SSH 兜底 `bv-admin panel-port`。
- **不擅自加防火墙 drop**：LXC 安全由宿主负责，避免锁死 SSH。
- **SQLite 用 modernc.org/sqlite**（纯 Go，CGO_ENABLED=0 纯静态，无 libc 依赖）。
