# ANS-GO v1.5.37 Release Notes（开发中 / 待发布）

> 🚧 **状态：开发中，尚未发布。** 截至 v1.5.37 发版前，最新已发布版本为 **v1.5.36**。本文描述的均为**已实现、待发布**的最终行为（与当前源码一致），但**尚未创建 release / 上传资产 / 推送镜像 / 部署服务器**，发版以 GitHub Releases 实际标签与产物（release + 6 资产 + ghcr.io 镜像）为准，不得提前按已发布口径引用。本文不含任何真实 IP / 域名 / 凭证。

## 变更清单

### 1. 公网 IPv4 自动获取（`server_ip` 自动回填）

- **触发条件**：`server_ip` 为空（新安装 / 升级后的普通启动即命中）
- **行为**：面板启动后**异步**发起**一次**公网 IPv4 查询（第三方 echo 服务：ipify / ifconfig.me / icanhazip 三选一兜底），成功则写入 `server_ip` 持久化
- **探测安全边界（最终实现）**：
  - **仅限公网 IPv4**：启动自动探测强制 `tcp4` 拨号（从拨号层保证 IPv4，而非只过滤响应），且只接受 IPv4 响应
  - **直连出网**：HTTP 客户端 `Proxy=nil`，忽略 `HTTP(S)_PROXY` 等环境代理变量，测得本机真实公网**出口** IPv4
  - **不跟随重定向**：echo 端点不该重定向，3xx 按非 200 拒绝（防被劫持端点把探测导向任意目标）
  - **响应限长**：响应体最多读 64 字节，超长视为异常响应拒绝（防被劫持端点倾倒数据）
  - **拒绝内网 / 保留 / 非法地址**：`isPrivateIP()` 覆盖 RFC1918 / CGNAT(100.64.0.0/10) / ULA(fc00::/7) / 回环 / 链路本地 / 未指定(0.0.0.0、::) / 组播 / IPv4 保留段 240.0.0.0/4（含广播），非法 IP 文本一律拒绝
  - **总超时 18s**：一次完整探测（3 端点 × 单端点 6s 串行）整体 18s 上限；调用方 ctx 已有 deadline 时以 deadline 为准
  - **原子落盘**：探测结果经配置锁内复查 `server_ip` 仍为空才写入（tmp+rename 原子替换）；探测期间用户已手动保存则放弃写入
- **边界**：
  - 已设置（`--server-ip`、面板手动保存）**不覆盖**
  - 查询失败不阻塞面板启动，保持为空等待下次启动或手动检测补齐
- 「面板设置 → 🔍 自动检测」保留为**手动候选入口**：与启动探测共用同一套校验（直连 / 限长 / 内网拒绝 / 重定向拒绝），但不强制 IPv4（可返回 IPv6 候选）；结果填入输入框，需用户点「保存」才生效，不自动覆盖
- **隐私规则更新**：v1.5.18「公网 IP 查询仅用户手动触发、启动不外发」的约束由本版取代——自动查询仅发生在 `server_ip` 为空时、成功落盘后不再外发，频次有上限且填显式 `--server-ip` 可彻底关闭。v1.5.18 历史记录保留于 README 版本历史与 AGENTS.md 版本演进摘要，不回改

### 2. 新增安装参数 `--server-ip` / `--panel-title`（裸金属 / Docker 通用）

- `--server-ip <IP>`：显式指定公网**入口** IP，优先级最高；填写后 `server_ip` 非空 → 启动自动查询不触发。VPC/NAT（公网出口 ≠ 入口）场景必须显式填写真实入口 IP
  - **严格校验**：IPv4 / IPv6 均可（优先 python3 `ipaddress`；该校验先于依赖安装执行，无 python3 环境走 shell 结构化兜底，依赖段就绪后再用 ipaddress 复核一次，非法即中止安装）
  - 填 IPv6 时节点 URI host 自动加 `[]`（见 §3）
