#!/usr/bin/env node
// =============================================================================
// ANS-GO 面板前端行为测试（v1.5.37，TDD，Node 运行，无浏览器依赖）
//
// 覆盖：
//   1. 证书管理 ACME 区精简：只保留 API Key + 邮箱输入；不渲染/不提交 Client ID/Secret
//   2. 旧 OAuth-only 凭证（has_oauth=true 且无 API Key）显示兼容迁移提示
//   3. 宿主证书同步脚本：仅 Docker 且 manual 模式显示；模式下拉切换即时生效
//   4. 节点信息：NaiveProxy 连接地址仍用域名，其余服务优先 IP；复制/二维码同 URI
//   5. 面板设置：公网 IP 帮助文案说明「首次留空自动探测保存 / 手动覆盖」
//
// 隔离性：提取 index.html 内联 <script> 在最小 DOM stub 中执行；fetch 全部假数据。
// 用法：node scripts/test-panel-ui.mjs
// =============================================================================
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';
import assert from 'node:assert/strict';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');
const html = readFileSync(join(ROOT, 'deploy/panel/web/index.html'), 'utf8');
const m = html.match(/\n<script>\n([\s\S]*?)\n<\/script>/);
assert.ok(m, 'index.html 内联 <script> 提取失败');
const script = m[1];

// ---- 最小 DOM stub ----
function makeEl(sel) {
  return {
    _sel: sel, innerHTML: '', textContent: '', value: '', className: '', title: '',
    style: {}, dataset: {}, disabled: false, onclick: null, onchange: null,
    classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    addEventListener() {}, focus() {}, setAttribute() {}, getAttribute() { return null; },
  };
}
function boot(responses) {
  const els = new Map();
  const posts = []; // 捕获 POST body
  const qr = [];
  const document = {
    querySelector: (s) => { if (!els.has(s)) els.set(s, makeEl(s)); return els.get(s); },
    querySelectorAll: () => [],
    getElementById: (id) => { const k = '#' + id; if (!els.has(k)) els.set(k, makeEl(k)); return els.get(k); },
    documentElement: makeEl('html'),
    addEventListener() {},
  };
  const fetchLog = [];
  const fetchImpl = async (url, opts = {}) => {
    const key = String(url).replace(/^\/+/, '');
    fetchLog.push({ key, method: opts.method || 'GET' });
    if (opts.method === 'POST') {
      posts.push({ key, body: opts.body });
      // 模拟后端保存：POST api/cert/config 的字段合并回 GET 响应（供保存后重渲染断言）；
      // POST 响应本身按真实后端契约返回 {ok:true}
      if (key === 'api/cert/config' && opts.body) {
        try { responses[key] = Object.assign(responses[key] ?? {}, JSON.parse(opts.body)); } catch {}
      }
      return { ok: true, status: 200, text: async () => JSON.stringify({ ok: true }) };
    }
    const resp = responses[key] !== undefined ? responses[key] : {};
    return { ok: true, status: 200, text: async () => JSON.stringify(resp) };
  };
  const ctx = {
    document, fetch: fetchImpl, fetchLog, posts, qr, els,
    window: { addEventListener() {}, matchMedia: () => ({ matches: false }) },
    localStorage: { getItem: () => null, setItem() {} },
    navigator: { clipboard: { writeText: () => Promise.resolve() } },
    location: { reload() {}, href: '' },
    confirm: () => true, alert() {}, prompt: () => null,
    QRCode: class { constructor(el, o) { qr.push(o && o.text); } },
    setTimeout: (fn) => {}, clearTimeout() {}, setInterval() {}, clearInterval() {},
    console, Promise, JSON, Object, Array, String, Number, Boolean, Math, Date, RegExp, Error, isNaN, parseInt, parseFloat, btoa, Uint8Array, crypto: { getRandomValues: (a) => a },
  };
  ctx.globalThis = ctx;
  vm.createContext(ctx);
  vm.runInContext(script, ctx);           // 执行面板脚本（顶层 checkAuth 异步 fire）
  // innerHTML 渲染不会解析出真实元素；__el 惰性创建并复用 stub 元素；
  // __has 仅探测存在性（不创建），用于断言「区块是否被渲染」
  ctx.__el = (s) => { if (!els.has(s)) els.set(s, makeEl(s)); return els.get(s); };
  ctx.__has = (s) => els.has(s);
  return ctx;
}
const settle = () => new Promise((r) => setTimeout(r, 0));

