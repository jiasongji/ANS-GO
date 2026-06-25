# 节点信息命名与 Docker 手动证书同步设计

日期：2026-06-25

## 背景

本次整改包含三类变更：

1. 面板标题与“网页标题”设置项语义调整。
2. 节点 URI `#fragment` 从固定 `ANS-GO-*` 改为基于用户自定义节点信息的短名称。
3. Docker 手动证书模式改为面向宿主真实路径，重点支持宝塔面板 + Nginx 已自行维护证书续期的场景，并提供可复制的同步脚本与计划任务。

设计目标是最小化风险：不向面板容器暴露 Docker socket，不动态挂载任意宿主路径，不要求容器直接读取宝塔/Nginx 证书目录。Docker 服务始终使用容器内 `/etc/ssl/ansgo/` 的证书副本。

## 目标

- 用户在“面板设置”中看到的字段从“网页标题”改为“节点信息”。
- 浏览器标题自动追加 `_ANS`：
  - 用户填写 `NodeName` → `NodeName_ANS`
  - 未设置或旧默认值 `ANS-GO 管理面板` → `Manage_ANS`
- 主服务 URI 名称改为 `<节点信息>-<服务简称>`：
  - AnyTLS → `AT`
  - NaiveProxy → `NV`
  - Shadowsocks → `SS`
  - SOCKS5 → `SK`
- 落地服务 URI 名称也统一脱离旧 `ANS-GO-Landing-*`，使用 `<节点信息>-LD-<落地名或ID>`。
- Docker 手动证书页允许用户填写宿主机真实证书路径，但不直接动态挂载该路径；改为生成同步脚本。
- Docker 手动证书页提供两种折叠脚本方案，默认隐藏，展开后可一键复制：
  1. 系统自动任务一键安装命令。
  2. 宝塔面板计划任务脚本。
- 新增容器内同步脚本，让 Docker manual 模式的“同步并重新加载证书”具备真实同步能力。
- 升级脚本确保旧 Docker 部署可获得固定同步目录、compose 挂载和新脚本。

## 非目标

- 不在 Web 面板容器内挂载 Docker socket。
- 不允许面板直接修改宿主 Docker compose 后动态挂载任意路径。
- 不默认启用 inotify 实时同步。
- 不让 caddy/sing-box/面板直接使用宿主证书路径。
- 不尝试接管宝塔、Nginx、certbot 或其他 ACME 客户端的证书签发/续期流程。

## 设计一：节点信息与标题

### 字段兼容

继续复用 `panel_title` 字段，避免配置迁移风险。UI 展示名改为“节点信息”。后端保存空值时保持现有行为：写回或兜底为 `ANS-GO 管理面板`。

为了满足新展示语义，新增展示 helper：

- `nodeBaseName(c)`：返回节点基础名。
  - `panel_title` 为空或等于旧默认 `ANS-GO 管理面板` → `Manage`
  - 其他值 → 去空白后的用户值
- `panelDisplayTitle(c)`：返回浏览器标题。
  - `nodeBaseName(c) + "_ANS"`
- `nodeFragment(c, suffix)`：返回 URL fragment 安全名称。
  - 基础名和 suffix 之间使用 `-`
  - 对空白、`#`、`?`、`/` 等 fragment 风险字符做替换或转义

### 浏览器标题

`rootHandler` 渲染首页时替换 `<title>` 为 `panelDisplayTitle(c)`。

前端保存设置成功后，`document.title` 也同步使用同样规则，避免保存后需刷新才看到新标题。

### URI fragment

`buildURIs` 改为使用统一 helper：

- Shadowsocks：`#<base>-SS`
- AnyTLS：`#<base>-AT`
- SOCKS5：`#<base>-SK`
- NaiveProxy：`#<base>-NV`
- 落地服务：`#<base>-LD-<landing-name-or-id>`

示例：用户节点信息为 `NodeName` 时：

