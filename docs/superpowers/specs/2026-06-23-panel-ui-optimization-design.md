# 面板 UI 优化设计（2026-06-23）

## 背景
v1.5.17 之后用户提出 4 处面板优化诉求：
1. 节点信息「连接地址」显示**服务器 IP** 而非域名
2. 把「服务控制」与「服务管理」合并为**一个服务管理菜单**
3. 「🎲 随机」按钮从卡底汇总，挪到**每个输入框右侧**
4. 每张服务卡的操作按钮**集中到一行**：`应用 → 启停 → 重启 → 装卸 → 检测`，按可用性自适应

## 设计决策（已与用户确认）
| 决策点 | 选择 |
|---|---|
| 服务器 IP 来源 | Go 端 `net.Dial("udp","8.8.8.8:80")` 取 LocalAddr，进程级缓存，失败回退域名 |
| 「服务控制」菜单 | 删除导航按钮（`loadSvc` 函数保留作兼容入口） |
| 🎲 随机按钮 | 移到每个输入框右侧，**纯前端生成不自动保存**，移除卡底汇总 |
| 「应用」语义 | **整合为「保存全部」**：一次保存该卡端口 + 密钥（串行调 `portHandler` + `keyHandler`，不新增端点） |
| 操作行规则 | 未安装 `[📥安装]`；已安装 `[💾应用][▶️启动/⏹停止][🔄重启][📤卸载][🔍检测]` |

## 详细改动

### A. 后端 `deploy/panel/handlers.go`

#### A.1 服务器 IP 探测（新增）
```go
var (
    serverIPCache string
    serverIPOOnce sync.Once
)
// cachedServerIP 一次性探测本机公网出口 IPv4。
// 用 UDP "连接" 8.8.8.8:80 触发路由表选出口，不真正发包；
// 进程级缓存（容器/主机运行期固定），失败返回 ""（前端回退域名）。
func cachedServerIP() string {
    serverIPOOnce.Do(func() {
        conn, err := net.Dial("udp", "8.8.8.8:80")
        if err != nil {
            return
        }
        defer conn.Close()
        if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
            ip := a.IP.String()
            if ip != "" && !strings.HasPrefix(ip, "127.") {
                serverIPCache = ip
            }
        }
    })
    return serverIPCache
}
```
import 新增 `"sync"`。

#### A.2 `nodeHandler` 增加 `server_ip` 字段
```go
resp := map[string]any{
    "domain":         c.Domain,
    "server_ip":      cachedServerIP(),   // 新增
    "ss": ..., "anytls": ..., ...,
}
```

> 不新增 `/api/svc-save`：复用现有 `portHandler`（端口 + 冲突校验 + 重启）和 `keyHandler`（密钥 + 长度校验 + 异步重启）即可覆盖「保存全部」语义，避免重复实现校验。

### B. 前端 `deploy/panel/web/index.html`

#### B.1 删除「服务控制」导航
移除：
```html
<button data-t="svc" onclick="showTab('svc')"><span class="ico">🎛️</span><span class="label">服务控制</span></button>
```
`loadSvc` 函数代码保留（向后兼容、`showTab` 路由表里仍存在，但无导航入口）。

#### B.2 节点信息连接地址（`loadNode`）
```js
// 顶部取
const serverIp = j.server_ip || '';
// card() 内
${row('连接地址', serverIp || n.host || domain)}
```
> URI 字段不变（TLS 协议 SNI 需域名），仅展示行换 IP。

#### B.3 新增前端工具函数（随机生成，不保存）
```js
// base64(16字节)，与 openssl rand -base64 16 等价（SS2022 密钥格式）
function randB64Bytes(n){
  const b=new Uint8Array(n);
  crypto.getRandomValues(b);
  return btoa(String.fromCharCode(...b));
}
// 可读随机串：默认大小写字母+数字
function randStr(len, alphabet='abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789'){
  const b=new Uint8Array(len);crypto.getRandomValues(b);
  let s='';for(let i=0;i<len;i++)s+=alphabet[b[i]%alphabet.length];
  return s;
}
// 按 input id 生成并填回（不保存）
function genField(id){
  let v='';
  switch(id){
    case 'k_ss_key':       v=randB64Bytes(16);break;                 // base64(16)
    case 'k_at_pass':
    case 'k_at2_pass':     v=randStr(20);break;                       // 20位混合
    case 'k_sk_pass':
    case 'k_nv_pass':      v=randStr(18);break;                       // 18位混合
    case 'k_sk_user':
    case 'k_nv_user':      v=randStr(8,'abcdefghijklmnopqrstuvwxyz0123456789');break; // 8位小写
    default: return;  // 加密方式等不随机
  }
  const el=document.getElementById(id);
  if(el){el.value=v;el.focus();toast('已生成，需点「应用」保存')}
}
```

