# ANS-GO

> 在一台低配 LXC VPS 上部署 **NaiveProxy + AnyTLS + Shadowsocks** 三协议代理 + **Go Web 管理面板**，共享一张 Let's Encrypt 证书。可审计、可回滚、可离线管理（SSH 兜底）。

![status](https://img.shields.io/badge/status-已部署验证-brightgreen) ![license](https://img.shields.io/badge/license-MIT-blue) ![stack](https://img.shields.io/badge/stack-Go%20%7C%20bash%20%7C%20sing--box%20%7C%20caddy-orange)

---

## 🚀 一键部署

> 所有资源取自本仓库 GitHub（脚本/源码走 raw，二进制走 Releases，面板镜像走 ghcr.io），不依赖第三方 CDN。

**交互式**（推荐，逐项确认）：
```bash
bash <(curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh)
```

**带参数一键**（CI / 自动化）：
```bash
curl -fsSL https://raw.githubusercontent.com/jiasongji/ANS-GO/main/install.sh \
  | bash -s -- --domain your-domain.com \
             --dynu-key <DYNU_API_KEY> \
             --email you@example.com \
             --non-interactive
```

参数全集：`--domain` · `--dynu-key`（或 `--dynu-client-id`+`--dynu-secret`）· `--email` · `--ss-port` · `--anytls-port` · `--naive-port` · `--panel-port` · `--panel-user` · `--disguise-panel` · `--disguise-naive` · `--non-interactive` · `--docker`

部署完成后脚本会打印：三协议客户端 URI、面板访问地址 + 随机 URL 路径、一次性管理员密码。

---

## ✨ 特性

- **三协议**：NaiveProxy（caddy forwardproxy-naive 分支）/ AnyTLS / Shadowsocks-2022（sing-box 双 inbound）
- **一张证书共享**：acme.sh + Dynu DNS-01 签发 ECDSA 证书，caddy / sing-box / 面板三服务共享，续期一次三服务一起重载
- **域名双伪装**：`:443` 纯反代伪装站（域名直访命中，不提供代理）+ naive 端口独立伪装；两个伪装站点均可在 Web 后台独立配置（默认反代 `example.com`）
- **Web 管理面板**：Go 单二进制（运行内存 ~12MB），中文 UI，管全部协议参数 / 端口 / 证书 / 自身配置，含客户端二维码
- **完全解耦**：caddy / sing-box / 面板是三个独立进程、端口、systemd unit，**改协议端口永远不会断面板**
- **离线兜底**：`ansgo-admin` bash 脚本零依赖，面板全挂也能 SSH 管理一切（含密码/路径/端口重置、备份回滚）
- **安全**：管理员密码 bcrypt、按 IP 登录锁定、8 小时会话、随机 URL 路径、全程 TLS

## 🏗 架构

```
            Let's Encrypt (acme.sh + Dynu DNS-01)  路径A: API Key（默认）/ B: OAuth（降级）
                        │ 签发 / 自动续期 + reload
                        ▼
          /etc/ssl/ansgo/{fullchain,privkey}.pem     一张证书三服务共享
                        │
      ┌─────────────────┼─────────────────────┐
      ▼                 ▼                     ▼
 caddy :443       sing-box :8443        面板(Go) :15608
 NaiveProxy       AnyTLS + Shadowsocks   Web 管理面板
 + 域名伪装反代
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
│   ├── Dockerfile       #    面板多阶段构建（推 ghcr.io）
│   ├── dns_dynukey.sh   #    acme.sh Dynu DNS-01 钩子（API Key）
│   ├── ansgo-cert-issue.sh  # 步骤 1-3：装 acme.sh + 签发证书（A/B 双保险）
│   ├── ansgo-cert-reload   #    续期后按需 reload 三服务
│   ├── ansgo-genconf       #    从 config + secrets 重新生成服务配置
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