- `NodeName-AT`
- `NodeName-NV`
- `NodeName-SS`
- `NodeName-SK`
- `NodeName-LD-landing1`

用户未设置或仍为旧默认值 `ANS-GO 管理面板` 时：

- 浏览器标题：`Manage_ANS`
- URI：`Manage-AT`、`Manage-NV`、`Manage-SS`、`Manage-SK`

## 设计二：Docker 手动证书同步模型

### 核心模型

Docker manual 证书采用三段式路径：

```text
宿主真实证书路径（宝塔/Nginx/用户 ACME 维护）
  -> 宿主固定同步目录 /etc/ansgo-docker/manual-certs/
  -> 容器固定导入目录 /host/manual-certs/
  -> 容器运行证书副本 /etc/ssl/ansgo/
```

服务实际使用路径固定为：

```text
/etc/ssl/ansgo/fullchain.pem
/etc/ssl/ansgo/privkey.pem
```

### Docker compose 固定挂载

Docker compose 增加固定只读挂载：

```yaml
- /etc/ansgo-docker/manual-certs:/host/manual-certs:ro
```

安装和升级都要确保宿主目录存在：

```bash
mkdir -p /etc/ansgo-docker/manual-certs
chmod 700 /etc/ansgo-docker/manual-certs
```

该目录仅用于 ANS-GO Docker 手动证书导入，不要求用户把宝塔/Nginx 证书原目录直接挂给容器。

### 配置字段

保留运行时字段：

- `cert_mode`
- `cert_fullchain`
- `cert_privkey`

新增 Docker 宿主源路径元数据字段：

- `cert_host_fullchain`
- `cert_host_privkey`

语义：

- 裸金属 manual：`cert_fullchain` / `cert_privkey` 是实际运行路径。
- Docker manual：
  - `cert_host_fullchain` / `cert_host_privkey` 是用户填写的宿主真实路径，用于生成同步命令。
  - `cert_fullchain` / `cert_privkey` 保持为 `/etc/ssl/ansgo/fullchain.pem` 和 `/etc/ssl/ansgo/privkey.pem`。

旧 Docker manual 部署如果 `ansgo.env` 中仍保留 `CERT_FULLCHAIN` / `CERT_PRIVKEY`，升级或 entrypoint 可迁移到 `cert_host_*`，但不强猜缺失数据。缺失时前端提示用户重新填写宿主源路径。

## 设计三：同步脚本

### 容器内脚本：`ansgo-sync-manual-cert`

新增脚本安装到镜像内：

```text
/usr/local/bin/ansgo-sync-manual-cert
```

输入固定为：

```text
/host/manual-certs/fullchain.pem
/host/manual-certs/privkey.pem
```

行为：

1. 确认当前为 manual 模式或允许在 manual 模式下执行。
2. 检查输入文件存在且非空。
3. 使用 `openssl x509` 校验证书格式。
4. 使用 `openssl pkey` 校验私钥格式。
5. 校验证书和私钥匹配。
6. 与 `/etc/ssl/ansgo/` 当前证书比较；无变化则输出“未变化”并退出 0。
7. 有变化时复制到临时文件。
8. 原子替换 `/etc/ssl/ansgo/fullchain.pem` 与 `/etc/ssl/ansgo/privkey.pem`。
9. 设置权限：证书 `0644`，私钥先使用当前兼容策略，必要时保持容器内服务可读。
10. 确保 `panel.json` 中 manual 运行路径为 `/etc/ssl/ansgo/fullchain.pem` 与 `/etc/ssl/ansgo/privkey.pem`。
11. 返回清晰日志。

脚本本身只同步，不强制 reload。这样它可以被按钮、cron、宝塔计划任务组合调用：

```bash
docker exec ansgo ansgo-sync-manual-cert && docker exec ansgo ansgo-cert-reload
```

### 宿主同步脚本模板

面板根据用户填写的宿主路径生成宿主同步脚本内容。脚本做：