- `--panel-title <名称>`：安装时设置「节点信息」基名，驱动浏览器标题（`<基名>_ANS`）与节点 URI fragment（`<基名>-<简称>`）；留空沿用默认规则（回退 `Manage_ANS`）
  - **特殊字符安全传输（最终实现）**：仅限制 trim 后长度 1-64、拒绝控制字符与纯空白；其余常见特殊字符（`$` `#` 引号 空格 中文 `%` 等）均可——裸金属侧 JSON 字符串转义（`ansgo_json_escape`）后写入 panel.json；Docker 侧经 `PANEL_TITLE_ENCODED` percent-encoding 透传 ansgo.env（规避 compose v1/v2 dotenv 对 `$` 插值与 `#` 行内注释的解析差异），entrypoint 解码还原可读值；已用真实 `docker compose config` 验证两种解析器下还原一致

### 3. 节点 URI 连接地址调整（出口/入口语义区分）

- **AnyTLS / 落地 AnyTLS / Shadowsocks / SOCKS5**：连接地址与 URI host **优先用 IP**；**SNI 行 / 参数仍为域名**（TLS 证书校验不受影响）
- **IPv6 连接地址自动加括号**：URI authority 中的 IPv6 主机按 RFC 3986 写成 `[hextext]`（`bracketIPv6Host()` 统一处理，IPv4 / 域名原样），避免客户端把冒号误解析为端口分隔符
- **NaiveProxy**：连接地址与 URI **保留域名**（伪装架构下域名即入口）
- IP 缺失（未设置且探测失败）时全部回退域名，与 v1.5.18~v1.5.36「连接地址 IP + URI 域名」行为兼容
- 语义说明：自动查询 / UDP 探测拿到的是公网**出口** IP；节点连接地址是**入口**。简单 VPS 上二者相同，NAT / VPC / 中转下可能不同——因此显式 `--server-ip` 优先级最高，自动值只做兜底

### 4. Dynu 凭证：API Key 主引导 + 缺凭证明确中止

- 安装与面板「证书管理」引导以 **API Key（路径 A）** 为主
- OAuth Client ID + Secret（路径 B）继续兼容：已配置的旧部署照常工作；路径 A 签发失败仍自动降级路径 B
- **新装 acme 模式缺凭证明确中止（避免无证书 crash）**：裸金属没有自签占位证书路径，无凭证签发必败且面板 TLS 起不来。因此新安装（裸金属 / Docker、交互 / `--non-interactive` / CLI 半对统一拦截）时，若无 API Key 且 OAuth 不完整（只填 Client ID 或只填 Secret）或完全缺失，install.sh **直接报错中止**（exit 2），提示改用 `--dynu-key`、补齐 OAuth 完整对或改用 `--cert-mode manual`；交互模式 API Key 必填引导（推荐），OAuth 仅作完整对备选（CLI `--dynu-client-id`/`--dynu-secret` 兼容保留）
- 面板「证书管理」acme 凭证区**精简为 API Key + 注册邮箱**：OAuth 输入框移除，仅显示「OAuth 凭证兼容模式」状态与迁移提示（建议补填 API Key 完成迁移，旧 OAuth 凭证不删除）；不提交的字段保留旧值，旧部署续期不受影响
- 面板凭证回显仍只显示「已配置 / 未配置」，不回传明文

### 5. Docker manual 证书同步脚本显隐 + cert 切换防误操作

- 两套可复制同步脚本（「系统自动任务一键安装」/「宝塔计划任务脚本」）仅在 **Docker + manual** 证书模式下显示；非 Docker 部署不渲染
- 证书来源下拉**切换但未保存**时：当前模式的「签发 / 续期 / 重新加载」等操作按钮**全部隐藏**，并显示「需先保存来源设置」提示——不允许执行与当前选择不符的旧模式操作；保存成功后按最新已保存模式重渲染（同步脚本区也随下拉即时显隐，无需保存）

### 6. 安装初始化加固：panel.json 原子写 + 失败中止

