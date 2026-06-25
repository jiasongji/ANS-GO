package main

import (
	"bufio"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "embed"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/qrcode.min.js
var qrcodeJS []byte

const (
	binGenConf   = "/usr/local/bin/ansgo-genconf"
	binAdmin     = "/usr/local/bin/ansgo-admin"
	binAcme      = "/root/.acme.sh/acme.sh"
	binCertIssue = "/etc/ansgo-deploy/ansgo-cert-issue.sh"
)

// acmeAccountConf：Dynu 凭证经 acme.sh _saveaccountconf_mutable 持久化于此。
// 续期 cron 不带环境变量，必须事先写进这个文件，续期才能读到凭证。
// 设为变量（而非常量）便于测试覆盖路径（生产运行时值不变），与 confPath/secretsPath 同口径。
var acmeAccountConf = "/root/.acme.sh/account.conf"

// ===================== 路由根 =====================

func rootHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	p := r.URL.Path

	// 访问不带尾斜杠 -> 跳转
	if p == strings.TrimRight(c.URLPath, "/") && p != "" {
		http.Redirect(w, r, c.URLPath, http.StatusFound)
		return
	}
	if !strings.HasPrefix(p, c.URLPath) {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(p, c.URLPath)

	switch {
	case rel == "" || rel == "/" || rel == "index.html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		title := c.PanelTitle
		if title == "" {
			title = "ANS-GO 管理面板"
		}
		page := strings.Replace(string(indexHTML), "<title>ANS-GO 管理面板</title>", "<title>"+html.EscapeString(title)+"</title>", 1)
		w.Write([]byte(page))
	case rel == "static/qrcode.min.js":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(qrcodeJS)
	case rel == "api/auth":
		authCheckHandler(w, r)
	case rel == "api/login":
		loginHandler(w, r)
	case rel == "api/logout":
		requireAuth(logoutHandler)(w, r)
	case rel == "api/dashboard":
		requireAuth(dashboardHandler)(w, r)
	case rel == "api/node":
		requireAuth(nodeHandler)(w, r)
	case rel == "api/service":
		requireAuth(serviceHandler)(w, r)
	case rel == "api/port":
		requireAuth(portHandler)(w, r)
	case rel == "api/regen":
		requireAuth(regenHandler)(w, r)
	case rel == "api/key":
		requireAuth(keyHandler)(w, r)
	case rel == "api/cert":
		requireAuth(certHandler)(w, r)
	case rel == "api/cert/renew":
		requireAuth(certRenewHandler)(w, r)
	case rel == "api/cert/config":
		requireAuth(certConfigHandler)(w, r)
	case rel == "api/cert/reload":
		requireAuth(certReloadHandler)(w, r)
	case rel == "api/cert/issue":
		requireAuth(certIssueHandler)(w, r)
	case rel == "api/settings":
		requireAuth(settingsHandler)(w, r)
	case rel == "api/detect-public-ip":
		requireAuth(detectPublicIPHandler)(w, r)
	case rel == "api/landing":
		requireAuth(landingHandler)(w, r)
	case rel == "api/group2":
		requireAuth(group2Handler)(w, r)
	case rel == "api/landings":
		requireAuth(landingsHandler)(w, r)
	case rel == "api/landings/update":
		requireAuth(landingUpdateHandler)(w, r)
	case rel == "api/landings/delete":
		requireAuth(landingDeleteHandler)(w, r)
	case rel == "api/landings/regen":
		requireAuth(landingRegenHandler)(w, r)
	case rel == "api/svc-install":
		requireAuth(svcInstallHandler)(w, r)
	case rel == "api/health":
		requireAuth(healthHandler)(w, r)
	case rel == "api/repair":
		requireAuth(repairHandler)(w, r)
	case rel == "api/logs":
		requireAuth(logsHandler)(w, r)
	default:
		http.NotFound(w, r)
	}
}

// ===================== 认证 =====================

func authCheckHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	if cookie, err := r.Cookie("bv_sess"); err == nil && sessionValid(cookie.Value) {
		// version 暴露给前端侧栏底部显示（v1.5.22），方便用户核实升级是否生效
		jwrite(w, 200, map[string]any{"authed": true, "user": c.AdminUser, "version": version})
		return
	}
	jwrite(w, 200, map[string]any{"authed": false, "version": version})
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	c := configGet()
	ip := clientIP(r)

	fails, until := lockStatus(ip)
	if time.Now().Unix() < until {
		jerr(w, 429, fmt.Sprintf("该 IP 已被锁定，约 %d 分钟后解除", (until-time.Now().Unix())/60+1))
		return
	}
	var b struct {
		User string `json:"user"`
		Pass string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		jerr(w, 400, "请求格式错误")
		return
	}
	match := b.User == c.AdminUser &&
		c.AdminPassHash != "" && !strings.HasPrefix(c.AdminPassHash, "PLACEHOLDER") &&
		bcryptOK(c.AdminPassHash, b.Pass)
	if match {
		lockReset(ip)
		tok, err := sessionCreate(ip, c.SessionHours)
		if err != nil {
			jerr(w, 500, "会话创建失败")
			return
		}
		setSessionCookie(w, tok, c.SessionHours)
		jwrite(w, 200, map[string]any{"ok": true})
		return
	}
	locked, _ := lockRecordFail(ip, c.LoginLockThreshold, c.LoginLockMinutes)
	if locked {
		jerr(w, 429, fmt.Sprintf("连续错误达 %d 次，该 IP 已锁定 %d 分钟", c.LoginLockThreshold, c.LoginLockMinutes))
		return
	}
	fails, _ = lockStatus(ip)
	remain := c.LoginLockThreshold - fails
	if remain < 0 {
		remain = 0
	}
	jerr(w, 401, fmt.Sprintf("用户名或密码错误，剩余尝试 %d 次", remain))
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("bv_sess"); err == nil {
		sessionDelete(cookie.Value)
	}
	clearSessionCookie(w)
	jwrite(w, 200, map[string]bool{"ok": true})
}

// ===================== 仪表盘 =====================

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	sbActive := svcActive("sing-box") == "active"
	caddyActive := svcActive("caddy") == "active"
	panelActive := svcActive("ansgo-panel") == "active"
	ssEnabled := c.SvcSSEnabled == "true"
	anytlsEnabled := c.SvcAnyTLSEnabled == "true"
	socksEnabled := c.SvcSocksEnabled == "true"
	naiveEnabled := c.SvcNaiveEnabled == "true"
	caddyNeeded := c.CaddyEnable == "true" || naiveEnabled
	svcStatus := func(enabled, carrierActive bool) string {
		if enabled && carrierActive {
			return "active"
		}
		return "inactive"
	}
	resp := map[string]any{
		"services": map[string]string{
			"naive":    svcStatus(naiveEnabled, caddyActive),
			"ss":       svcStatus(ssEnabled, sbActive),
			"anytls":   svcStatus(anytlsEnabled, sbActive),
			"socks":    svcStatus(socksEnabled, sbActive),
			"panel":    map[bool]string{true: "active", false: "inactive"}[panelActive],
			"caddy":    svcStatus(caddyNeeded, caddyActive),
			"sing-box": svcStatus(ssEnabled || anytlsEnabled || socksEnabled || len(enabledLandings(c)) > 0, sbActive),
		},
		"svc_enabled": map[string]bool{"ss": ssEnabled, "anytls": anytlsEnabled, "socks": socksEnabled, "naive": naiveEnabled},
		"ports":       map[string]int{"naive": c.NaivePort, "anytls": c.AnyTLSPort, "socks": c.SocksPort, "ss": c.SSPort, "panel": c.PanelPort},
		"domain":      c.Domain,
		"url":         fmt.Sprintf("https://%s:%d%s", c.Domain, c.PanelPort, c.URLPath),
		"mem":         memInfo(), "load": loadAvg(), "uptime": uptimeHours(), "tcp": tcpEstabCount(), "cert": certInfoFull(c),
		"landings_enabled": len(enabledLandings(c)),
	}
	jwrite(w, 200, resp)
}

// ===================== 节点信息 =====================

// 服务器出口 IP 解析（v1.5.18 修正 VPC 问题）。
// 优先级：① 用户在面板设置填写的 server_ip（最高，VPC 下唯一可靠来源）
//
//	② UDP "连接" 8.8.8.8:80 探测本机出口 IP（仅当为公网时才采用）
//	③ 空（前端回退域名）
//
// VPC/NAT 网络下，UDP 探测只能拿到内网网卡 IP（10./172.16-31./192.168./100.64.），
// 公网 IP 在 NAT 网关上做 SNAT，本机无从得知。故对内网 IP 直接丢弃并回退域名，
// 同时通过 server_ip_hint 字段告知前端「需用户手动填写公网 IP」。
// 不调任何第三方公网 API（避免默认外发 + 符合 §13 隐私偏好）。
var (
	probeIPCache string // UDP 探测到的本机出口 IP（可能为空或内网）
	probeIPOnce  sync.Once
)

func probeLocalIP() string {
	probeIPOnce.Do(func() {
		conn, err := net.Dial("udp", "8.8.8.8:80")
		if err != nil {
			return
		}
		defer conn.Close()
		if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			probeIPCache = a.IP.String()
		}
	})
	return probeIPCache
}

// isPrivateIP 判定一个 IPv4 字符串是否为内网（RFC1918 / CGNAT / 回环 / 链路本地）。
func isPrivateIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return true // 非法 IP 视为不可用
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return true
		}
		// 172.16.0.0/12
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		// 192.168.0.0/16
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
		// 100.64.0.0/10 (CGNAT)
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
	}
	return false
}

// resolveServerIP 按「手动填写 > 公网探测」优先级返回展示用 IP + hint。
func resolveServerIP(c Config) (ip string, hint string) {
	// ① 用户手动填写的公网 IP（最高优先级）
	if manual := strings.TrimSpace(c.ServerIP); manual != "" {
		return manual, ""
	}
	// ② UDP 探测本机出口
	probe := probeLocalIP()
	if probe != "" && !isPrivateIP(probe) {
		return probe, ""
	}
	// ③ 探测失败或为内网 → 返回空，提示前端引导用户手动填写
	if probe != "" && isPrivateIP(probe) {
		return "", "检测到内网 IP（" + probe + "），VPC/NAT 网络下无法自动获取公网 IP，请在「面板设置」填写公网 IP"
	}
	return "", "无法探测服务器 IP，请在「面板设置」填写公网 IP（留空则连接地址显示域名）"
}

func nodeHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	sec := readSecrets()
	uris := buildURIs(c, sec)
	ip, hint := resolveServerIP(c)
	// v1.5.26: 落地服务列表（替代旧 anytls2 单条）
	landings := []map[string]any{}
	for _, L := range c.Landings {
		pass, _, _ := readLandingSecrets(L.ID)
		via := "direct"
		if L.RemoteEnabled && L.RemoteHost != "" && L.RemotePort != 0 {
			if L.RemoteType == "socks" {
				via = "socks5-landing"
			} else {
				via = "ss-landing"
			}
		}
		landings = append(landings, map[string]any{
			"id":          L.ID,
			"name":        L.Name,
			"uri":         uris["landing-"+L.ID],
			"port":        L.Port,
			"password":    pass,
			"sni":         c.Domain,
			"enabled":     L.Enabled,
			"host":        c.Domain,
			"via":         via,
			"remote_type": L.RemoteType,
		})
	}
	resp := map[string]any{
		"domain":         c.Domain,
		"server_ip":      ip,
		"server_ip_hint": hint,
		"ss":             map[string]any{"uri": uris["ss"], "method": c.SSMethod, "port": c.SSPort, "password": sec.SSKey, "enabled": c.SvcSSEnabled == "true", "host": c.Domain},
		"anytls":         map[string]any{"uri": uris["anytls"], "port": c.AnyTLSPort, "password": sec.AnyTLSPass, "sni": c.Domain, "enabled": c.SvcAnyTLSEnabled == "true", "host": c.Domain},
		"socks":          map[string]any{"uri": uris["socks"], "port": c.SocksPort, "user": sec.SocksUser, "pass": sec.SocksPass, "password": sec.SocksPass, "enabled": c.SvcSocksEnabled == "true", "host": c.Domain},
		"naive":          map[string]any{"uri": uris["naive"], "port": c.NaivePort, "user": sec.NaiveUser, "pass": sec.NaivePass, "password": sec.NaivePass, "sni": c.Domain, "enabled": c.SvcNaiveEnabled == "true", "host": c.Domain},
		"landings":       landings,
	}
	jwrite(w, 200, resp)
}

// ===================== 服务控制 =====================

func serviceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct{ Target, Action string }
	json.NewDecoder(r.Body).Decode(&b)
	if b.Action != "start" && b.Action != "stop" && b.Action != "restart" {
		jerr(w, 400, "action 必须为 start/stop/restart")
		return
	}
	if b.Target == "ss" || b.Target == "anytls" || b.Target == "socks" {
		if b.Action == "stop" || b.Action == "start" {
			c := configGet()
			en := boolStr(b.Action == "start")
			switch b.Target {
			case "ss":
				c.SvcSSEnabled = en
			case "anytls":
				c.SvcAnyTLSEnabled = en
			case "socks":
				c.SvcSocksEnabled = en
			}
			if err := configSet(c); err != nil {
				jerr(w, 500, "保存配置失败: "+err.Error())
				return
			}
			exec.Command(binGenConf, "sing-box").Run()
			needSB := c.SvcSSEnabled == "true" || c.SvcAnyTLSEnabled == "true" || c.SvcSocksEnabled == "true" || len(enabledLandings(c)) > 0
			if needSB {
				_ = exec.Command("systemctl", "enable", "sing-box").Run()
				_ = exec.Command("systemctl", "restart", "sing-box").Run()
			} else {
				_ = exec.Command("systemctl", "stop", "sing-box").Run()
				_ = exec.Command("systemctl", "disable", "sing-box").Run()
			}
			jwrite(w, 200, map[string]any{"ok": true, "target": b.Target, "action": b.Action})
			return
		}
	}
	var svcs []string
	switch b.Target {
	case "ss", "anytls", "socks":
		svcs = []string{"sing-box"}
	case "naive":
		svcs = []string{"caddy"}
	case "panel":
		svcs = []string{"ansgo-panel"}
	case "all":
		svcs = []string{"caddy", "sing-box", "ansgo-panel"}
	default:
		jerr(w, 400, "target 非法")
		return
	}
	var errs []string
	for _, svc := range svcs {
		if err := systemctl(b.Action, svc); err != nil {
			errs = append(errs, svc+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		jwrite(w, 500, map[string]any{"ok": false, "errors": errs})
		return
	}
	jwrite(w, 200, map[string]bool{"ok": true})
}

// ===================== 端口管理 =====================

func portHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct {
		Target string `json:"target"`
		Port   int    `json:"port"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	if !validPort(b.Port) {
		jerr(w, 400, "端口非法（1-65535）")
		return
	}
	c := configGet()
	switch b.Target {
	case "panel":
		c.PanelPort = b.Port
		if msg := portConflicts(c); msg != "" {
			jerr(w, 400, "端口冲突: "+msg)
			return
		}
		if err := configSet(c); err != nil {
			jerr(w, 500, "保存配置失败: "+err.Error())
			return
		}
		_ = exec.Command("sh", "-c", "nft add rule inet filter input tcp dport "+strconv.Itoa(b.Port)+" accept 2>/dev/null || true").Run()
		newURL := fmt.Sprintf("https://%s:%d%s", c.Domain, b.Port, c.URLPath)
		scheduleSelfRestart(3)
		jwrite(w, 200, map[string]any{"ok": true, "restart_in": 3, "new_url": newURL, "msg": fmt.Sprintf("面板将在 3 秒后重启到新端口 %d，请用新地址重新访问（会话已重置，需重新登录）。", b.Port)})
	case "ss", "anytls", "socks":
		if b.Target == "ss" {
			c.SSPort = b.Port
		} else if b.Target == "anytls" {
			c.AnyTLSPort = b.Port
		} else {
			c.SocksPort = b.Port
		}
		if msg := portConflicts(c); msg != "" {
			jerr(w, 400, "端口冲突: "+msg)
			return
		}
		if err := applyProto(c, "sing-box"); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		jwrite(w, 200, map[string]bool{"ok": true})
	case "naive":
		c.NaivePort = b.Port
		if msg := portConflicts(c); msg != "" {
			jerr(w, 400, "端口冲突: "+msg)
			return
		}
		if err := applyProto(c, "caddy"); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		jwrite(w, 200, map[string]bool{"ok": true})
	default:
		jerr(w, 400, "target 非法")
	}
}

// applyProto: 写配置 + 重新生成服务配置 + 重启
func applyProto(c Config, confTarget string) error {
	if err := configSet(c); err != nil {
		return err
	}
	if err := exec.Command(binGenConf, confTarget).Run(); err != nil {
		return fmt.Errorf("生成配置失败: %w", err)
	}
	svc := "sing-box"
	if confTarget == "caddy" {
		svc = "caddy"
	}
	if err := systemctl("restart", svc); err != nil {
		return fmt.Errorf("重启 %s 失败: %w", svc, err)
	}
	return nil
}

// genconfRestartVerify 同步执行：genconf → systemctl restart → 验证服务 active。
// v1.5.24：替代旧的「异步 go func + 忽略错误」模式。任一步失败立即返回错误，
// 调用方据此回滚 secrets.env / 报错给用户，避免「secrets 已改但 Caddyfile 未跟上 /
// caddy 重启失败」导致的不一致（NaiveProxy probe_resistance 会让这种不一致表现为
// 「反代正常但代理不能用」——极具迷惑性）。
// confTarget: "caddy" | "sing-box"。返回 (active状态, 错误)。
func genconfRestartVerify(confTarget string) (active string, err error) {
	svc := confTarget
	if confTarget == "caddy" {
		svc = "caddy"
	}
	out, gerr := exec.Command(binGenConf, confTarget).CombinedOutput()
	if gerr != nil {
		// genconf 失败时它内部已回滚 Caddyfile，服务继续用旧配置——不重启。
		return "", fmt.Errorf("生成配置失败（已回滚旧配置，服务未重启）: %s", strings.TrimSpace(string(out)))
	}
	if rerr := exec.Command("systemctl", "restart", svc).Run(); rerr != nil {
		return "", fmt.Errorf("重启 %s 失败: %w", svc, rerr)
	}
	// 验证：等待最多 4 秒确认服务真的 active（非 deactivating/failed）。
	// caddy/sing-box 启动很快，1-2 秒内应 active；给 4 秒余量。
	for i := 0; i < 8; i++ {
		st := svcActive(svc)
		if st == "active" {
			return "active", nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	final := svcActive(svc)
	return final, fmt.Errorf("%s 重启后未进入 active（当前 %s），可能配置错误或端口冲突；查看 journalctl -u %s", svc, final, svc)
}

// ===================== 密钥管理（委托 ansgo-admin）=====================

func regenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct {
		Target string `json:"target"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	if b.Target != "ss" && b.Target != "anytls" && b.Target != "socks" && b.Target != "naive" && b.Target != "g2" {
		jerr(w, 400, "target 必须为 ss/anytls/socks/naive/g2")
		return
	}
	adminArgs := []string{"regen", b.Target}
	if b.Target == "g2" {
		adminArgs = []string{"regen2"}
	}
	out, err := exec.Command(binAdmin, adminArgs...).CombinedOutput()
	if err != nil {
		jerr(w, 500, "regen 失败: "+string(out))
		return
	}
	c := configGet()
	sec := readSecrets()
	uris := buildURIs(c, sec)
	jwrite(w, 200, map[string]any{"ok": true, "log": string(out), "uris": uris})
}

// ===================== 手动设置密钥（直接写 secrets.env）=====================

// setSecret 原子写入单个 secrets.env 字段：存在则替换，不存在则追加。
// 不走 ansgo-admin 的 sed 路径（避免 `|` 等特殊字符破坏 sed 分隔符），直接全文重写。
// 与 readSecrets 共用 cfgMu 锁以保证读写互斥。
func setSecret(key, value string) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return setSecretLocked(key, value)
}

func setSecretLocked(key, value string) error {
	data, err := os.ReadFile(secretsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		data = []byte{}
	}
	var lines []string
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			lines = append(lines, line)
			continue
		}
		kv := strings.SplitN(trimmed, "=", 2)
		if len(kv) == 2 && strings.TrimSpace(kv[0]) == key {
			lines = append(lines, key+"="+value)
			found = true
		} else {
			lines = append(lines, line)
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	tmp := secretsPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, secretsPath)
}

// setLandingPassword 保存单个落地服务的 AnyTLS 密码。
// v1.5.28：落地服务页一直展示「AnyTLS 密码」输入框，但旧版 saveLanding
// 没有把该字段提交给后端，后端 update 也没有写 LANDING_<id>_PASS。
// 用户改密码后客户端拿到的是未生效密码，表现为新增/修改后的落地 AnyTLS 不可用。
func setLandingPassword(id, pass string) error {
	id = strings.TrimSpace(id)
	pass = strings.TrimSpace(pass)
	if id == "" {
		return fmt.Errorf("落地服务 id 不能为空")
	}
	if pass == "" {
		return fmt.Errorf("落地 AnyTLS 密码不能为空")
	}
	return setSecret("LANDING_"+id+"_PASS", pass)
}