1. 从宝塔/Nginx/用户 ACME 路径复制到 `/etc/ansgo-docker/manual-certs/`。
2. 使用临时文件 + `mv` 原子替换。
3. 调用容器内 `ansgo-sync-manual-cert`。
4. 有变化时再 reload；若容器脚本实现无法明确区分变化，先简单调用 reload，后续可优化为脚本输出状态码。

为了安全，脚本中所有用户路径都必须 shell quote，不能直接拼接未转义字符串。

## 设计四：证书管理页 UI

### Docker manual 文案

Docker manual 模式下文案改为：

> Docker 部署请填写宿主机上的证书/私钥真实路径，例如宝塔面板或 Nginx 正在使用并自动续期的证书路径。ANS-GO 不会动态挂载任意宿主目录，而是通过固定同步目录 `/etc/ansgo-docker/manual-certs/` 导入证书，服务实际使用容器内 `/etc/ssl/ansgo/` 副本。

显示两类路径：

- 宿主源路径：用户填写。
- 容器运行路径：只读展示 `/etc/ssl/ansgo/fullchain.pem` 与 `/etc/ssl/ansgo/privkey.pem`。

### 折叠脚本区

Docker manual 模式下，保存路径后显示“同步方案”区域。默认只显示两个按钮/标题，不展开脚本内容：

1. `方案一：系统自动任务一键安装`
2. `方案二：宝塔计划任务脚本`

用户点击对应方案后展开详情。每个详情区包含：

- 简短说明。
- 代码块。
- `📋 复制` 按钮。

默认折叠隐藏，避免证书管理页过长。

### 方案一：系统自动任务一键安装

生成一条适合 SSH 执行的一键命令，完成：

1. 写入 `/usr/local/bin/ansgo-docker-sync-manual-cert`。
2. `chmod +x`。
3. 写入 `/etc/cron.d/ansgo-docker-manual-cert-sync`。
4. 立即执行一次同步。

周期默认建议 15 分钟或 1 小时。文案说明：证书通常 60-90 天续一次，15 分钟不是为了高频续期，而是为了续期发生后能较快同步；用户可自行改为每小时或每天。

### 方案二：宝塔计划任务脚本

生成适合复制到宝塔“计划任务 → Shell 脚本”的内容。建议用户设置：

- 任务类型：Shell 脚本
- 执行周期：每 15 分钟 / 每小时 / 每天凌晨

脚本内容与系统自动任务内部核心逻辑一致，但不创建 cron 文件。

### 同步并重新加载按钮

Docker manual 模式下，“重新加载证书”按钮改名为：

```text
🔄 同步并重新加载证书
```

行为：

1. 调用 `ansgo-sync-manual-cert`。
2. 若同步成功，再调用 `ansgo-cert-reload`。
3. 若 `/host/manual-certs/` 尚无证书，提示用户先执行系统自动任务一键命令，或把脚本加入宝塔计划任务。

裸金属 manual 模式保留现有“重新加载证书”语义。

## 设计五：安装与升级

### install.sh

Docker 部署时：

- 创建 `/etc/ansgo-docker/manual-certs`。
- compose 默认加入固定只读挂载。
- manual 模式首次部署时，如果用户通过 CLI 提供 `--cert-fullchain` / `--cert-privkey`：
  - 可直接把这两个文件复制到 `/etc/ansgo-docker/manual-certs/`。
  - 将宿主源路径写入 env 或 panel 初始配置，供前端显示。
  - 不再需要为 cert/key 原目录动态注入 bind mount。

### docker-compose.yml

模板加入固定挂载：

```yaml
- /etc/ansgo-docker/manual-certs:/host/manual-certs:ro
```

### Dockerfile.allinone

复制新增脚本：

```text
ansgo-sync-manual-cert -> /usr/local/bin/ansgo-sync-manual-cert
```

### entrypoint.sh

Docker manual 模式启动时：

