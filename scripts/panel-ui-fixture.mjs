#!/usr/bin/env node
// =============================================================================
// ANS-GO 面板 UI 本地验收夹具（无敏感假 API，零依赖 Node http）
//
// 供主代理用浏览器实际渲染验收 deploy/panel/web/index.html 的前端改动
// （v1.5.37：ACME 精简 / OAuth 迁移提示 / 同步脚本即时切换 / naive 域名 / 设置文案）。
//
// 启动（仓库根目录）：
//   node scripts/panel-ui-fixture.mjs                       # 裸金属 + acme 默认场景
//   ANSGO_FIXTURE_DOCKER=1 node scripts/panel-ui-fixture.mjs          # Docker 形态
//   ANSGO_FIXTURE_DOCKER=1 ANSGO_FIXTURE_CERT_MODE=manual node scripts/panel-ui-fixture.mjs
//   ANSGO_FIXTURE_DYNU=oauth node scripts/panel-ui-fixture.mjs        # OAuth-only 迁移提示
//   ANSGO_FIXTURE_DYNU=none  node scripts/panel-ui-fixture.mjs        # 未配置凭证
//   PORT=9000 node scripts/panel-ui-fixture.mjs             # 自定义端口（默认 8788）
//
// 全部数据均为假值：域名 your-domain.com、IP 用 TEST-NET 保留段（203.0.113.0/24）、
// 密钥/密码为明显假串。无任何真实部署信息。登录页任意账号密码可登录。
// =============================================================================
import { createServer } from 'node:http';
import { readFileSync, existsSync } from 'node:fs';
import { dirname, join, extname } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');
const WEB = join(ROOT, 'deploy/panel/web');
const PORT = Number(process.env.PORT || 8788);

// ---- 场景开关（环境变量） ----
const SC = {
  docker: process.env.ANSGO_FIXTURE_DOCKER === '1',          // Docker 形态（宿主证书 UI + 同步脚本区）
  certMode: process.env.ANSGO_FIXTURE_CERT_MODE || 'acme',   // acme | manual
  dynu: process.env.ANSGO_FIXTURE_DYNU || 'apikey',          // apikey | oauth | both | none
};
const dynu = {
  apikey: { has_api_key: true, has_oauth: false },
  oauth: { has_api_key: false, has_oauth: true },            // OAuth-only → 迁移提示
  both: { has_api_key: true, has_oauth: true },
  none: { has_api_key: false, has_oauth: false },
}[SC.dynu] || { has_api_key: false, has_oauth: false };

const now = new Date('2026-09-06T12:00:00Z');
const DATA = {
  domain: 'your-domain.com',
  serverIp: '203.0.113.7', // TEST-NET-3，非真实地址
};

const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'application/javascript', '.css': 'text/css' };