func cloneConfig(c Config) Config {
	out := c
	if c.Landings != nil {
		out.Landings = append([]LandingService{}, c.Landings...)
	}
	return out
}

func backupSecretsFile() ([]byte, bool) {
	data, err := os.ReadFile(secretsPath)
	if err != nil {
		return nil, false
	}
	return append([]byte{}, data...), true
}

func restoreSecretsFile(data []byte, existed bool) {
	if existed {
		_ = os.WriteFile(secretsPath, data, 0600)
		return
	}
	_ = os.Remove(secretsPath)
}

func restoreLandingApply(original Config, secData []byte, secExisted bool) {
	_ = configSet(original)
	restoreSecretsFile(secData, secExisted)
}

// keyHandler 手动设置各协议密钥（与 regen 的随机生成互补）。
// POST {target, method?, key?, pass?, user?}
//   - ss      : 写 SS_METHOD + SS_KEY（校验 SS2022 密钥长度）
//   - anytls  : 写 ANYTLS_PASS
//   - naive   : 写 NAIVE_USER + NAIVE_PASS
//   - socks   : 写 SOCKS_USER + SOCKS_PASS
//
// 落地服务密钥（原 anytls2）已移到 api/landings/regen（v1.5.26）。
func keyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct{ Target, Method, Key, Pass, User string }
	json.NewDecoder(r.Body).Decode(&b)
	confTarget := ""
	switch b.Target {
	case "ss":
		method := strings.TrimSpace(b.Method)
		if method == "" {
			method = "2022-blake3-aes-128-gcm"
		}
		normKey, msg := normalizeSS2022Key(method, b.Key)
		if msg != "" {
			jerr(w, 400, msg)
			return
		}
		if err := setSecret("SS_METHOD", method); err != nil {
			jerr(w, 500, "写入 SS_METHOD 失败: "+err.Error())
			return
		}
		if err := setSecret("SS_KEY", normKey); err != nil {
			jerr(w, 500, "写入 SS_KEY 失败: "+err.Error())
			return
		}
		c := configGet()
		if c.SSMethod != method {
			c.SSMethod = method
			_ = configSet(c)
		}
		confTarget = "sing-box"
	case "anytls":
		if strings.TrimSpace(b.Pass) == "" {
			jerr(w, 400, "AnyTLS 密码不能为空")
			return
		}
		if err := setSecret("ANYTLS_PASS", strings.TrimSpace(b.Pass)); err != nil {
			jerr(w, 500, "写入 ANYTLS_PASS 失败: "+err.Error())
			return
		}
		confTarget = "sing-box"
	case "socks":
		u := strings.TrimSpace(b.User)
		pw := strings.TrimSpace(b.Pass)
		if u == "" || pw == "" {
			jerr(w, 400, "SOCKS5 用户名和密码均不能为空")
			return
		}
		if strings.ContainsAny(u, ": \t\r\n") || strings.ContainsAny(pw, ": \t\r\n") {
			jerr(w, 400, "SOCKS5 用户名/密码不能包含冒号或空白字符")
			return
		}
		if err := setSecret("SOCKS_USER", u); err != nil {
			jerr(w, 500, "写入 SOCKS_USER 失败: "+err.Error())
			return
		}
		if err := setSecret("SOCKS_PASS", pw); err != nil {
			jerr(w, 500, "写入 SOCKS_PASS 失败: "+err.Error())
			return
		}
		confTarget = "sing-box"
	case "naive":
		u := strings.TrimSpace(b.User)
		pw := strings.TrimSpace(b.Pass)
		if u == "" || pw == "" {
			jerr(w, 400, "NaiveProxy 用户名和密码均不能为空")
			return
		}
		// v1.5.23：NaiveProxy 凭证写入 Caddyfile 的 basic_auth 指令，
		// 含空格/制表符/换行/{ }  会破坏 Caddyfile 语法 → caddy 重启失败
		// （用户表现为「改密码/重装后 Naive 无法运行」）。前端 🎲 生成的凭证
		// 都是纯字母数字，此校验主要拦截用户手动输入的特殊字符。
		if strings.ContainsAny(u, " \t\r\n{}") || strings.ContainsAny(pw, " \t\r\n{}") {
			jerr(w, 400, "NaiveProxy 用户名/密码不能包含空格、制表符、换行或花括号 {}（会破坏 Caddyfile 语法）")
			return
		}
		if err := setSecret("NAIVE_USER", u); err != nil {
			jerr(w, 500, "写入 NAIVE_USER 失败: "+err.Error())
			return
		}
		if err := setSecret("NAIVE_PASS", pw); err != nil {
			jerr(w, 500, "写入 NAIVE_PASS 失败: "+err.Error())
			return
		}
		confTarget = "caddy"
	default:
		jerr(w, 400, "target 必须为 ss/anytls/socks/naive（落地服务密钥请用 api/landings/regen）")
		return
	}
	// v1.5.24：改为同步 genconf + restart + 验证 active（替代旧的 fire-and-forget）。
	// 旧版异步执行且忽略错误，caddy 重启失败（deactivating）或 genconf 失败时
	// 用户仍收到 {ok:true}，导致 secrets.env 已改但 Caddyfile 没跟上 → 不一致。
	// NaiveProxy 的 probe_resistance 会让这种不一致极具迷惑性：
	// 「反代网站能打开（伪装生效）但代理不能用（凭证不匹配静默走伪装）」。
	// 现在同步执行并返回真实结果，失败时给出明确诊断。
	if active, err := genconfRestartVerify(confTarget); err != nil {
		log.Printf("keyHandler: 应用 %s 配置失败: %v（active=%s）", confTarget, err, active)
		jwrite(w, 500, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"hint":  "凭证已保存到 secrets.env 但服务未能正常重启应用新配置。请检查 journalctl -u " + confTarget + " 的错误日志。",
			"uris":  buildURIs(configGet(), readSecrets()),
		})
		return
	}
	c := configGet()
	sec := readSecrets()
	jwrite(w, 200, map[string]any{"ok": true, "uris": buildURIs(c, sec)})
}

// ===================== 证书 =====================

// dynuConfigured 报告当前是否已配置 Dynu 凭证（任一路径齐全即视为可签发）。
// 路径 A：dynu_api_key；路径 B：dynu_client_id + dynu_secret。
func dynuConfigured(c Config) bool {
	return c.DynuAPIKey != "" || (c.DynuClientID != "" && c.DynuSecret != "")
}

// acmeEmailOrDefault 返回 acme 注册邮箱，缺省回退 admin@<域名>（与 cert-issue.sh 一致）。
func acmeEmailOrDefault(c Config) string {
	if e := strings.TrimSpace(c.AcmeEmail); e != "" {
		return e
	}
	if c.Domain != "" {
		return "admin@" + c.Domain
	}
	return "admin@example.com"
}

// writeAcmeAccountConf 把 Dynu 凭证 + 邮箱写进 acme.sh 的 account.conf，
// 使续期 cron（不带环境变量）也能读到凭证。
//
// account.conf 是 KEY='VALUE' 的 shell 脚本片段（acme.sh 用 source 加载）。
// 本函数：读取现有内容 → 按行 upsert 指定键 → 原子写回。
// 空字段不写（保留 acme.sh 已持久化的旧值，不主动清除）。
func writeAcmeAccountConf(c Config) error {
	if _, err := os.Stat(acmeAccountConf); err != nil {
		// account.conf 不存在通常意味着 acme.sh 未安装；交给 issue 流程安装后再写。
		// 不在此报错——issue 流程内部会安装 acme.sh。
	}
	kv := map[string]string{}
	if v := strings.TrimSpace(c.AcmeEmail); v != "" {
		kv["ACCOUNT_EMAIL"] = v
	}
	if v := strings.TrimSpace(c.DynuAPIKey); v != "" {
		kv["DYNU_API_KEY"] = v
	}
	if v := strings.TrimSpace(c.DynuClientID); v != "" {
		kv["Dynu_ClientId"] = v
	}
	if v := strings.TrimSpace(c.DynuSecret); v != "" {
		kv["Dynu_Secret"] = v
	}
	if len(kv) == 0 {
		return nil
	}
	data, _ := os.ReadFile(acmeAccountConf)
	lines := strings.Split(string(data), "\n")
	written := make(map[string]bool)
	var out []string
	for _, ln := range lines {
		key := ""
		if eq := strings.IndexByte(ln, '='); eq > 0 {
			key = strings.TrimSpace(ln[:eq])
		}
		if newv, ok := kv[key]; ok {
			out = append(out, fmt.Sprintf("%s='%s'", key, newv))
			written[key] = true
			continue
		}
		out = append(out, ln)
	}
	// 追加新增键（原文件没有的）
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	// 稳定顺序：ACCOUNT_EMAIL, DYNU_API_KEY, Dynu_ClientId, Dynu_Secret
	order := []string{"ACCOUNT_EMAIL", "DYNU_API_KEY", "Dynu_ClientId", "Dynu_Secret"}
	seen := map[string]bool{}
	for _, k := range order {
		if _, ok := kv[k]; ok && !written[k] && !seen[k] {
			out = append(out, fmt.Sprintf("%s='%s'", k, kv[k]))
			seen[k] = true
		}
	}
	content := strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
	tmp := acmeAccountConf + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, acmeAccountConf)
}

// runCertIssue 调用 ansgo-cert-issue.sh 签发证书（同步阻塞，约 1-3 分钟）。
// 凭证从 Config 注入环境变量（与 install.sh 同语义），不再依赖调用方传环境。
// 返回 (日志, 错误)。
func runCertIssue(c Config) (string, error) {
	// 先把凭证写进 account.conf，保证续期 cron 可用（issue 流程也会通过环境读到）。
	_ = writeAcmeAccountConf(c)

	cmd := exec.Command("bash", binCertIssue)
	cmd.Env = append(os.Environ(),
		"DOMAIN="+c.Domain,
		"EMAIL="+acmeEmailOrDefault(c),
		"DYNU_API_KEY="+c.DynuAPIKey,
		"DYNU_CLIENT_ID="+c.DynuClientID,
		"DYNU_SECRET="+c.DynuSecret,
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func certHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	jwrite(w, 200, certInfoFull(c))
}

func certRenewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	c := configGet()
	if c.CertMode == "manual" {
		jerr(w, 400, "当前为手动证书模式，无法通过 acme.sh 续期。请在服务器替换证书文件后点击「重新加载证书」。")
		return
	}
	// v1.5.27：续期前确保 account.conf 含最新凭证（manual→acme 切换后续期也能读到）。
	_ = writeAcmeAccountConf(c)
	out, err := exec.Command("sh", "-c", binAcme+" --renew -d "+c.Domain+" --ecc --force 2>&1; "+binGenConf+" all 2>/dev/null; /usr/local/bin/ansgo-cert-reload").CombinedOutput()
	if err != nil {
		jwrite(w, 500, map[string]any{"ok": false, "log": string(out)})
		return
	}
	jwrite(w, 200, map[string]any{"ok": true, "log": string(out), "cert": certInfoFull(c)})
}