let PASS = 0, FAIL = 0;
const ok = (t) => { PASS++; console.log('  ok   ' + t); };
const no = (t) => { FAIL++; console.log('  FAIL ' + t); };

const certConfigBase = {
  mode: 'acme', fullchain: '', privkey: '', host_fullchain: '', host_privkey: '',
  runtime_fullchain: '/etc/ssl/ansgo/fullchain.pem', runtime_privkey: '/etc/ssl/ansgo/privkey.pem',
  docker_mode: false, domain: 'your-domain.com',
  dynu: { has_api_key: false, has_oauth: false }, dynu_configured: false, acme_email: '',
};
const certInfo = { subject: 'your-domain.com', issuer: "Let's Encrypt", issuer_org: "Let's Encrypt", not_before: 'a', not_after: 'b', days_left: 60 };

// ========== T1 ACME 精简：只渲染 API Key + 邮箱 ==========
{
  const ctx = boot({ 'api/auth': { authed: false }, 'api/cert': certInfo, 'api/cert/config': { ...certConfigBase, dynu: { has_api_key: true, has_oauth: false }, dynu_configured: true } });
  await settle(); await ctx.loadCert(); await settle();
  const h = ctx.__el('#content').innerHTML;
  h.includes('id="dynu_api_key"') ? ok('ACME 区渲染 API Key 输入') : no('ACME 区缺少 API Key 输入');
  h.includes('id="acme_email"') ? ok('ACME 区渲染邮箱输入') : no('ACME 区缺少邮箱输入');
  !h.includes('dynu_client_id') && !h.includes('dynu_secret')
    ? ok('ACME 区不再渲染 Client ID / Secret 输入')
    : no('ACME 区仍渲染 Client ID / Secret 输入');
}

// ========== T2 saveCertConfig 不提交 OAuth 字段 ==========
{
  const ctx = boot({
    'api/auth': { authed: false },
    'api/cert': certInfo,
    'api/cert/config': { ...certConfigBase, dynu: { has_api_key: false, has_oauth: false } },
  });
  await settle(); await ctx.loadCert(); await settle();
  ctx.__el('#cert_mode').value = 'acme';
  ctx.__el('#dynu_api_key').value = 'NEWFAKEKEY';
  ctx.__el('#acme_email').value = 'a@b.c';
  await ctx.saveCertConfig(); await settle();
  const post = ctx.posts.find((p) => p.key === 'api/cert/config');
  if (!post) { no('saveCertConfig 未发出 POST'); }
  else {
    const body = JSON.parse(post.body);
    body.dynu_api_key === 'NEWFAKEKEY' && body.acme_email === 'a@b.c' && body.mode === 'acme'
      ? ok('提交体含 mode/dynu_api_key/acme_email') : no('提交体缺 mode/api_key/email');
    !('dynu_client_id' in body) && !('dynu_secret' in body)
      ? ok('提交体不再含 dynu_client_id / dynu_secret') : no('提交体仍含 OAuth 字段');
  }
}