const api = {
  'auth': { authed: true, user: 'admin', version: '1.5.37' },
  'login': { ok: true },
  'logout': { ok: true },
  'dashboard': {
    services: { anytls: 'active', naive: 'active', ss: 'active', socks: 'inactive', panel: 'active' },
    ports: { anytls: 23458, naive: 23460, ss: 23457, socks: 23459, panel: 23456 },
    svc_enabled: { anytls: true, naive: true, ss: true, socks: true },
    mem: { total: 480000, used: 120000 }, load: [0.1, 0.2, 0.15], uptime: 72.5, tcp: 18,
    domain: DATA.domain,
    cert: { subject: DATA.domain, issuer: "Let's Encrypt", issuer_org: "Let's Encrypt", not_before: '2026-08-01', not_after: '2026-10-01', days_left: 25 },
  },
  'node': {
    // 字段与 URI 严格对齐真实后端 nodeHandler/buildURIs（v1.5.37）语义：
    //   ss     ss://<base64url-nopad(method:key)>@<host>:<port>#<frag>   （SIP002 userinfo）
    //   anytls anytls://<pass>@<host>:<port>/?sni=<domain>#<frag>
    //   socks  socks5://<user>:<pass-queryescape>@<host>:<port>#<frag>
    //   naive  naive+https://<user>:<pass-queryescape>@<domain>:<port>#<frag>（域名，不用 IP）
    //   host 字段：连接主机（server_ip 优先），naive 固定域名；sni 始终域名
    domain: DATA.domain,
    server_ip: DATA.serverIp,
    server_ip_hint: '',
    ss: (() => {
      const method = '2022-blake3-aes-128-gcm', key = 'ZmFrZWtleS1iYXNlNjQtMTY='; // 假密钥
      const userinfo = Buffer.from(method + ':' + key).toString('base64url'); // 无 padding，与 RawURLEncoding 一致
      return { enabled: true, uri: `ss://${userinfo}@${DATA.serverIp}:23457#NodeB-SS`, host: DATA.serverIp, port: 23457, method, password: key };
    })(),
    anytls: { enabled: true, uri: `anytls://fakepass@${DATA.serverIp}:23458/?sni=${DATA.domain}#NodeB-AT`, host: DATA.serverIp, port: 23458, password: 'fakepass', sni: DATA.domain },
    naive: { enabled: true, uri: `naive+https://fakeuser:fakepass@${DATA.domain}:23460#NodeB-NV`, host: DATA.domain, port: 23460, user: 'fakeuser', pass: 'fakepass', password: 'fakepass', sni: DATA.domain },
    socks: { enabled: true, uri: `socks5://fakeuser:fakepass@${DATA.serverIp}:23459#NodeB-SK`, host: DATA.serverIp, port: 23459, user: 'fakeuser', pass: 'fakepass', password: 'fakepass' },
    landings: [{ enabled: true, name: 'HK', via: 'ss-landing', uri: `anytls://fakepass@${DATA.serverIp}:23461/?sni=${DATA.domain}#NodeB-LD-HK`, host: DATA.serverIp, port: 23461, password: 'fakepass', sni: DATA.domain, remote_type: 'ss' }],
  },
  'settings': {
    panel_title: 'NodeB', url_path: '/fixture01/', admin_user: 'admin', session_hours: 8, panel_port: 23456,
    login_lock_threshold: 5, login_lock_minutes: 10, server_ip: DATA.serverIp, server_ip_hint: '', server_ip_resolved: DATA.serverIp,
    caddy_enable: 'true', disguise_panel: 'proxy:https://example.com', disguise_naive: 'proxy:https://example.com',
    anytls_port: 23458, naive_port: 23460, ss_port: 23457, socks_port: 23459,
  },
  'landings': { landings: [
    { id: 1, name: 'HK', port: 23461, enabled: true, password: 'fakepass', has_keys: true,
      remote_enabled: true, remote_type: 'ss', remote_host: '198.51.100.20', remote_port: 8388,
      remote_method: '2022-blake3-aes-128-gcm', remote_password: 'fakeremotekey' },
    { id: 2, name: '直连组', port: 23462, enabled: false, password: '', has_keys: false,
      remote_enabled: false, remote_type: 'ss', remote_host: '', remote_port: 0, remote_method: '2022-blake3-aes-128-gcm', remote_password: '' },
  ] },
  'cert': { subject: DATA.domain, issuer: "Let's Encrypt", issuer_org: "Let's Encrypt", not_before: '2026-08-01', not_after: '2026-10-01', days_left: 25 },
  'cert/config': {
    mode: SC.certMode,
    fullchain: '/etc/letsencrypt/live/your-domain.com/fullchain.pem',
    privkey: '/etc/letsencrypt/live/your-domain.com/privkey.pem',
    host_fullchain: SC.docker ? '/www/server/panel/vhost/cert/your-domain.com/fullchain.pem' : '',
    host_privkey: SC.docker ? '/www/server/panel/vhost/cert/your-domain.com/privkey.pem' : '',
    runtime_fullchain: '/etc/ssl/ansgo/fullchain.pem', runtime_privkey: '/etc/ssl/ansgo/privkey.pem',
    docker_mode: SC.docker, domain: DATA.domain,
    dynu, dynu_configured: dynu.has_api_key || dynu.has_oauth,
    acme_email: 'admin@your-domain.com',
  },
  'detect-public-ip': { ok: true, ip: DATA.serverIp },
  'logs': { logs: '[fixture] 示例日志行 1\n[fixture] 示例日志行 2' },
};
// POST 端点统一假成功（设置保存不返回 new_url，避免页面跳转打断验收）
const postOK = {
  'settings': { ok: true },
  'port': { ok: true }, 'key': { ok: true }, 'regen': { ok: true },
  'svc-install': { ok: true }, 'service': { ok: true }, 'repair': { ok: true, results: {} },
  'health': { ok: true, enabled: true, active: 'active', unit: 'sing-box.service', port: 23458, port_listening: 'yes', tcp_connect: 'yes', tcp_ms: 3, summary: 'fixture: 一切正常（假数据）' },
  'landings': { ok: true }, 'landings/update': { ok: true }, 'landings/regen': { ok: true }, 'landings/delete': { ok: true },
  'cert/config': { ok: true }, 'cert/issue': { ok: true, log: 'fixture: 签发成功（假）' },
  'cert/renew': { ok: true }, 'cert/reload': { ok: true },
};