// certReloadHandler 手动证书模式下，用户在服务器替换证书文件后点此触发三服务重新读取。
// 不跑 acme.sh，直接调 ansgo-cert-reload（restart caddy/sing-box/ansgo-panel）。
func certReloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	c := configGet()
	// 校验当前证书文件可读（避免路径错误导致三服务全部起不来）
	fullchain, privkey := certPaths(c)
	if _, err := os.ReadFile(fullchain); err != nil {
		jerr(w, 400, "证书文件无法读取: "+err.Error())
		return
	}
	if _, err := os.ReadFile(privkey); err != nil {
		jerr(w, 400, "私钥文件无法读取: "+err.Error())
		return
	}
	go func() {
		_ = exec.Command("/usr/local/bin/ansgo-cert-reload").Run()
	}()
	newURL := fmt.Sprintf("https://%s:%d%s", c.Domain, c.PanelPort, c.URLPath)
	scheduleSelfRestart(3)
	jwrite(w, 200, map[string]any{
		"ok":         true,
		"restart_in": 3,
		"new_url":    newURL,
		"msg":        "证书已重新加载，面板将在 3 秒后重启。",
		"cert":       certInfoFull(c),
	})
}

// certConfigHandler 读取/设置证书来源模式（acme | manual）+ Dynu 凭证（v1.5.27）
//
// GET  -> {mode, fullchain, privkey, cert_info, domain,
//
//	       dynu:{has_api_key, has_oauth, email}, dynu_configured}
//	Dynu 凭证绝不回传明文（敏感），只回传是否存在（boolean），前端据此显示「已配置/未配置」。
//
// POST -> {mode?, fullchain?, privkey?, dynu_api_key?, dynu_client_id?, dynu_secret?, acme_email?}
//   - 仅当字段非 nil 时更新；传空串可清除该字段。
//   - manual 模式校验两文件可读。
//   - 切到 acme 或更新了 Dynu 凭证时，把凭证写进 acme.sh account.conf（供续期 cron 使用）。
//   - 保存后重生成配置 + 重启 caddy/sing-box/面板。
func certConfigHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	if r.Method == "GET" {
		jwrite(w, 200, map[string]any{
			"mode":      c.CertMode,
			"fullchain": c.CertFullchain,
			"privkey":   c.CertPrivkey,
			"cert_info": certInfoFull(c),
			"domain":    c.Domain,
			"dynu": map[string]bool{
				"has_api_key": c.DynuAPIKey != "",
				"has_oauth":   c.DynuClientID != "" && c.DynuSecret != "",
			},
			"dynu_configured": dynuConfigured(c),
			"acme_email":      c.AcmeEmail,
		})
		return
	}
	// POST
	var b struct {
		Mode         *string `json:"mode"`
		Fullchain    *string `json:"fullchain"`
		Privkey      *string `json:"privkey"`
		DynuAPIKey   *string `json:"dynu_api_key"`
		DynuClientID *string `json:"dynu_client_id"`
		DynuSecret   *string `json:"dynu_secret"`
		AcmeEmail    *string `json:"acme_email"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	dynuChanged := false
	if b.Mode != nil {
		m := strings.TrimSpace(*b.Mode)
		if m != "acme" && m != "manual" {
			jerr(w, 400, "mode 必须为 acme 或 manual")
			return
		}
		c.CertMode = m
	}
	if b.Fullchain != nil {
		c.CertFullchain = strings.TrimSpace(*b.Fullchain)
	}
	if b.Privkey != nil {
		c.CertPrivkey = strings.TrimSpace(*b.Privkey)
	}
	if b.DynuAPIKey != nil {
		if v := strings.TrimSpace(*b.DynuAPIKey); v != c.DynuAPIKey {
			dynuChanged = true
		}
		c.DynuAPIKey = strings.TrimSpace(*b.DynuAPIKey)
	}
	if b.DynuClientID != nil {
		if v := strings.TrimSpace(*b.DynuClientID); v != c.DynuClientID {
			dynuChanged = true
		}
		c.DynuClientID = strings.TrimSpace(*b.DynuClientID)
	}
	if b.DynuSecret != nil {
		if v := strings.TrimSpace(*b.DynuSecret); v != c.DynuSecret {
			dynuChanged = true
		}
		c.DynuSecret = strings.TrimSpace(*b.DynuSecret)
	}
	if b.AcmeEmail != nil {
		c.AcmeEmail = strings.TrimSpace(*b.AcmeEmail)
	}
	// manual 模式必须两路径齐全且可读
	if c.CertMode == "manual" {
		if c.CertFullchain == "" || c.CertPrivkey == "" {
			jerr(w, 400, "手动模式需同时提供证书与私钥的完整文件路径")
			return
		}
		if _, err := os.ReadFile(c.CertFullchain); err != nil {
			jerr(w, 400, "证书文件无法读取: "+err.Error())
			return
		}
		if _, err := os.ReadFile(c.CertPrivkey); err != nil {
			jerr(w, 400, "私钥文件无法读取: "+err.Error())
			return
		}
	}
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存失败: "+err.Error())
		return
	}
	// v1.5.27：acme 模式且凭证有变更 → 同步写进 account.conf，确保续期 cron 可用。
	if c.CertMode == "acme" && dynuChanged && dynuConfigured(c) {
		if err := writeAcmeAccountConf(c); err != nil {
			// 写 account.conf 失败不阻断主流程（issue 时还会再写一次），仅记录。
			log.Printf("certConfig: 写 account.conf 警告: %v", err)
		}
	}
	// 证书路径/模式变化影响 caddy + sing-box + 面板自身，全部重生成并重启。
	// 面板自身因 TLS 证书也变，需要 scheduleSelfRestart 并提示前端 overlay 重新登录。
	go func() {
		_ = exec.Command(binGenConf, "all").Run()
		_ = exec.Command("systemctl", "restart", "caddy").Run()
		_ = exec.Command("systemctl", "restart", "sing-box").Run()
	}()
	newURL := fmt.Sprintf("https://%s:%d%s", c.Domain, c.PanelPort, c.URLPath)
	scheduleSelfRestart(3)
	jwrite(w, 200, map[string]any{
		"ok": true, "restart_in": 3, "new_url": newURL,
		"msg":  "证书设置已保存，三服务将在 3 秒后重启（面板会断开，请用新证书重新访问）。",
		"cert": certInfoFull(c),
	})
}

// certIssueHandler 触发一次完整 acme 签发流程（ansgo-cert-issue.sh）。
// 适用场景：manual→acme 切换后、或证书丢失/DNS 变更后，用已保存的 Dynu 凭证重新签发。
// v1.5.27：补全「首次 manual 部署后切 acme 没法签发」的遗漏。
//
// 同步阻塞执行（约 1-3 分钟 DNS-01 签发），失败返回 acme.sh 日志供前端展示。
func certIssueHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	c := configGet()
	if c.CertMode != "acme" {
		jerr(w, 400, "当前不是 acme 自动签发模式，请先在「证书来源」切到 acme 并保存。")
		return
	}
	if !dynuConfigured(c) {
		jerr(w, 400, "尚未配置 Dynu 凭证。请先填写 API Key（路径A）或 Client ID+Secret（路径B）并保存，再点签发。")
		return
	}
	out, err := runCertIssue(c)
	if err != nil {
		jwrite(w, 500, map[string]any{"ok": false, "log": out,
			"msg": "签发失败，请检查凭证与域名 DNS 是否正确指向本机。"})
		return
	}
	jwrite(w, 200, map[string]any{
		"ok": true, "log": out,
		"cert": certInfoFull(c),
		"msg":  "证书已签发并安装。如需让面板立即用新证书，请点「🔄 手动续期」或稍候自动 reload。",
	})
}

// ===================== 面板设置 =====================

func settingsHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	if r.Method == "GET" {
		ip, hint := resolveServerIP(c)
		jwrite(w, 200, map[string]any{
			"domain":               c.Domain,
			"server_ip":            c.ServerIP,
			"server_ip_resolved":   ip,
			"server_ip_hint":       hint,
			"panel_port":           c.PanelPort,
			"panel_title":          c.PanelTitle,
			"url_path":             c.URLPath,
			"admin_user":           c.AdminUser,
			"session_hours":        c.SessionHours,
			"login_lock_threshold": c.LoginLockThreshold,
			"login_lock_minutes":   c.LoginLockMinutes,
			"ss_port":              c.SSPort, "anytls_port": c.AnyTLSPort, "socks_port": c.SocksPort, "naive_port": c.NaivePort,
			"disguise_panel": c.DisguisePanel,
			"disguise_naive": c.DisguiseNaive,
			"caddy_enable":   c.CaddyEnable,
			"cert_mode":      c.CertMode,
			"cert_days_left": certInfoFull(c)["days_left"],
		})
		return
	}
	var b struct {
		URLPath            *string `json:"url_path"`
		PanelTitle         *string `json:"panel_title"`
		SessionHours       *int    `json:"session_hours"`
		AdminUser          *string `json:"admin_user"`
		AdminPass          *string `json:"admin_pass"`
		PanelPort          *int    `json:"panel_port"`
		LoginLockThreshold *int    `json:"login_lock_threshold"`
		LoginLockMinutes   *int    `json:"login_lock_minutes"`
		DisguisePanel      *string `json:"disguise_panel"`
		DisguiseNaive      *string `json:"disguise_naive"`
		ServerIP           *string `json:"server_ip"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	needRestart := false
	needCaddyReload := false
	// v1.5.21 修复：仅在 url_path 真正变化时才触发重启。
	// 前端 saveSet() 每次都把当前 url_path 原样回传（不是"改动"），旧逻辑只要
	// 字段存在就 needRestart=true，导致每次保存设置都重启面板（改个标题/IP 也重启），
	// 用户表现为「点保存无反应 / 刷新后状态异常」。改为与 PanelPort 一致的 != 守卫。
	if b.URLPath != nil {
		newPath := normalizePath(*b.URLPath)
		if newPath != c.URLPath {
			c.URLPath = newPath
			needRestart = true
		}
	}
	if b.PanelTitle != nil {
		c.PanelTitle = strings.TrimSpace(*b.PanelTitle)
		if c.PanelTitle == "" {
			c.PanelTitle = "ANS-GO 管理面板"
		}
	}
	if b.SessionHours != nil {
		c.SessionHours = clamp(*b.SessionHours, 1, 720)
	}
	if b.AdminUser != nil && strings.TrimSpace(*b.AdminUser) != "" {
		c.AdminUser = strings.TrimSpace(*b.AdminUser)
	}
	if b.AdminPass != nil && *b.AdminPass != "" {
		hash, err := bcryptHash(*b.AdminPass)
		if err != nil {
			jerr(w, 500, "密码哈希失败")
			return
		}
		c.AdminPassHash = hash
	}
	if b.PanelPort != nil && *b.PanelPort != c.PanelPort {
		if !validPort(*b.PanelPort) {
			jerr(w, 400, "面板端口非法")
			return
		}
		c.PanelPort = *b.PanelPort
		_ = exec.Command("sh", "-c", "nft add rule inet filter input tcp dport "+strconv.Itoa(c.PanelPort)+" accept 2>/dev/null || true").Run()
		needRestart = true
	}
	if b.LoginLockThreshold != nil {
		c.LoginLockThreshold = clamp(*b.LoginLockThreshold, 1, 100)
	}
	if b.LoginLockMinutes != nil {
		c.LoginLockMinutes = clamp(*b.LoginLockMinutes, 1, 1440)
	}
	validateDisguise := func(v string) bool {
		return strings.HasPrefix(v, "proxy:http") || v == "page" || v == "file" || v == "file_server"
	}
	if b.DisguisePanel != nil && *b.DisguisePanel != "" {
		if !validateDisguise(*b.DisguisePanel) {
			jerr(w, 400, "直访伪装格式错误：应为 proxy:<URL> 或 page")
			return
		}
		if c.DisguisePanel != *b.DisguisePanel {
			c.DisguisePanel = *b.DisguisePanel
			needCaddyReload = true
		}
	}
	if b.DisguiseNaive != nil && *b.DisguiseNaive != "" {
		if !validateDisguise(*b.DisguiseNaive) {
			jerr(w, 400, "Naive伪装格式错误：应为 proxy:<URL> 或 page")
			return
		}
		if c.DisguiseNaive != *b.DisguiseNaive {
			c.DisguiseNaive = *b.DisguiseNaive
			needCaddyReload = true
		}
	}
	// v1.5.18：服务器公网 IP（VPC 下手动填写）。允许空（清空则回退自动探测/域名）。
	if b.ServerIP != nil {
		ip := strings.TrimSpace(*b.ServerIP)
		if ip != "" {
			// 校验为合法 IPv4/IPv6，且非回环/链路本地
			parsed := net.ParseIP(ip)
			if parsed == nil {
				jerr(w, 400, "服务器 IP 格式非法（须为 IPv4 或 IPv6）")
				return
			}
			if parsed.IsLoopback() || parsed.IsLinkLocalUnicast() || parsed.IsLinkLocalMulticast() {
				jerr(w, 400, "服务器 IP 不能是回环或链路本地地址")
				return
			}
		}
		c.ServerIP = ip
	}
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存失败: "+err.Error())
		return
	}
	if needCaddyReload {
		go func() {
			_ = exec.Command(binGenConf, "caddy").Run()
			_ = exec.Command("systemctl", "restart", "caddy").Run()
		}()
	}
	if needRestart {
		newURL := fmt.Sprintf("https://%s:%d%s", c.Domain, c.PanelPort, c.URLPath)
		scheduleSelfRestart(3)
		jwrite(w, 200, map[string]any{"ok": true, "restart_in": 3, "new_url": newURL, "msg": "设置已保存，面板将在 3 秒后重启（如改了端口/路径需用新地址重新登录）。"})
		return
	}
	jwrite(w, 200, map[string]bool{"ok": true})
}