#### B.4 `loadMgmt` 重构：输入框 + 🎲 同行 / 操作行一行自适应
每张服务卡结构变为：
```
[端口]    [input]                          ← 端口单独一行（应用按钮挪到操作行）
[加密]    [input]                          ← 仅 SS，不加 🎲
[密钥]    [input] [🎲]                     ← 每个密钥/用户名/密码框带 🎲
─────────────────────────────────
操作行（flex-wrap 自适应换行）：
  未安装：        [📥 安装]
  已安装运行中：  [💾 应用] [⏹ 停止] [🔄 重启] [📤 卸载] [🔍 检测]
  已安装未运行：  [💾 应用] [▶️ 启动] [🔄 重启] [📤 卸载] [🔍 检测]
检测结果框（在按钮行正下方展开）
```

新增 JS：
```js
// 保存全部：按字段顺序串行调 portHandler + keyHandler
async function svcSave(target){
  if(!confirm(`确认保存 ${target} 的所有改动？\n对应服务将重启，现有连接会断开。`))return;
  // 1. 端口（若有改动）
  const portEl=document.getElementById('p_'+target);
  if(portEl){
    const v=parseInt(portEl.value);
    if(v && v>=1 && v<=65535){
      const pr=await api('api/port',{method:'POST',body:JSON.stringify({target,port:v})});
      if(pr.error){toast('端口保存失败: '+pr.error);return}
    }
  }
  // 2. 密钥（按 target 拼字段）
  let body={target};
  if(target==='ss'){body.method=$('#k_ss_method').value;body.key=$('#k_ss_key').value}
  else if(target==='anytls'){body.pass=$('#k_at_pass').value}
  else if(target==='socks'){body.user=$('#k_sk_user').value;body.pass=$('#k_sk_pass').value}
  else if(target==='naive'){body.user=$('#k_nv_user').value;body.pass=$('#k_nv_pass').value}
  const kr=await api('api/key',{method:'POST',body:JSON.stringify(body)});
  if(kr.error){toast('密钥保存失败: '+kr.error);return}
  toast('已保存，服务正在重启');
  setTimeout(reloadCurrentTab,800);
}
```

「💾 应用」按钮 = `onclick="svcSave('ss')"`（每卡对应自己的 target）。

> **面板端口特殊**：保留独立「应用并重启」按钮（会断会话，走 overlay 提示流程，不并入 `svcSave`）。
> **落地页 AnyTLS-2 密码框**：加 🎲（生成不保存），操作按钮仍调 `saveKey('anytls2')`（无启停/装卸概念，由 group2 开关控制）。

#### B.5 CSS 微调
- 输入框行：`<div class="f">` 容器改 flex，`input{flex:1}` + `button{flex-shrink:0;margin-left:6px}`
- 操作行容器：`<div style="display:flex;flex-wrap:wrap;gap:8px;align-items:center">`
- 移动端窄屏自动换行（`@media(max-width:560px)` 已有 `.row{flex-wrap:wrap}` 基础）

## 受影响文件
| 文件 | 改动 |
|---|---|
| `deploy/panel/handlers.go` | + `cachedServerIP` + `sync` import / `nodeHandler` 加 `server_ip` 字段 |
| `deploy/panel/web/index.html` | 删导航按钮 / `loadNode` 连接地址 / `loadMgmt` 重构 / 新增 `randB64Bytes` `randStr` `genField` `svcSave` |

**零新增 API 端点**（复用 `portHandler` + `keyHandler`），最大程度降低回归面。

## 验证计划
1. `node --check` 抽取的 `<script>` 块，确认无 JS 语法错误
2. `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` 交叉编译，确认 Go 编译通过
3. 提交前 §13 安全自检：全仓 grep 真实 IP/域名/路径 0 命中
4. 部署后人工冒烟：① 节点页连接地址显示 IP ② 服务管理卡按钮一行 + 🎲 单字段生成 ③ 应用保存端口+密钥生效 ④ 删导航后服务管理是唯一入口

## 回滚
- 备份：`ansgo-admin backup` → `/etc/ansgo-backup-{ts}/`
- 二进制：`cp /etc/ansgo-backup-update-vX.Y.Z-{ts}/ansgo-panel.old /usr/local/bin/ansgo-panel`
- 文档：`git revert` 单次提交

## 不在本次范围
- 不改落地服务页主结构（仅 AnyTLS-2 密码框加 🎲）
- 不改后端校验/认证/会话逻辑
- 不改协议配置（sing-box / caddy config.json 生成）
- 不动 install.sh / upgrade.sh
