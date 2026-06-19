# ANS-GO

> 在一台低配 LXC VPS 上部署 **NaiveProxy + AnyTLS + Shadowsocks** 三协议代理 + **Go Web 管理面板**，共享一张 Let's Encrypt 证书。可审计、可回滚、可离线管理（SSH 兜底）。

![status](https://img.shields.io/badge/status-已部署验证-brightgreen) ![license](https://img.shields.io/badge/license-MIT-blue) ![stack](https://img.shields.io/badge/stack-Go%20%7C%20bash%20%7C%20sing--box%20%7C%20caddy-orange)

---

## ✨ 特性

- **三协议**：NaiveProxy（caddy forwardproxy-naive 分支）/ AnyTLS / Shadowsocks-2022（sing-box 双 inbound）
- **一张证书共享**：acme.sh + Dynu DNS-01 签发 ECDSA 证书，caddy / sing-box / 面板三服务共享，续期一次三服务一起 reload
- **Web 管理面板**：Go 单二进制（运行内存 ~12MB），中文 UI，管全部协议参数 / 端口 / 证书 / 自身配置，含客户端二维码
- **完全解耦**：caddy / sing-box / 面板是三个独立进程、端口、systemd unit，**改协议端口永远不会断面板**
- **离线兜底**：`bv-admin` bash 脚本零依赖，面板全挂也能 SSH 管理一切（含密码/路径/端口重置、备份回滚）
- **安全**：管理员密码 bcrypt、按 IP 登录锁定、8 小时会话、随机 URL 路径、全程 TLS

## 🏗 架构

```
            Let's Encrypt (acme.sh + Dynu DNS-01)  路径A: API Key（默认）/ B: OAuth（降级）
                        │ 签发 / 自动续期 + reload
                        ▼
          /etc/ssl/bv-la/{fullchain,privkey}.pem     一张证书三服务共享
                        │
      ┌─────────────────┼─────────────────────┐
      ▼                 ▼                     ▼
 caddy :443       sing-box :8443        面板(Go) :15608
 NaiveProxy       AnyTLS + Shadowsocks   Web 管理面板
      ▲                 ▲                     ▲
      └─────── bv-admin (bash) ───────────────┘   SSH 离线兜底
```

## 📁 仓库结构

```
.
├── AGENTS.md            # ⭐ 唯一事实来源（方案 / 架构 / 部署顺序 / 约束）
├── deploy/              # 可复现的全部部署产物
│   ├── README.md        #    部署 / 复现指南
│   ├── dns_dynukey.sh   #    acme.sh Dynu DNS-01 钩子（API Key）
│   ├── bv-deploy-cert.sh#    步骤 1-3：装 acme.sh + 签发证书（A/B 双保险）
│   ├── bv-cert-reload   #    续期后按需 reload 三服务
│   ├── bv-genconf       #    从 config + secrets 重新生成服务配置
│   ├── bv-admin         #    离线管理脚本（status/info/restart/regen/cert/...）
│   ├── bv-panel.service #    面板 systemd unit
│   └── panel/           #    Go 面板源码（main/handlers/crypto.go + web/）
└── .secrets.local       # 🔒 敏感凭证（已 gitignore，不入库）
```

## 🚀 快速开始

部署分两阶段，详见 [`deploy/README.md`](deploy/README.md)：

1. **第一阶段（基础优化）**：清理垃圾包、系统升级、内核网络调优、装 sing-box + caddy（详见 [AGENTS.md §1](AGENTS.md)）
2. **第二阶段（本仓库内容）**：签发证书 → 切换真实证书 → 部署 `bv-admin` → 编译部署 `bv-panel`（AGENTS.md §9 十步）

面板访问示例：
```
https://your.domain:15608/<随机URL路径>/
```

## 📖 文档

| 文档 | 内容 |
|------|------|
| [AGENTS.md](AGENTS.md) | **唯一事实来源**：项目目标、架构、端口、证书方案、三协议配置、面板设计、部署顺序、风险回滚、约束原则 |
| [deploy/README.md](deploy/README.md) | 部署 / 复现指南：文件清单、各步骤命令、配置模板 |

## 🔒 安全声明

- 本仓库**不含任何密钥、密码、域名凭证**。所有敏感值在服务器 `/etc/proxy-secrets.env`、`/etc/bv-panel/config.json`、`/root/.acme.sh/`，本地镜像在 `.secrets.local`（已 gitignore）。
- 部署后请自行验证：客户端连通、面板功能走查、IP 锁定机制、证书真实性。

## 📄 License

MIT — 见 [LICENSE](LICENSE)。