// detectPublicIPHandler 主动检测公网 IP（v1.5.18）。
// 仅当用户在面板设置页点「🔍 自动检测公网 IP」按钮时才触发，
// 默认不做任何外发（避免每次启动都外发，符合 §13 隐私偏好）。
// 依次尝试多个公网 echo 服务（互为兜底），取第一个有效结果返回。
// 返回的 IP 由前端填入输入框供用户确认后保存，不直接写配置。
func detectPublicIPHandler(w http.ResponseWriter, r *http.Request) {
	clients := []string{
		"https://api.ipify.org?format=text",
		"https://ifconfig.me/ip",
		"https://4.icanhazip.com",
	}
	client := &http.Client{Timeout: 6 * time.Second}
	for _, url := range clients {
		resp, err := client.Get(url)
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ip := strings.TrimSpace(string(body))
		if ip != "" && net.ParseIP(ip) != nil {
			jwrite(w, 200, map[string]any{"ok": true, "ip": ip, "source": url})
			return
		}
	}
	jerr(w, 503, "所有公网 IP 检测服务均不可达（可能服务器无法访问外网），请手动填写 IP")
}

// ===================== 日志 =====================

func logsHandler(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		target = "all"
	}
	svc := svcOfLog(target)
	n := r.URL.Query().Get("n")
	if n == "" {
		n = "200"
	}
	out, err := exec.Command("journalctl", "-u", svc, "-n", n, "--no-pager", "--output=cat").CombinedOutput()
	if err != nil && len(out) == 0 {
		jerr(w, 500, "读取日志失败")
		return
	}
	jwrite(w, 200, map[string]string{"logs": string(out)})
}

// ===================== 服务健康检测（v1.5.12）=====================

func probeLandingRemote(L LandingService, timeout time.Duration) (string, int64) {
	if !L.RemoteEnabled {
		return "skip", 0
	}
	if strings.TrimSpace(L.RemoteHost) == "" || L.RemotePort <= 0 {
		return "no", 0
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(L.RemoteHost, strconv.Itoa(L.RemotePort)), timeout)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return "no", ms
	}
	_ = conn.Close()
	return "yes", ms
}