const server = createServer((req, res) => {
  const url = new URL(req.url, `http://localhost:${PORT}`);
  const p = url.pathname;
  const send = (code, body, type = 'application/json; charset=utf-8') => {
    res.writeHead(code, { 'Content-Type': type, 'Cache-Control': 'no-store' });
    res.end(typeof body === 'string' ? body : JSON.stringify(body));
  };
  // 静态（HTML 每请求实时读盘：改 index.html 无需重启夹具）
  if (p === '/' || p === '/index.html' || p === '/fixture01/' || p.startsWith('/fixture01')) {
    let page = readFileSync(join(WEB, 'index.html'), 'utf8');
    // ?noconfirm：自动化浏览器常默认 dismiss confirm/prompt 导致保存流程静默取消，
    // 注入自动同意 stub 方便无人值守验收（真实交互不受影响）
    if (url.searchParams.has('noconfirm')) {
      page = page.replace('<head>', '<head><script>window.confirm=()=>true;window.alert=()=>{};window.prompt=(m,d)=>(d!==undefined?d:null)</script>');
    }
    return send(200, page, MIME['.html']);
  }
  if (p === '/static/qrcode.min.js') {
    return send(200, readFileSync(join(WEB, 'qrcode.min.js'), 'utf8'), MIME['.js']);
  }
  // API（相对路径，面板以 api/... 调用）
  if (p.startsWith('/api/')) {
    const key = p.replace(/^\/api\//, '').replace(/\/$/, '');
    if (req.method === 'POST') {
      let body = '';
      req.on('data', (c) => { body += c; });
      req.on('end', () => {
        // 模拟真实后端保存副作用：POST cert/config 的 mode/路径合并回 GET 响应，
        // 保存后前端 loadCert 重渲染才能拿到新已存模式（否则验收会误判「保存无效」）
        if (key === 'cert/config' && body) {
          try {
            const b = JSON.parse(body);
            if (b.mode === 'acme' || b.mode === 'manual') api['cert/config'].mode = b.mode;
            if (typeof b.host_fullchain === 'string') api['cert/config'].host_fullchain = b.host_fullchain;
            if (typeof b.host_privkey === 'string') api['cert/config'].host_privkey = b.host_privkey;
            // 凭证只回传存在状态（与真实后端一致，绝不回明文）
            if (b.dynu_api_key) api['cert/config'].dynu.has_api_key = true;
            if (typeof b.acme_email === 'string') api['cert/config'].acme_email = b.acme_email;
          } catch {}
        }
        if (postOK[key] !== undefined) return send(200, postOK[key]);
        return send(200, { ok: true, fixture: true });
      });
      return;
    }
    if (api[key] !== undefined) return send(200, api[key]);
    return send(200, { fixture: true });
  }
  if (existsSync(join(WEB, p)) && !p.includes('..')) {
    return send(200, readFileSync(join(WEB, p)), MIME[extname(p)] || 'application/octet-stream');
  }
  send(404, 'not found (fixture)', 'text/plain');
});

server.listen(PORT, '127.0.0.1', () => {
  console.log(`ANS-GO 面板 UI 验收夹具已启动（假数据，无敏感信息）`);
  console.log(`  地址:      http://127.0.0.1:${PORT}/  （已预登录 authed，任意账号密码也可登录）`);
  console.log(`  提示:      加 ?noconfirm 自动同意 confirm/prompt（自动化浏览器默认 dismiss 弹窗会让保存静默取消）`);
  console.log(`  当前场景:  形态=${SC.docker ? 'Docker' : '裸金属'} / 证书=${SC.certMode} / Dynu=${SC.dynu}`);
  console.log(`  场景切换:  ANSGO_FIXTURE_DOCKER=1 | ANSGO_FIXTURE_CERT_MODE=manual | ANSGO_FIXTURE_DYNU=apikey|oauth|both|none`);
  console.log(`  验收要点:  证书管理页（ACME 仅 API Key+邮箱；OAuth-only 迁移提示；Docker+manual 同步脚本随下拉即时显隐）、`);
  console.log(`             节点信息页（Naive 连接地址=域名，其余=203.0.113.7；复制/二维码同 URI）、`);
  console.log(`             面板设置页（公网 IP 首次自动保存/手动覆盖文案）`);
});