// ========== T3 OAuth-only 迁移提示 ==========
{
  const ctx = boot({ 'api/auth': { authed: false }, 'api/cert': certInfo, 'api/cert/config': { ...certConfigBase, dynu: { has_api_key: false, has_oauth: true }, dynu_configured: true } });
  await settle(); await ctx.loadCert(); await settle();
  const h = ctx.__el('#content').innerHTML;
  /OAuth/i.test(h) && /迁移|继续|兼容|旧/.test(h)
    ? ok('OAuth-only（无 API Key）显示兼容迁移提示') : no('OAuth-only 无迁移提示');
}
{
  const ctx = boot({ 'api/auth': { authed: false }, 'api/cert': certInfo, 'api/cert/config': { ...certConfigBase, dynu: { has_api_key: true, has_oauth: false }, dynu_configured: true } });
  await settle(); await ctx.loadCert(); await settle();
  !/迁移/.test(ctx.__el('#content').innerHTML)
    ? ok('已配 API Key 时不显示迁移提示') : no('有 API Key 仍显示迁移提示');
}

// ========== T4 同步脚本：Docker + manual 即时切换 ==========
{
  // Docker 形态、当前保存 mode=acme → 切下拉到 manual 应立即出现同步脚本区
  // 初始渲染态从 innerHTML 文本断言（模板内联 style）；切换后从元素 style 断言（onchange 操作元素）
  const ctx = boot({ 'api/auth': { authed: false }, 'api/cert': certInfo, 'api/cert/config': { ...certConfigBase, docker_mode: true } });
  await settle(); await ctx.loadCert(); await settle();
  const h0 = ctx.__el('#content').innerHTML;
  if (!h0.includes('id="docker_sync_wrap"')) { no('Docker 形态未渲染 docker_sync_wrap（无法即时切换）'); }
  else {
    h0.includes('id="docker_sync_wrap" style="display:none;') ? ok('初始 acme 模式同步脚本区隐藏') : no('初始 acme 模式同步脚本区应隐藏');
    typeof ctx.__el('#cert_mode').onchange === 'function' ? ok('cert_mode 绑定 onchange') : no('cert_mode 未绑定 onchange');
    ctx.__el('#cert_mode').value = 'manual';
    ctx.__el('#cert_mode').onchange();
    ctx.__el('#docker_sync_wrap').style.display === 'block' ? ok('切到 manual 后同步脚本区即时显示') : no('切到 manual 后同步脚本区未显示');
    ctx.__el('#cert_mode').value = 'acme';
    ctx.__el('#cert_mode').onchange();
    ctx.__el('#docker_sync_wrap').style.display === 'none' ? ok('切回 acme 后同步脚本区即时隐藏') : no('切回 acme 后未隐藏');
  }
  // manual 初始即显示
  const ctx2 = boot({ 'api/auth': { authed: false }, 'api/cert': certInfo, 'api/cert/config': { ...certConfigBase, mode: 'manual', docker_mode: true } });
  await settle(); await ctx2.loadCert(); await settle();
  const h1 = ctx2.__el('#content').innerHTML;
  h1.includes('id="docker_sync_wrap"') && !h1.includes('id="docker_sync_wrap" style="display:none;')
    ? ok('Docker + manual（初始）同步脚本区显示') : no('Docker + manual（初始）同步脚本区未显示');
}
{
  // 裸金属：任何模式都不渲染同步脚本区（回归守卫）
  for (const mode of ['acme', 'manual']) {
    const ctx = boot({ 'api/auth': { authed: false }, 'api/cert': certInfo, 'api/cert/config': { ...certConfigBase, mode, docker_mode: false } });
    await settle(); await ctx.loadCert(); await settle();
    !ctx.__el('#content').innerHTML.includes('id="docker_sync_wrap"')
      ? ok(`裸金属 ${mode} 模式无同步脚本区`) : no(`裸金属 ${mode} 模式渲染了同步脚本区`);
  }
}