- 优先从 `/host/manual-certs/` 同步。
- 兼容旧部署：如果 env 中 `CERT_FULLCHAIN` / `CERT_PRIVKEY` 可读，则可先复制到 `/etc/ansgo-docker/manual-certs/` 对应挂载源或直接同步到 `/etc/ssl/ansgo/`。
- 将运行路径保持为 `/etc/ssl/ansgo/`。
- 迁移 `cert_host_*` 元数据，能确定时写入；不能确定时留空并由前端提示。

### upgrade.sh

Docker 分支升级时：

1. 备份 `/etc/ansgo-docker` 与 volumes。
2. 创建 `/etc/ansgo-docker/manual-certs`。
3. 确保 compose 固定挂载存在，避免重复添加。
4. 保留 `ansgo.env`。
5. `docker compose pull` 后 `up -d --force-recreate`。
6. 升级说明中明确：Docker manual 证书用户需在面板查看同步方案，或设置宝塔计划任务。

## 风险与缓解

### 风险：用户误以为 ANS-GO 会接管宝塔证书续期

缓解：文案明确“证书签发/续期仍由宝塔/Nginx/用户 ACME 工具负责，ANS-GO 只同步副本”。

### 风险：用户填写路径后没有设置计划任务

缓解：面板显示黄色提示；“同步并重新加载证书”按钮在未发现 `/host/manual-certs/` 文件时提示执行一键命令或宝塔计划任务。

### 风险：定时任务复制到半写入证书

缓解：容器同步脚本必须校验证书格式与证书/私钥匹配；失败不覆盖旧证书。

### 风险：频繁 reload 造成服务闪断

缓解：同步脚本先比较新旧文件，无变化不 reload。宿主脚本或按钮按容器脚本结果决定是否调用 reload。

### 风险：私钥权限

缓解：宿主同步目录权限 `700`；私钥复制到宿主同步目录用 `0600`；容器内权限需兼容 caddy/sing-box/panel，若服务均以 root 运行，可使用 `0600`，否则保持当前兼容策略并在后续单独收紧。

### 风险：路径含特殊字符导致脚本注入

缓解：前端展示脚本时对 shell 参数做安全单引号转义；后端生成脚本时也使用 quote helper，严禁原样拼接。

## 测试计划

### Go 单元测试

- `nodeBaseName`：空值、旧默认、用户自定义。
- `panelDisplayTitle`：`Manage_ANS`、`NodeName_ANS`。
- `buildURIs`：四个主服务 fragment 使用 `AT/NV/SS/SK`。
- 落地服务 fragment 使用 `LD` 前缀且特殊字符被处理。
- Docker manual cert config GET/POST 返回宿主源路径与运行路径语义正确。

### Shell 测试

- `ansgo-sync-manual-cert`：
  - 输入缺失时报错且不覆盖旧证书。
  - 无变化时退出成功并标记无变化。
  - 证书/私钥不匹配时报错且不覆盖旧证书。
  - 有效证书时原子替换。
- `install.sh` / `upgrade.sh`：
  - compose 固定挂载幂等添加。
  - Docker manual cert CLI 初始部署可把宿主证书复制到同步目录。

### 前端检查

- `node --check` 检查嵌入脚本语法。
- 证书管理页 Docker manual 模式两个同步方案默认折叠。
- 展开后脚本可复制。
- 保存设置后标题更新为 `*_ANS`。

### 发布前检查

- 敏感信息 grep：真实 IP、真实域名、真实面板路径、私钥名、真实时间戳不得进入 git。
- 子代理审计：至少从前端/后端、Docker shell/upgrade 两个角度检查。
- 构建 amd64/arm64 面板二进制。
- 更新版本号到新版本。
- 创建 GitHub Release 并上传资产。
- 重建并推送 ghcr.io 镜像 `latest` 与版本 tag。
- 确认 `deploy/upgrade.sh` 指向新版本，Docker 分支可通过一键升级获得新镜像与固定同步目录支持。