- panel.json **已存在一律不覆盖**（裸金属 / Docker 卷内数据优先，保留用户改过的端口 / 标题 / IP 等）
- **首次生成原子写**（裸金属 `metal_ensure_panel_json` / Docker `entrypoint_ensure_panel_json` 同一语义）：同目录临时文件 → python3 JSON 合法性校验 → `chmod 600` → `mv` 原子替换；任一步失败清理临时文件并返回明确错误，绝不落地半成品 / 坏 JSON
- **失败即中止**：裸金属生成失败 install.sh 以 exit 3 中止安装；Docker entrypoint 生成失败以 exit 1 中止容器初始化（不带病拉起 systemd）

### 7. 面板 `server_ip` 表单保存语义（防丢自动探测值）

- 设置页加载时记录 `server_ip` 输入框初值；保存时「**初值为空且未编辑**」则 payload **不提交该字段**——后端按字段不存在**完全不动**该值，防止空表单回显把启动自动探测保存的 IP 抹掉
- 用户手动输入 / 「🔍 自动检测」填入（值变化）照常提交；「**显式清空**」（原值非空 → 清成空）提交空串 → 后端清空并回退自动探测 / 域名
- 后端对非空提交值校验 IPv4 / IPv6 格式并拒绝回环 / 链路本地地址
- 设置保存写回走**配置锁内重放**（以最新内存配置为 base 只覆盖设置页字段）：请求处理期间并发落盘的自动探测 server_ip / 落地 / 端口 / 证书变更不会被旧快照抹掉

## 升级与兼容

- 升级路径不变：`upgrade.sh` 自动识别裸金属 / Docker；本版不新增必填配置字段
- 已设置 `server_ip` 的部署升级后行为不变（不覆盖、不触发自动查询）
- `panel.json` 无破坏性字段变更；服务 / 落地 / 证书配置全部向后兼容；旧 OAuth Dynu 凭证部署的签发 / 续期不受影响

## 本地验证状态（截至本文最后更新，开发机执行；不含发版产物验证）

- Go 回归测试（`deploy/panel`）：全部通过（含启动探测 / 原子持久化 / 并发竞态、URI IPv6 括号、设置保存字段语义、落地与标题回归等）
- `scripts/test-install-params.sh`：105/105 通过（参数校验 / acme 缺凭证中止语义 / panel.json 生成函数 / ansgo.env 特殊字符跨两种 dotenv 解析器还原）
- `scripts/test-entrypoint-panel-json.sh`：12/12 通过（原子写 / JSON 校验拦截 / 失败中止容器初始化 / install→entrypoint 全链路标题还原）
- `scripts/test-panel-ui.mjs`：39/39 通过（cert 模式切换显隐与未保存操作隐藏 / server_ip 初值语义等）
- 发版产物（release 6 资产 / ghcr 镜像 / 服务器升级验证）**尚未执行**——见下方发版前检查

## 发版前检查（供执行者）

- [ ] `git tag v1.5.37 && git push --tags` 触发 CI（ghcr.io 镜像 + 6 资产自动上传）
- [ ] `gh release view v1.5.37 --json assets --jq '[.assets[].name]'` 确认 6 项资产齐全
- [ ] 行为验证：空 `server_ip` 启动自动回填一次（公网 IPv4）/ 已设不覆盖 / `--server-ip` 优先且跳过查询（IPv4/IPv6 校验、非法中止）/ 查询失败不阻塞 / 新装 acme 缺 API Key + OAuth 半对被中止 / AT·落地 AT·SS·SOCKS URI 优先 IP（IPv6 自动加括号）且 SNI 为域名 / Naive URI 保留域名 / 含特殊字符标题裸金属 + Docker 均正确还原 / Docker manual 同步脚本随 cert_mode 切换即时显隐、未保存切换时旧操作隐藏 / server_ip 空初值未编辑保存不抹掉自动值、显式清空仍生效
- [ ] Release notes 发布时核对并移除「开发中 / 待发布」标注