// ========== T5 节点信息：naive 用域名，其余优先 IP；复制/二维码同 URI ==========
{
  const nodeResp = {
    domain: 'your-domain.com', server_ip: '203.0.113.7',
    anytls: { enabled: true, uri: 'anytls://u:p@203.0.113.7:23458?sni=your-domain.com#NodeB-AT', host: '203.0.113.7', port: 23458, password: 'p', sni: 'your-domain.com' },
    naive: { enabled: true, uri: 'https://nu:np@your-domain.com:23460#NodeB-NV', host: 'your-domain.com', port: 23460, user: 'nu', pass: 'np', sni: 'your-domain.com' },
    ss: { enabled: true, uri: 'ss://xxx@203.0.113.7:23457#NodeB-SS', host: '203.0.113.7', port: 23457, method: '2022-blake3-aes-128-gcm', password: 'k' },
    socks: { enabled: true, uri: 'socks://su:sp@203.0.113.7:23459#NodeB-SK', host: '203.0.113.7', port: 23459, user: 'su', password: 'sp' },
    landings: [{ enabled: true, name: 'HK', uri: 'anytls://u:p@203.0.113.7:23461?sni=your-domain.com#NodeB-LD-HK', host: '203.0.113.7', port: 23461, password: 'p' }],
  };
  const ctx = boot({ 'api/auth': { authed: false }, 'api/node': nodeResp });
  await settle(); await ctx.loadNode(); await settle();
  const h = ctx.__el('#content').innerHTML;
  // 按相邻卡片标题切边界（innerHTML 含换行，不能依赖连续 </div></div>）
  const seg = (start, end) => {
    const i = h.indexOf(start);
    if (i < 0) return '';
    const j = end ? h.indexOf(end, i) : -1;
    return h.slice(i, j < 0 ? h.length : j);
  };
  const anytlsCard = seg('🛡️ AnyTLS', '🌀 NaiveProxy');
  const naiveCard = seg('🌀 NaiveProxy', '🔐 Shadowsocks');
  const ssCard = seg('🔐 Shadowsocks', '🧦 SOCKS5');
  const landCard = seg('🛬 落地-HK', null);
  anytlsCard.includes('203.0.113.7') ? ok('AnyTLS 连接地址用 IP') : no('AnyTLS 连接地址未用 IP');
  ssCard.includes('203.0.113.7') ? ok('Shadowsocks 连接地址用 IP') : no('Shadowsocks 连接地址未用 IP');
  landCard.includes('203.0.113.7') ? ok('落地服务连接地址用 IP') : no('落地服务连接地址未用 IP');
  naiveCard.includes('your-domain.com') && !naiveCard.replace(/sni[^<]*/g, '').includes('203.0.113.7')
    ? ok('NaiveProxy 连接地址仍用域名（不用 IP）') : no('NaiveProxy 连接地址用了 IP 或缺域名');
  // 复制与二维码使用同一 URI（naive 卡：showQR(title,uri) 与 copy(`uri`) 参数一致）
  const nvUri = nodeResp.naive.uri;
  naiveCard.includes(`showQR('NaiveProxy',\`${nvUri}\`)`) && naiveCard.includes(`copy(\`${nvUri}\`)`)
    ? ok('NaiveProxy 复制按钮与二维码按钮使用同一 URI') : no('NaiveProxy 复制/二维码 URI 不一致');
}

// ========== T6 设置页公网 IP 文案 ==========
{
  const ctx = boot({ 'api/auth': { authed: false }, 'api/settings': { panel_title: '', url_path: '/x/', admin_user: 'admin', session_hours: 8, panel_port: 1234, login_lock_threshold: 5, login_lock_minutes: 10, server_ip: '', server_ip_hint: '', server_ip_resolved: '', caddy_enable: 'true', disguise_panel: 'proxy:https://example.com', disguise_naive: 'proxy:https://example.com', anytls_port: 1, naive_port: 2, ss_port: 3, socks_port: 4 } });
  await settle(); await ctx.loadSet(); await settle();
  const h = ctx.__el('#content').innerHTML;
  /首次|第一次/.test(h) && /自动/.test(h) && /保存/.test(h)
    ? ok('公网 IP 文案说明「首次留空自动探测保存 / 手动覆盖」') : no('公网 IP 文案未说明首次自动保存语义');
}