// healthHandler 检测单个服务的运行状态：
//  1. systemd 是否 active（systemctl is-active）
//  2. 配置端口是否在 LISTEN（ss -tln）
//  3. 本机 TCP 自连能否握手（net.DialTimeout）
//
// POST {target: ss|anytls|naive|panel|caddy|group2}
// 返回 {ok, target, enabled, active, port, port_listening, tcp_connect, tcp_ms, summary}
func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct {
		Target string `json:"target"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	c := configGet()
	type svcInfo struct {
		unit, port string
		enabled    bool
	}
	info := svcInfo{}
	var landingForProbe *LandingService
	caddyActive := c.CaddyEnable == "true" || c.SvcNaiveEnabled == "true"
	switch b.Target {
	case "ss":
		info = svcInfo{"sing-box", strconv.Itoa(c.SSPort), c.SvcSSEnabled == "true"}
	case "anytls":
		info = svcInfo{"sing-box", strconv.Itoa(c.AnyTLSPort), c.SvcAnyTLSEnabled == "true"}
	case "socks":
		info = svcInfo{"sing-box", strconv.Itoa(c.SocksPort), c.SvcSocksEnabled == "true"}
	case "naive":
		info = svcInfo{"caddy", strconv.Itoa(c.NaivePort), c.SvcNaiveEnabled == "true"}
	case "panel":
		info = svcInfo{"ansgo-panel", strconv.Itoa(c.PanelPort), true}
	case "caddy":
		info = svcInfo{"caddy", "443", caddyActive}
	case "anytls2":
		// v1.5.26 兼容：旧前端书签可能仍用 anytls2，映射到 landings[0]
		if len(c.Landings) > 0 {
			landingForProbe = &c.Landings[0]
			info = svcInfo{"sing-box", strconv.Itoa(c.Landings[0].Port), c.Landings[0].Enabled}
		} else {
			info = svcInfo{"sing-box", "0", false}
		}
	default:
		// v1.5.26: target=landing-<id> 形式
		if strings.HasPrefix(b.Target, "landing-") {
			lid := strings.TrimPrefix(b.Target, "landing-")
			var L *LandingService
			for i := range c.Landings {
				if c.Landings[i].ID == lid {
					L = &c.Landings[i]
					break
				}
			}
			if L == nil {
				jerr(w, 400, "未找到落地服务: "+lid)
				return
			}
			landingForProbe = L
			info = svcInfo{"sing-box", strconv.Itoa(L.Port), L.Enabled}
		} else {
			jerr(w, 400, "target 必须为 ss/anytls/socks/naive/panel/caddy/landing-<id>")
			return
		}
	}
	active := svcActive(info.unit)
	portListening := "no"
	if port := info.port; port != "" && port != "0" {
		out, _ := exec.Command("sh", "-c", "ss -tln 2>/dev/null | awk 'NR>1{print $4}'").Output()
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasSuffix(strings.TrimSpace(line), ":"+port) {
				portListening = "yes"
				break
			}
		}
	}
	tcpConnect := "no"
	tcpMs := int64(0)
	if portListening == "yes" {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+info.port, time.Second)
		tcpMs = time.Since(start).Milliseconds()
		if err == nil {
			conn.Close()
			tcpConnect = "yes"
		}
	}
	landingRemote := "skip"
	landingRemoteMs := int64(0)
	landingRemoteAddr := ""
	if landingForProbe != nil && info.enabled && active == "active" && portListening == "yes" && tcpConnect == "yes" {
		landingRemote, landingRemoteMs = probeLandingRemote(*landingForProbe, 2*time.Second)
		if landingForProbe.RemoteEnabled {
			landingRemoteAddr = net.JoinHostPort(landingForProbe.RemoteHost, strconv.Itoa(landingForProbe.RemotePort))
		}
	}
	summary := "正常"
	if !info.enabled {
		summary = "服务未启用（请先在服务管理页安装）"
	} else if active != "active" {
		summary = info.unit + " 服务未运行（systemctl 状态: " + active + "）"
	} else if portListening != "yes" {
		summary = "端口 " + info.port + " 未监听（检查服务日志）"
	} else if tcpConnect != "yes" {
		summary = "端口监听但 TCP 握手失败（防火墙或服务异常）"
	} else if landingForProbe != nil && landingForProbe.RemoteEnabled && landingRemote != "yes" {
		summary = "本机落地入站正常，但远端落地出口不可达（" + landingRemoteAddr + "），请检查远端服务/防火墙/端口"
	}
	jwrite(w, 200, map[string]any{"ok": true, "target": b.Target, "unit": info.unit, "enabled": info.enabled, "active": active, "port": info.port, "port_listening": portListening, "tcp_connect": tcpConnect, "tcp_ms": tcpMs, "landing_remote": landingRemote, "landing_remote_ms": landingRemoteMs, "landing_remote_addr": landingRemoteAddr, "summary": summary})
}

// repairHandler 一键修复代理服务配置（v1.5.24）。
// 解决场景：NaiveProxy「反代正常但代理不能用」——通常因 secrets.env 与 Caddyfile
// 不同步（旧版 keyHandler 异步写 secrets 再 genconf，中途失败留下不一致）+
// probe_resistance 认证失败静默走伪装，极具迷惑性。
// 动作：重新从 secrets.env 生成对应服务配置（caddy/sing-box/all）+ 重启 + 验证 active。
// POST {target: caddy|sing-box|all}  默认 all。
func repairHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct{ Target string }
	json.NewDecoder(r.Body).Decode(&b)
	target := strings.TrimSpace(b.Target)
	if target == "" {
		target = "all"
	}
	if target != "caddy" && target != "sing-box" && target != "all" {
		jerr(w, 400, "target 必须为 caddy|sing-box|all")
		return
	}
	results := map[string]any{}
	anyErr := false
	targets := []string{target}
	if target == "all" {
		targets = []string{"caddy", "sing-box"}
	}
	for _, t := range targets {
		active, err := genconfRestartVerify(t)
		if err != nil {
			anyErr = true
			results[t] = map[string]any{"ok": false, "active": active, "error": err.Error()}
		} else {
			results[t] = map[string]any{"ok": true, "active": active}
		}
	}
	if anyErr {
		jwrite(w, 500, map[string]any{"ok": false, "results": results, "hint": "部分服务修复失败，查看上方 error。可 SSH 执行 ansgo-admin restart all 兜底。"})
		return
	}
	jwrite(w, 200, map[string]any{"ok": true, "results": results, "msg": "配置已从 secrets.env 重新生成并重启验证，secrets↔配置已同步。请用节点信息页的最新凭证配置客户端。"})
}

// ===================== 辅助 =====================

func svcOfLog(t string) string {
	switch t {
	case "ss", "anytls", "socks", "sing-box":
		return "sing-box"
	case "naive", "caddy":
		return "caddy"
	case "panel", "ansgo-panel":
		return "ansgo-panel"
	}
	return "ansgo-panel"
}

func systemctl(action, svc string) error {
	return exec.Command("systemctl", action, svc).Run()
}

// svcActive 返回 systemctl is-active 的真实状态文本。
// 关键：systemctl is-active 对 inactive/failed/activating 都返回非0退出码，
// 但 stdout 仍输出正确的状态字符串。早期版本因 err!=nil 丢弃 stdout 直接返回
// "unknown"，把 caddy 重启循环中的 "activating"、未启动的 "inactive"、崩溃的
// "failed" 全部归为 unknown，丢失诊断信息、误导排查（v1.5.14 修复）。
// 仅当 stdout 为空（极少见，如 unit 不存在）才回退 unknown。
func svcActive(svc string) string {
	out, err := exec.Command("systemctl", "is-active", svc).Output()
	s := strings.TrimSpace(string(out))
	if s == "" {
		if err != nil {
			return "unknown"
		}
		return "unknown"
	}
	return s
}

func validPort(p int) bool { return p > 0 && p < 65536 }

// portConflicts 检查所有本机监听端口是否有重复。
// v1.5.29：caddy / sing-box / 面板虽是独立进程，但在裸金属与 Docker host 网络下
// 处于同一网络命名空间，跨进程同端口同样会 bind: address already in use。
// 旧版只检查「同进程内部」冲突，漏掉 naive(caddy) 与 landing(sing-box) 同端口，
// 导致 caddy 进入 activating/restart loop。
// 返回冲突描述，空串表示无冲突。
func portConflicts(c Config) string {
	ports := map[int][]string{}
	add := func(port int, name string) {
		if port > 0 {
			ports[port] = append(ports[port], name)
		}
	}
	if c.PanelPort > 0 {
		add(c.PanelPort, "panel")
	}
	if c.CaddyEnable == "true" {
		add(443, ":443 伪装站")
		add(80, ":80 跳转")
	}
	if c.SvcNaiveEnabled == "true" {
		add(c.NaivePort, "naive(caddy)")
	}
	if c.SvcSSEnabled == "true" {
		add(c.SSPort, "ss(sing-box)")
	}
	if c.SvcAnyTLSEnabled == "true" {
		add(c.AnyTLSPort, "anytls(sing-box)")
	}
	if c.SvcSocksEnabled == "true" {
		add(c.SocksPort, "socks(sing-box)")
	}
	// v1.5.26: 多落地服务端口（每个启用落地的 anytls 端口）
	for _, L := range c.Landings {
		if L.Enabled {
			add(L.Port, "landing-"+L.ID+"("+L.Name+")(sing-box)")
		}
	}
	var errs []string
	for port, names := range ports {
		if len(names) > 1 {
			errs = append(errs, fmt.Sprintf("端口 %d 被 %s 同时占用（同一主机网络命名空间内不能重复监听）", port, strings.Join(names, "+")))
		}
	}
	return strings.Join(errs, "; ")
}
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---- bcrypt 包装（避免在多处 import bcrypt）----
func bcryptHash(plain string) (string, error) {
	return genBcrypt(plain)
}
func bcryptOK(hash, plain string) bool {
	return cmpBcrypt(hash, plain)
}

// ---- 密钥读取 ----
type secretData struct {
	SSMethod, SSKey, AnyTLSPass, AnyTLSUUID, SocksUser, SocksPass, NaiveUser, NaivePass string
}

func readSecrets() secretData {
	var s secretData
	data, err := os.ReadFile(secretsPath)
	if err != nil {
		return s
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		switch k {
		case "SS_METHOD":
			s.SSMethod = v
		case "SS_KEY":
			s.SSKey = v
		case "ANYTLS_PASS":
			s.AnyTLSPass = v
		case "ANYTLS_UUID":
			s.AnyTLSUUID = v
		case "SOCKS_USER":
			s.SocksUser = v
		case "SOCKS_PASS":
			s.SocksPass = v
		case "NAIVE_USER":
			s.NaiveUser = v
		case "NAIVE_PASS":
			s.NaivePass = v
		}
	}
	if s.SSMethod == "" {
		s.SSMethod = "2022-blake3-aes-128-gcm"
	}
	return s
}

// readLandingSecrets 读取单个落地服务的 anytls 凭证（v1.5.26）。
// secrets.env 里存为 LANDING_<id>_PASS / LANDING_<id>_UUID。
// 返回 (pass, uuid, found)；found=false 表示凭证尚未生成。
func readLandingSecrets(id string) (pass, uuid string, found bool) {
	data, err := os.ReadFile(secretsPath)
	if err != nil {
		return "", "", false
	}
	wantPass := "LANDING_" + id + "_PASS"
	wantUUID := "LANDING_" + id + "_UUID"
	gotPass, gotUUID := false, false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		k := strings.TrimSpace(kv[0])
		v := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		switch k {
		case wantPass:
			pass = v
			gotPass = v != ""
		case wantUUID:
			uuid = v
			gotUUID = v != ""
		}
	}
	found = gotPass && gotUUID
	return
}

// enabledLandings 返回启用的落地服务列表（v1.5.26）。
func enabledLandings(c Config) []LandingService {
	var out []LandingService
	for _, L := range c.Landings {
		if L.Enabled {
			out = append(out, L)
		}
	}
	return out
}

func buildURIs(c Config, s secretData) map[string]string {
	u := map[string]string{}
	if s.SSKey != "" {
		ui := base64.RawURLEncoding.EncodeToString([]byte(c.SSMethod + ":" + s.SSKey))
		u["ss"] = fmt.Sprintf("ss://%s@%s:%d#ANS-GO-SS", ui, c.Domain, c.SSPort)
	}
	if s.AnyTLSPass != "" {
		u["anytls"] = fmt.Sprintf("anytls://%s@%s:%d/?sni=%s#ANS-GO-AnyTLS", s.AnyTLSPass, c.Domain, c.AnyTLSPort, c.Domain)
	}
	if s.SocksUser != "" {
		u["socks"] = fmt.Sprintf("socks5://%s:%s@%s:%d#ANS-GO-SOCKS5", url.QueryEscape(s.SocksUser), url.QueryEscape(s.SocksPass), c.Domain, c.SocksPort)
	}
	if s.NaiveUser != "" {
		u["naive"] = fmt.Sprintf("naive+https://%s:%s@%s:%d#ANS-GO-Naive", url.QueryEscape(s.NaiveUser), url.QueryEscape(s.NaivePass), c.Domain, c.NaivePort)
	}
	// v1.5.26: 多落地服务 URI（key=landing-<id>）
	for _, L := range c.Landings {
		if !L.Enabled || L.Port == 0 {
			continue
		}
		pass, _, ok := readLandingSecrets(L.ID)
		if !ok {
			continue
		}
		// 名称做 URL fragment 安全处理（# 后的字符），去掉空白
		frag := "ANS-GO-Landing-" + sanitizeLandingName(L.Name)
		u["landing-"+L.ID] = fmt.Sprintf("anytls://%s@%s:%d/?sni=%s#%s", pass, c.Domain, L.Port, c.Domain, frag)
	}
	return u
}

// sanitizeLandingName 把落地服务名称里的空白/特殊字符替换，用于 URL fragment 安全。
func sanitizeLandingName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "unnamed"
	}
	var b strings.Builder
	for _, r := range name {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '#' || r == '?' || r == '/' {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---- 系统指标 ----
func memInfo() map[string]int64 {
	m := map[string]int64{}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var k string
		var v int64
		fmt.Sscanf(sc.Text(), "%s %d", &k, &v)
		switch k {
		case "MemTotal:":
			m["total"] = v
		case "MemAvailable:":
			m["avail"] = v
		}
	}
	m["used"] = m["total"] - m["avail"]
	return m
}

func loadAvg() []float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	p := strings.Fields(string(data))
	out := []float64{}
	for i := 0; i < 3 && i < len(p); i++ {
		if f, err := strconv.ParseFloat(p[i], 64); err == nil {
			out = append(out, f)
		}
	}
	return out
}

func uptimeHours() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	p := strings.Fields(string(data))
	if len(p) > 0 {
		if f, err := strconv.ParseFloat(p[0], 64); err == nil {
			return f / 3600.0
		}
	}
	return 0
}

// 统计 ESTABLISHED 的 TCP 连接数（/proc/net/tcp 状态 01）
func tcpEstabCount() int {
	f, err := os.Open("/proc/net/tcp")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) >= 4 && fields[3] == "01" {
			n++
		}
	}
	return n
}

// certInfo 解析指定证书文件（完整路径，非目录）返回到期等信息。
// 调用方需先用 certPaths(c) 取得 fullchain 完整路径再传入。
func certInfo(certFile string) map[string]any {
	info := map[string]any{}
	data, err := os.ReadFile(certFile)
	if err != nil {
		info["error"] = err.Error()
		return info
	}
	var cert0 *x509.Certificate
	rest := data
	for {
		block, r := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = r
		if block.Type == "CERTIFICATE" {
			c, err := x509.ParseCertificate(block.Bytes)
			if err == nil {
				cert0 = c
				break
			}
		}
	}
	if cert0 == nil {
		info["error"] = "无法解析证书"
		return info
	}
	days := int(time.Until(cert0.NotAfter).Hours() / 24)
	info["subject"] = cert0.Subject.CommonName
	info["issuer"] = cert0.Issuer.CommonName
	info["issuer_org"] = strings.Join(cert0.Issuer.Organization, ", ")
	info["not_before"] = cert0.NotBefore.Format("2006-01-02")
	info["not_after"] = cert0.NotAfter.Format("2006-01-02")
	info["days_left"] = days
	return info
}

// certInfoFull 按 Config 当前证书模式取完整证书路径并解析信息
func certInfoFull(c Config) map[string]any {
	fullchain, _ := certPaths(c)
	return certInfo(fullchain)
}

// ===================== 多落地服务 CRUD（v1.5.26）=====================
// 替代旧的单个硬编码 AnyTLS-2（landingHandler 远端 SS + group2Handler 启用）。
// 现在每个落地服务 = 一个 anytls 入站 + 可选一个远端出站（SS/SOCKS5）。
// 所有写操作走同步事务（v1.5.24 教训）：genconf → restart → verify active，
// 任一步失败立即报错，绝不 fire-and-forget。

// landingToMap 把一个 LandingService 转成 API 响应用的 map（含从 secrets 读出的 password/uuid）。
func landingToMap(L LandingService) map[string]any {
	pass, uuid, found := readLandingSecrets(L.ID)
	return map[string]any{
		"id":              L.ID,
		"name":            L.Name,
		"enabled":         L.Enabled,
		"port":            L.Port,
		"has_keys":        found,
		"password":        pass,
		"uuid":            uuid,
		"remote_enabled":  L.RemoteEnabled,
		"remote_type":     L.RemoteType,
		"remote_host":     L.RemoteHost,
		"remote_port":     L.RemotePort,
		"remote_method":   L.RemoteMethod,
		"remote_password": L.RemotePassword,
		"remote_user":     L.RemoteUser,
	}
}

// validateLandingRemote 校验落地服务的远端出口配置（启用时）。
// 返回错误描述，空串表示通过。
func validateLandingRemote(L *LandingService) string {
	if !L.RemoteEnabled {
		return ""
	}
	if L.RemoteHost == "" || L.RemotePort == 0 {
		return "启用远端落地需填写 host/port"
	}
	if L.RemoteType == "socks" {
		if L.RemoteUser == "" || L.RemotePassword == "" {
			return "SOCKS5 远端需填写用户名和密码"
		}
	} else { // ss（默认）
		if L.RemoteType == "" {
			L.RemoteType = "ss"
		}
		if L.RemotePassword == "" {
			return "Shadowsocks 远端需填写密钥"
		}
		// SS2022 密钥长度校验（复用 validSS2022Key）
		if strings.HasPrefix(L.RemoteMethod, "2022-blake3-aes-128") {
			if !validSS2022Key(L.RemotePassword, 16) {
				return "密钥长度错误：2022-blake3-aes-128-gcm 需 base64(16字节)=24字符的密钥"
			}
		} else if strings.HasPrefix(L.RemoteMethod, "2022-blake3-aes-256") {
			if !validSS2022Key(L.RemotePassword, 32) {
				return "密钥长度错误：2022-blake3-aes-256-gcm 需 base64(32字节)=44字符的密钥"
			}
		}
	}
	return ""
}

// landingsHandler GET 返回列表；POST 新建落地服务。
//
//	GET  -> {landings: [...]}
//	POST -> {name, port, enabled, remote_*} 创建（分配新 id + 生成凭证）
func landingsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		c := configGet()
		out := []map[string]any{}
		for _, L := range c.Landings {
			out = append(out, landingToMap(L))
		}
		jwrite(w, 200, map[string]any{"landings": out})
		return
	}
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	// POST: 新建
	var b struct {
		Name           string `json:"name"`
		Port           int    `json:"port"`
		Enabled        *bool  `json:"enabled"`
		RemoteEnabled  *bool  `json:"remote_enabled"`
		RemoteType     string `json:"remote_type"`
		RemoteHost     string `json:"remote_host"`
		RemotePort     int    `json:"remote_port"`
		RemoteMethod   string `json:"remote_method"`
		RemotePassword string `json:"remote_password"`
		RemoteUser     string `json:"remote_user"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	name := strings.TrimSpace(b.Name)
	if name == "" {
		jerr(w, 400, "名称不能为空")
		return
	}
	if b.Port < 1 || b.Port > 65535 {
		jerr(w, 400, "端口必须在 1-65535 范围")
		return
	}
	// 分配新 id（现有最大 id + 1）
	c := configGet()
	maxID := 0
	for _, L := range c.Landings {
		if n, err := strconv.Atoi(L.ID); err == nil && n > maxID {
			maxID = n
		}
	}
	newID := strconv.Itoa(maxID + 1)
	L := LandingService{
		ID:             newID,
		Name:           name,
		Enabled:        true, // 新建默认启用
		Port:           b.Port,
		RemoteType:     b.RemoteType,
		RemoteHost:     strings.TrimSpace(b.RemoteHost),
		RemotePort:     b.RemotePort,
		RemoteMethod:   b.RemoteMethod,
		RemotePassword: b.RemotePassword,
		RemoteUser:     strings.TrimSpace(b.RemoteUser),
	}
	if b.Enabled != nil {
		L.Enabled = *b.Enabled
	}
	if b.RemoteEnabled != nil {
		L.RemoteEnabled = *b.RemoteEnabled
	}
	if L.RemoteMethod == "" {
		L.RemoteMethod = "2022-blake3-aes-128-gcm"
	}
	if L.RemoteType == "" {
		L.RemoteType = "ss"
	}
	// 端口冲突预检（用新配置副本）
	trial := c
	trial.Landings = append(append([]LandingService{}, c.Landings...), L)
	if msg := portConflicts(trial); msg != "" {
		jerr(w, 400, "端口冲突: "+msg)
		return
	}
	// 远端校验
	if msg := validateLandingRemote(&L); msg != "" {
		jerr(w, 400, msg)
		return
	}
	// 生成凭证（调 ansgo-admin regen-landing <id>）
	original := cloneConfig(c)
	secData, secExisted := backupSecretsFile()
	out, err := exec.Command(binAdmin, "regen-landing", newID).CombinedOutput()
	if err != nil {
		restoreSecretsFile(secData, secExisted)
		jerr(w, 500, "生成落地凭证失败: "+string(out))
		return
	}
	c.Landings = append(c.Landings, L)
	if err := configSet(c); err != nil {
		restoreLandingApply(original, secData, secExisted)
		jerr(w, 500, "保存失败: "+err.Error())
		return
	}
	// 同步 genconf + restart + verify（v1.5.24 模式）
	if active, gerr := genconfRestartVerify("sing-box"); gerr != nil {
		restoreLandingApply(original, secData, secExisted)
		jwrite(w, 500, map[string]any{
			"ok":      false,
			"error":   gerr.Error(),
			"hint":    "落地服务创建未应用成功，已回滚 panel.json 与落地凭证；查看 journalctl -u sing-box",
			"active":  active,
			"landing": landingToMap(L),
		})
		return
	}
	jwrite(w, 200, map[string]any{"ok": true, "landing": landingToMap(L)})
}