// ========== T7 模式相关操作按钮随下拉即时切换（未保存切换隐藏旧模式操作） ==========
{
  // Docker + manual（已保存）：reload 操作显示，签发/续期隐藏，无 pending 提示
  const ctx = boot({ 'api/auth': { authed: false }, 'api/cert': certInfo, 'api/cert/config': { ...certConfigBase, mode: 'manual', docker_mode: true } });
  await settle(); await ctx.loadCert(); await settle();
  const h = ctx.__el('#content').innerHTML;
  h.includes('id="cert_ops_manual" style="display:inline"') ? ok('初始 manual：同步/重载操作区显示') : no('初始 manual：同步/重载操作区未渲染为显示');
  h.includes('id="cert_ops_acme" style="display:none"') ? ok('初始 manual：签发/续期操作区隐藏') : no('初始 manual：签发/续期操作区未隐藏');
  h.includes('id="cert_mode_pending" style="display:none;') && h.includes('已切换证书来源')
    ? ok('初始 manual：pending 提示隐藏但文案已备') : no('初始 manual：pending 提示区缺失或未隐藏');
  h.includes('同步并重新加载证书') ? ok('Docker+manual 显示「同步并重新加载证书」') : no('Docker+manual 缺「同步并重新加载证书」');
  // 切到 acme（未保存）：不允许点旧模式（manual）操作 → 全部模式操作隐藏 + pending 提示
  ctx.__el('#cert_mode').value = 'acme';
  ctx.__el('#cert_mode').onchange();
  ctx.__el('#cert_ops_manual').style.display === 'none' ? ok('切 acme 未保存：旧模式 reload 操作隐藏（不可点击）') : no('切 acme 未保存：旧模式 reload 操作仍显示');
  ctx.__el('#cert_ops_acme').style.display === 'none' ? ok('切 acme 未保存：签发操作也隐藏（需先保存）') : no('切 acme 未保存：签发操作未隐藏');
  ctx.__el('#cert_mode_pending').style.display !== 'none' ? ok('切 acme 未保存：显示「需先保存来源」提示') : no('切 acme 未保存：无 pending 提示');
  // 切回已保存的 manual：恢复对应操作
  ctx.__el('#cert_mode').value = 'manual';
  ctx.__el('#cert_mode').onchange();
  ctx.__el('#cert_ops_manual').style.display === 'inline' ? ok('切回已保存 manual：reload 操作恢复显示') : no('切回已保存 manual：reload 操作未恢复');
  ctx.__el('#cert_mode_pending').style.display === 'none' ? ok('切回已保存 manual：pending 提示消失') : no('切回已保存 manual：pending 提示未消失');
}
{
  // acme（已保存）初始：签发/续期显示；未保存切 manual 时同样全隐藏 + pending
  const ctx = boot({ 'api/auth': { authed: false }, 'api/cert': certInfo, 'api/cert/config': { ...certConfigBase, mode: 'acme', docker_mode: false } });
  await settle(); await ctx.loadCert(); await settle();
  const h = ctx.__el('#content').innerHTML;
  h.includes('id="cert_ops_acme" style="display:inline"') && h.includes('立即签发证书') && h.includes('手动续期')
    ? ok('初始 acme：签发/续期操作区显示') : no('初始 acme：签发/续期操作区未正确显示');
  ctx.__el('#cert_mode').value = 'manual';
  ctx.__el('#cert_mode').onchange();
  ctx.__el('#cert_ops_acme').style.display === 'none' && ctx.__el('#cert_ops_manual').style.display === 'none'
    ? ok('切 manual 未保存：acme 与 manual 操作均隐藏（不可点旧模式操作）') : no('切 manual 未保存：模式操作未全部隐藏');
  ctx.__el('#cert_mode_pending').style.display !== 'none' ? ok('切 manual 未保存：pending 提示显示') : no('切 manual 未保存：无 pending 提示');
}