// landingUpdateHandler 修改某个落地服务（全字段）。
// POST {id, name?, port?, enabled?, remote_*?}
func landingUpdateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct {
		ID             string  `json:"id"`
		Name           *string `json:"name"`
		Port           *int    `json:"port"`
		Enabled        *bool   `json:"enabled"`
		RemoteEnabled  *bool   `json:"remote_enabled"`
		RemoteType     *string `json:"remote_type"`
		RemoteHost     *string `json:"remote_host"`
		RemotePort     *int    `json:"remote_port"`
		RemoteMethod   *string `json:"remote_method"`
		RemotePassword *string `json:"remote_password"`
		RemoteUser     *string `json:"remote_user"`
		Password       *string `json:"password"`
	}

	json.NewDecoder(r.Body).Decode(&b)
	c := configGet()
	idx := -1
	for i := range c.Landings {
		if c.Landings[i].ID == b.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		jerr(w, 404, "未找到落地服务: "+b.ID)
		return
	}
	original := cloneConfig(c)
	secData, secExisted := backupSecretsFile()
	prev := c.Landings[idx] // 备份用于冲突回滚
	// 应用可选字段
	if b.Name != nil {
		if name := strings.TrimSpace(*b.Name); name != "" {
			c.Landings[idx].Name = name
		}
	}
	if b.Port != nil {
		if *b.Port < 1 || *b.Port > 65535 {
			jerr(w, 400, "端口必须在 1-65535 范围")
			return
		}
		c.Landings[idx].Port = *b.Port
	}
	if b.Enabled != nil {
		c.Landings[idx].Enabled = *b.Enabled
	}
	if b.RemoteEnabled != nil {
		c.Landings[idx].RemoteEnabled = *b.RemoteEnabled
	}
	if b.RemoteType != nil {
		c.Landings[idx].RemoteType = *b.RemoteType
	}
	if c.Landings[idx].RemoteType == "" {
		c.Landings[idx].RemoteType = "ss"
	}
	if b.RemoteHost != nil {
		c.Landings[idx].RemoteHost = strings.TrimSpace(*b.RemoteHost)
	}
	if b.RemotePort != nil {
		c.Landings[idx].RemotePort = *b.RemotePort
	}
	if b.RemoteMethod != nil {
		c.Landings[idx].RemoteMethod = *b.RemoteMethod
	}
	if c.Landings[idx].RemoteMethod == "" {
		c.Landings[idx].RemoteMethod = "2022-blake3-aes-128-gcm"
	}
	if b.RemotePassword != nil {
		c.Landings[idx].RemotePassword = *b.RemotePassword
	}
	if b.RemoteUser != nil {
		c.Landings[idx].RemoteUser = strings.TrimSpace(*b.RemoteUser)
	}
	if b.Password != nil && strings.TrimSpace(*b.Password) == "" {
		jerr(w, 400, "落地 AnyTLS 密码不能为空")
		return
	}
	// 远端校验

	if msg := validateLandingRemote(&c.Landings[idx]); msg != "" {
		jerr(w, 400, msg)
		return
	}
	// 端口冲突预检
	if msg := portConflicts(c); msg != "" {
		c.Landings[idx] = prev
		_ = configSet(c)
		jerr(w, 400, "端口冲突: "+msg+"。已撤销本次端口改动。")
		return
	}
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存失败: "+err.Error())
		return
	}
	if b.Password != nil {
		if err := setLandingPassword(b.ID, *b.Password); err != nil {
			restoreLandingApply(original, secData, secExisted)
			jerr(w, 500, "保存落地 AnyTLS 密码失败: "+err.Error())
			return
		}
	}
	// 同步 genconf + restart + verify

	if active, gerr := genconfRestartVerify("sing-box"); gerr != nil {
		restoreLandingApply(original, secData, secExisted)
		jwrite(w, 500, map[string]any{
			"ok":     false,
			"error":  gerr.Error(),
			"hint":   "落地服务配置未应用成功，已回滚 panel.json 与落地凭证；查看 journalctl -u sing-box",
			"active": active,
		})
		return
	}
	jwrite(w, 200, map[string]any{"ok": true, "landing": landingToMap(c.Landings[idx])})
}

// landingDeleteHandler 删除某个落地服务（含清理 secrets 凭证）。
// POST {id}
func landingDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct {
		ID string `json:"id"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	c := configGet()
	idx := -1
	for i := range c.Landings {
		if c.Landings[i].ID == b.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		jerr(w, 404, "未找到落地服务: "+b.ID)
		return
	}
	// 清理 secrets 凭证（删 LANDING_<id>_PASS/UUID 行）
	if err := removeSecretsByPrefix("LANDING_" + b.ID + "_"); err != nil {
		jerr(w, 500, "清理凭证失败: "+err.Error())
		return
	}
	c.Landings = append(c.Landings[:idx], c.Landings[idx+1:]...)
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存失败: "+err.Error())
		return
	}
	// 同步 genconf + restart + verify
	if active, gerr := genconfRestartVerify("sing-box"); gerr != nil {
		jwrite(w, 500, map[string]any{
			"ok":     false,
			"error":  gerr.Error(),
			"hint":   "落地服务已删除但 sing-box 未能正常重启，查看 journalctl -u sing-box",
			"active": active,
		})
		return
	}
	jwrite(w, 200, map[string]bool{"ok": true})
}

// landingRegenHandler 重置某个落地服务的 anytls 凭证（生成新 pass+uuid）。
// POST {id}
func landingRegenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct {
		ID string `json:"id"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	c := configGet()
	found := false
	for _, L := range c.Landings {
		if L.ID == b.ID {
			found = true
			break
		}
	}
	if !found {
		jerr(w, 404, "未找到落地服务: "+b.ID)
		return
	}
	out, err := exec.Command(binAdmin, "regen-landing", b.ID).CombinedOutput()
	if err != nil {
		jerr(w, 500, "重置凭证失败: "+string(out))
		return
	}
	// 同步 genconf + restart + verify（凭证变了，sing-box 要重新读 secrets）
	if active, gerr := genconfRestartVerify("sing-box"); gerr != nil {
		jwrite(w, 500, map[string]any{
			"ok":     false,
			"error":  gerr.Error(),
			"hint":   "凭证已重置但 sing-box 未能正常重启，查看 journalctl -u sing-box",
			"active": active,
		})
		return
	}
	// 返回更新后的落地服务（含新 password）
	var updated map[string]any
	cc := configGet()
	for _, L := range cc.Landings {
		if L.ID == b.ID {
			updated = landingToMap(L)
			break
		}
	}
	jwrite(w, 200, map[string]any{"ok": true, "landing": updated})
}

// removeSecretsByPrefix 删除 secrets.env 里所有以指定前缀开头的 key 行（如 LANDING_1_）。
// 用于删除落地服务时清理孤儿凭证。
func removeSecretsByPrefix(prefix string) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	data, err := os.ReadFile(secretsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			lines = append(lines, line)
			continue
		}
		kv := strings.SplitN(trimmed, "=", 2)
		if len(kv) == 2 && strings.HasPrefix(strings.TrimSpace(kv[0]), prefix) {
			continue // 跳过该行（删除）
		}
		lines = append(lines, line)
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	tmp := secretsPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, secretsPath)
}

// ===================== 旧落地 API 兼容（v1.5.26 deprecated）=====================
// 旧 api/landing（远端 SS）和 api/group2（启用）路由保留注册：
//   - GET  返回迁移提示 + 当前 landings[0] 状态（不报错，避免旧书签 404）
//   - POST 返回 410 Gone，引导用新 api/landings*
// 新前端不调旧路由，仅为兼容老浏览器缓存/书签。

func landingHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	if r.Method == "GET" {
		L0 := map[string]any{"enabled": false, "host": "", "port": 0, "method": "2022-blake3-aes-128-gcm", "password": ""}
		if len(c.Landings) > 0 {
			L := c.Landings[0]
			L0 = map[string]any{
				"enabled":  L.RemoteEnabled && L.RemoteType == "ss",
				"host":     L.RemoteHost,
				"port":     L.RemotePort,
				"method":   L.RemoteMethod,
				"password": L.RemotePassword,
			}
		}
		jwrite(w, 200, L0)
		return
	}
	jwrite(w, 410, map[string]any{
		"ok":         false,
		"error":      "此接口已弃用（v1.5.26），请刷新页面使用新的「落地服务」页管理",
		"deprecated": true,
	})
}

func group2Handler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	if r.Method == "GET" {
		enabled := false
		port := 0
		hasKeys := false
		landingOn := false
		if len(c.Landings) > 0 {
			L := c.Landings[0]
			enabled = L.Enabled
			port = L.Port
			_, _, hasKeys = readLandingSecrets(L.ID)
			landingOn = L.RemoteEnabled
		}
		jwrite(w, 200, map[string]any{"enabled": enabled, "anytls2_port": port, "has_keys": hasKeys, "landing_on": landingOn})
		return
	}
	jwrite(w, 410, map[string]any{
		"ok":         false,
		"error":      "此接口已弃用（v1.5.26），请刷新页面使用新的「落地服务」页管理",
		"deprecated": true,
	})
}

// ===================== 服务安装/卸载（面板内按需）=====================

// svcInstallHandler 处理单个代理服务的安装/卸载
// POST {service: ss|anytls|naive, action: install|uninstall}
// 安装：写开关=true + genconf + 若 sing-box/caddy 未运行则 enable+start
// 卸载：写开关=false + genconf + 若对应进程无其他启用服务则 stop+disable
func svcInstallHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct{ Service, Action string }
	json.NewDecoder(r.Body).Decode(&b)
	if b.Action != "install" && b.Action != "uninstall" {
		jerr(w, 400, "action 必须为 install/uninstall")
		return
	}
	c := configGet()
	original := cloneConfig(c)
	var confTarget, procName string
	switch b.Service {
	case "ss":
		c.SvcSSEnabled = boolStr(b.Action == "install")
		confTarget, procName = "sing-box", "sing-box"
	case "anytls":
		c.SvcAnyTLSEnabled = boolStr(b.Action == "install")
		confTarget, procName = "sing-box", "sing-box"
	case "socks":
		c.SvcSocksEnabled = boolStr(b.Action == "install")
		confTarget, procName = "sing-box", "sing-box"
	case "naive":
		c.SvcNaiveEnabled = boolStr(b.Action == "install")
		confTarget, procName = "caddy", "caddy"
	default:
		jerr(w, 400, "service 必须为 ss/anytls/socks/naive")
		return
	}
	if msg := portConflicts(c); msg != "" {
		jerr(w, 400, "端口冲突: "+msg)
		return
	}
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存配置失败: "+err.Error())
		return
	}
	if out, err := exec.Command(binGenConf, confTarget).CombinedOutput(); err != nil {
		_ = configSet(original)
		jerr(w, 500, "生成配置失败（已回滚服务开关，服务未重启）: "+string(out))
		return
	}
	needProc := false
	if procName == "sing-box" {
		needProc = c.SvcSSEnabled == "true" || c.SvcAnyTLSEnabled == "true" || c.SvcSocksEnabled == "true" || len(enabledLandings(c)) > 0
	} else if procName == "caddy" {
		needProc = c.CaddyEnable == "true" || c.SvcNaiveEnabled == "true"
	}
	if b.Action == "install" || needProc {
		_ = exec.Command("systemctl", "enable", procName).Run()
		restartErr := exec.Command("systemctl", "restart", procName).Run()
		// v1.5.24：验证服务真的 active（旧版无脑返回 ok，caddy deactivating 时
		// 用户以为装好了实际没跑起来）。等待最多 4 秒确认状态。
		st := ""
		for i := 0; i < 8; i++ {
			st = svcActive(procName)
			if st == "active" {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if restartErr != nil || st != "active" {
			_ = configSet(original)
			_ = exec.Command(binGenConf, confTarget).Run()
			oldNeedProc := false
			if procName == "sing-box" {
				oldNeedProc = original.SvcSSEnabled == "true" || original.SvcAnyTLSEnabled == "true" || original.SvcSocksEnabled == "true" || len(enabledLandings(original)) > 0
			} else if procName == "caddy" {
				oldNeedProc = original.CaddyEnable == "true" || original.SvcNaiveEnabled == "true"
			}
			if oldNeedProc {
				_ = exec.Command("systemctl", "restart", procName).Run()
			} else {
				_ = exec.Command("systemctl", "stop", procName).Run()
			}
			errMsg := fmt.Sprintf("%s 安装后未进入 active（当前状态 %s），可能配置错误或端口冲突", procName, st)
			if restartErr != nil {
				errMsg = fmt.Sprintf("重启 %s 失败: %v（当前状态 %s）", procName, restartErr, st)
			}
			jwrite(w, 500, map[string]any{
				"ok":      false,
				"service": b.Service,
				"error":   errMsg,
				"hint":    "已回滚服务开关并尝试恢复旧配置。查看日志：journalctl -u " + procName + " -n 30。常见原因：NaiveProxy 凭证含特殊字符、端口冲突、证书路径错误。",
			})
			return
		}
	} else {
		_ = exec.Command("systemctl", "stop", procName).Run()
		_ = exec.Command("systemctl", "disable", procName).Run()
	}
	jwrite(w, 200, map[string]any{"ok": true, "service": b.Service, "installed": b.Action == "install", "proc": procName, "proc_on": needProc || b.Action == "install"})
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