// ========== T8 保存来源后按已存模式重渲染（正确显示签发/续期或重载） ==========
{
  const ctx = boot({ 'api/auth': { authed: false }, 'api/cert': certInfo, 'api/cert/config': { ...certConfigBase, mode: 'acme', docker_mode: true } });
  await settle(); await ctx.loadCert(); await settle();
  ctx.__el('#cert_mode').value = 'manual';
  await ctx.saveCertConfig(); await settle();   // POST 成功后前端应重新拉取并按已存模式渲染
  const h = ctx.__el('#content').innerHTML;
  h.includes('id="cert_ops_manual" style="display:inline"') && h.includes('同步并重新加载证书')
    ? ok('保存 manual 后重渲染：显示 Docker 同步/重载操作') : no('保存 manual 后未按已存模式显示重载操作');
  h.includes('id="cert_ops_acme" style="display:none"')
    ? ok('保存 manual 后重渲染：签发/续期隐藏') : no('保存 manual 后签发/续期仍显示');
  const post = ctx.posts.find((p) => p.key === 'api/cert/config');
  post && JSON.parse(post.body).mode === 'manual' ? ok('保存提交了 mode=manual') : no('保存未提交 mode=manual');
}

// ========== T9 server_ip 表单空初值保护（不抹掉后端启动自动保存的 IP） ==========
const settingsBase = { panel_title: '', url_path: '/x/', admin_user: 'admin', session_hours: 8, panel_port: 1234, login_lock_threshold: 5, login_lock_minutes: 10, server_ip: '', server_ip_hint: '', server_ip_resolved: '', caddy_enable: 'true', disguise_panel: 'proxy:https://example.com', disguise_naive: 'proxy:https://example.com', anytls_port: 1, naive_port: 2, ss_port: 3, socks_port: 4 };
{
  // a) 初值空 + 未动 IP + 改标题：payload 不得含 server_ip（防止空串覆盖后端自动保存值）
  const ctx = boot({ 'api/auth': { authed: false }, 'api/settings': { ...settingsBase, server_ip: '' } });
  await settle(); await ctx.loadSet(); await settle();
  const el = ctx.__el('#s_server_ip');
  (el.dataset.initial === '') ? ok('loadSet 记录 server_ip 初值（空）') : no('loadSet 未记录 server_ip 初值');
  ctx.__el('#s_panel_title').value = 'NodeB';  // 只改标题，IP 未动（value 仍空）
  await ctx.saveSet(); await settle();
  const post = ctx.posts.find((p) => p.key === 'api/settings');
  post && !('server_ip' in JSON.parse(post.body))
    ? ok('初值空未动：payload 不含 server_ip（自动保存值不被空串抹掉）') : no('初值空未动仍提交 server_ip（会抹掉自动保存值）');
}
{
  // b) 初值空 + 用户输入 IP（含「自动检测」填入场景）：提交
  const ctx = boot({ 'api/auth': { authed: false }, 'api/settings': { ...settingsBase, server_ip: '' } });
  await settle(); await ctx.loadSet(); await settle();
  ctx.__el('#s_server_ip').value = '203.0.113.9';
  await ctx.saveSet(); await settle();
  const post = ctx.posts.find((p) => p.key === 'api/settings');
  post && JSON.parse(post.body).server_ip === '203.0.113.9'
    ? ok('用户输入/自动检测填入：提交 server_ip') : no('用户输入的 IP 未提交');
}
{
  // c) 初值非空 + 用户显式清空：照常提交空（允许清除）
  const ctx = boot({ 'api/auth': { authed: false }, 'api/settings': { ...settingsBase, server_ip: '203.0.113.7' } });
  await settle(); await ctx.loadSet(); await settle();
  const el = ctx.__el('#s_server_ip');
  el.dataset.initial = '203.0.113.7';  // loadSet 记录的非空初值
  el.value = '';                        // 用户显式清空
  await ctx.saveSet(); await settle();
  const post = ctx.posts.find((p) => p.key === 'api/settings');
  post && JSON.parse(post.body).server_ip === ''
    ? ok('初值非空显式清空：提交 server_ip=""（允许清除）') : no('显式清空未提交（无法移除 IP）');
}

console.log(`\n结果: PASS=${PASS} FAIL=${FAIL}`);
process.exit(FAIL === 0 ? 0 : 1);
