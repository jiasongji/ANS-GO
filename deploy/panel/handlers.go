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
	binGenConf = "/usr/local/bin/ansgo-genconf"
	binAdmin   = "/usr/local/bin/ansgo-admin"
	binAcme    = "/root/.acme.sh/acme.sh"
)

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
	case rel == "api/settings":
		requireAuth(settingsHandler)(w, r)
	case rel == "api/detect-public-ip":
		requireAuth(detectPublicIPHandler)(w, r)
	case rel == "api/landing":
		requireAuth(landingHandler)(w, r)
	case rel == "api/group2":
		requireAuth(group2Handler)(w, r)
	case rel == "api/svc-install":
		requireAuth(svcInstallHandler)(w, r)
	case rel == "api/health":
		requireAuth(healthHandler)(w, r)
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
			"sing-box": svcStatus(ssEnabled || anytlsEnabled || socksEnabled || c.Group2Enabled == "true", sbActive),
		},
		"svc_enabled": map[string]bool{"ss": ssEnabled, "anytls": anytlsEnabled, "socks": socksEnabled, "naive": naiveEnabled},
		"ports":       map[string]int{"naive": c.NaivePort, "anytls": c.AnyTLSPort, "socks": c.SocksPort, "ss": c.SSPort, "panel": c.PanelPort},
		"domain":      c.Domain,
		"url":         fmt.Sprintf("https://%s:%d%s", c.Domain, c.PanelPort, c.URLPath),
		"mem":         memInfo(), "load": loadAvg(), "uptime": uptimeHours(), "tcp": tcpEstabCount(), "cert": certInfoFull(c),
	}
	jwrite(w, 200, resp)
}

// ===================== 节点信息 =====================

// 服务器出口 IP 解析（v1.5.18 修正 VPC 问题）。
// 优先级：① 用户在面板设置填写的 server_ip（最高，VPC 下唯一可靠来源）
//        ② UDP "连接" 8.8.8.8:80 探测本机出口 IP（仅当为公网时才采用）
//        ③ 空（前端回退域名）
// VPC/NAT 网络下，UDP 探测只能拿到内网网卡 IP（10./172.16-31./192.168./100.64.），
// 公网 IP 在 NAT 网关上做 SNAT，本机无从得知。故对内网 IP 直接丢弃并回退域名，
// 同时通过 server_ip_hint 字段告知前端「需用户手动填写公网 IP」。
// 不调任何第三方公网 API（避免默认外发 + 符合 §13 隐私偏好）。
var (
	probeIPCache  string // UDP 探测到的本机出口 IP（可能为空或内网）
	probeIPOnce   sync.Once
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
	resp := map[string]any{
		"domain":         c.Domain,
		"server_ip":      ip,
		"server_ip_hint": hint,
		"ss":             map[string]any{"uri": uris["ss"], "method": c.SSMethod, "port": c.SSPort, "password": sec.SSKey, "enabled": c.SvcSSEnabled == "true", "host": c.Domain},
		"anytls":         map[string]any{"uri": uris["anytls"], "port": c.AnyTLSPort, "password": sec.AnyTLSPass, "sni": c.Domain, "enabled": c.SvcAnyTLSEnabled == "true", "host": c.Domain},
		"socks":          map[string]any{"uri": uris["socks"], "port": c.SocksPort, "user": sec.SocksUser, "pass": sec.SocksPass, "password": sec.SocksPass, "enabled": c.SvcSocksEnabled == "true", "host": c.Domain},
		"naive":          map[string]any{"uri": uris["naive"], "port": c.NaivePort, "user": sec.NaiveUser, "pass": sec.NaivePass, "password": sec.NaivePass, "sni": c.Domain, "enabled": c.SvcNaiveEnabled == "true", "host": c.Domain},
		"group2_enabled": c.Group2Enabled == "true",
	}
	if c.Group2Enabled == "true" {
		resp["anytls2"] = map[string]any{"uri": uris["anytls2"], "port": c.AnyTLS2Port, "password": sec.AnyTLS2Pass, "sni": c.Domain, "enabled": true, "host": c.Domain, "via": "ss-landing"}
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
			needSB := c.SvcSSEnabled == "true" || c.SvcAnyTLSEnabled == "true" || c.SvcSocksEnabled == "true" || c.Group2Enabled == "true"
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

// keyHandler 手动设置各协议密钥（与 regen 的随机生成互补）。
// POST {target, method?, key?, pass?, user?}
//   - ss      : 写 SS_METHOD + SS_KEY（校验 SS2022 密钥长度）
//   - anytls  : 写 ANYTLS_PASS
//   - naive   : 写 NAIVE_USER + NAIVE_PASS
//   - anytls2 : 写 ANYTLS2_PASS
//   - socks   : 写 SOCKS_USER + SOCKS_PASS
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
		wantBytes := 0
		switch {
		case strings.HasPrefix(method, "2022-blake3-aes-128"):
			wantBytes = 16
		case strings.HasPrefix(method, "2022-blake3-aes-256"):
			wantBytes = 32
		}
		if wantBytes > 0 && !validSS2022Key(b.Key, wantBytes) {
			jerr(w, 400, fmt.Sprintf("密钥长度错误：%s 需 base64(%d字节) 的密钥", method, wantBytes))
			return
		}
		if err := setSecret("SS_METHOD", method); err != nil {
			jerr(w, 500, "写入 SS_METHOD 失败: "+err.Error())
			return
		}
		if err := setSecret("SS_KEY", b.Key); err != nil {
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
	case "anytls2":
		if strings.TrimSpace(b.Pass) == "" {
			jerr(w, 400, "AnyTLS-2 密码不能为空")
			return
		}
		if err := setSecret("ANYTLS2_PASS", strings.TrimSpace(b.Pass)); err != nil {
			jerr(w, 500, "写入 ANYTLS2_PASS 失败: "+err.Error())
			return
		}
		confTarget = "sing-box"
	default:
		jerr(w, 400, "target 必须为 ss/anytls/socks/naive/anytls2")
		return
	}
	// v1.5.23：genconf 失败（如 Caddyfile 校验未过）时不重启服务——
	// genconf 内部已回滚旧配置，服务继续用旧配置运行，避免无谓中断。
	go func() {
		if err := exec.Command(binGenConf, confTarget).Run(); err != nil {
			log.Printf("keyHandler: genconf %s 失败（%v），跳过重启 %s（旧配置已回滚保留）", confTarget, err, confTarget)
			return
		}
		_ = exec.Command("systemctl", "restart", confTarget).Run()
	}()
	c := configGet()
	sec := readSecrets()
	jwrite(w, 200, map[string]any{"ok": true, "uris": buildURIs(c, sec)})
}

// ===================== 证书 =====================

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

// certConfigHandler 读取/设置证书来源模式（acme | manual）
// GET  -> {mode, fullchain, privkey, cert_info}
// POST -> {mode, fullchain?, privkey?}；manual 模式校验两文件可读，写配置后重启三服务
func certConfigHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	if r.Method == "GET" {
		jwrite(w, 200, map[string]any{
			"mode":      c.CertMode,
			"fullchain": c.CertFullchain,
			"privkey":   c.CertPrivkey,
			"cert_info": certInfoFull(c),
			"domain":    c.Domain,
		})
		return
	}
	// POST
	var b struct {
		Mode      *string `json:"mode"`
		Fullchain *string `json:"fullchain"`
		Privkey   *string `json:"privkey"`
	}
	json.NewDecoder(r.Body).Decode(&b)
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
			"caddy_enable":  c.CaddyEnable,
			"cert_mode":     c.CertMode,
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
		info = svcInfo{"sing-box", strconv.Itoa(c.AnyTLS2Port), c.Group2Enabled == "true"}
	default:
		jerr(w, 400, "target 必须为 ss/anytls/socks/naive/panel/caddy/anytls2")
		return
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
	summary := "正常"
	if !info.enabled {
		summary = "服务未启用（请先在服务管理页安装）"
	} else if active != "active" {
		summary = info.unit + " 服务未运行（systemctl 状态: " + active + "）"
	} else if portListening != "yes" {
		summary = "端口 " + info.port + " 未监听（检查服务日志）"
	} else if tcpConnect != "yes" {
		summary = "端口监听但 TCP 握手失败（防火墙或服务异常）"
	}
	jwrite(w, 200, map[string]any{"ok": true, "target": b.Target, "unit": info.unit, "enabled": info.enabled, "active": active, "port": info.port, "port_listening": portListening, "tcp_connect": tcpConnect, "tcp_ms": tcpMs, "summary": summary})
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

// portConflicts 检查 caddy / sing-box 各自承载的端口集合内部是否有重复
// （跨进程端口重复是允许的，caddy 和 sing-box 是独立进程）。
// 返回冲突描述，空串表示无冲突。v1.5.14 新增。
//
// caddy 端口：:443 伪装站（CaddyEnable=true 时）+ naive
// sing-box 端口：ss + anytls + anytls2（启用时）
// panel 端口不属于任何载体，单独校验，不在此函数内。
func portConflicts(c Config) string {
	caddyPorts := map[int][]string{}
	addCaddy := func(port int, name string) { caddyPorts[port] = append(caddyPorts[port], name) }
	if c.CaddyEnable == "true" {
		addCaddy(443, ":443 伪装站")
	}
	if c.SvcNaiveEnabled == "true" {
		addCaddy(c.NaivePort, "naive")
	}
	sbPorts := map[int][]string{}
	addSB := func(port int, name string) { sbPorts[port] = append(sbPorts[port], name) }
	if c.SvcSSEnabled == "true" {
		addSB(c.SSPort, "ss")
	}
	if c.SvcAnyTLSEnabled == "true" {
		addSB(c.AnyTLSPort, "anytls")
	}
	if c.SvcSocksEnabled == "true" {
		addSB(c.SocksPort, "socks")
	}
	if c.Group2Enabled == "true" {
		addSB(c.AnyTLS2Port, "anytls2")
	}
	var errs []string
	for port, names := range caddyPorts {
		if len(names) > 1 {
			errs = append(errs, fmt.Sprintf("caddy 端口 %d 被 %s 同时占用", port, strings.Join(names, "+")))
		}
	}
	for port, names := range sbPorts {
		if len(names) > 1 {
			errs = append(errs, fmt.Sprintf("sing-box 端口 %d 被 %s 同时占用", port, strings.Join(names, "+")))
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
	AnyTLS2Pass, AnyTLS2UUID                                                            string
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
		case "ANYTLS2_PASS":
			s.AnyTLS2Pass = v
		case "ANYTLS2_UUID":
			s.AnyTLS2UUID = v
		}
	}
	if s.SSMethod == "" {
		s.SSMethod = "2022-blake3-aes-128-gcm"
	}
	return s
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
	if c.Group2Enabled == "true" && s.AnyTLS2Pass != "" && c.AnyTLS2Port != 0 {
		u["anytls2"] = fmt.Sprintf("anytls://%s@%s:%d/?sni=%s#ANS-GO-AnyTLS2", s.AnyTLS2Pass, c.Domain, c.AnyTLS2Port, c.Domain)
	}
	return u
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

// ===================== 落地 SS 出口设置 =====================

func landingHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	if r.Method == "GET" {
		jwrite(w, 200, map[string]any{
			"enabled":  c.SSLandingEnabled == "true",
			"host":     c.SSLandingHost,
			"port":     c.SSLandingPort,
			"method":   c.SSLandingMethod,
			"password": c.SSLandingPassword,
		})
		return
	}
	// POST
	var b struct {
		Enabled  *bool   `json:"enabled"`
		Host     *string `json:"host"`
		Port     *int    `json:"port"`
		Method   *string `json:"method"`
		Password *string `json:"password"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	if b.Enabled != nil {
		if *b.Enabled {
			c.SSLandingEnabled = "true"
		} else {
			c.SSLandingEnabled = "false"
		}
	}
	if b.Host != nil {
		c.SSLandingHost = strings.TrimSpace(*b.Host)
	}
	if b.Port != nil {
		c.SSLandingPort = clamp(*b.Port, 1, 65535)
	}
	if b.Method != nil && *b.Method != "" {
		c.SSLandingMethod = *b.Method
	}
	if b.Password != nil {
		c.SSLandingPassword = *b.Password
	}
	// 启用时校验完整性 + 密钥长度（2022-blake3-aes-128-gcm 需 16 字节密钥）
	if c.SSLandingEnabled == "true" {
		if c.SSLandingHost == "" || c.SSLandingPort == 0 || c.SSLandingPassword == "" {
			jerr(w, 400, "启用落地需填写 host/port/password")
			return
		}
		// 校验密钥：2022-blake3 方法要求原始字节长度 = 算法要求的字节数
		if strings.HasPrefix(c.SSLandingMethod, "2022-blake3-aes-128") {
			if !validSS2022Key(c.SSLandingPassword, 16) {
				jerr(w, 400, "密钥长度错误：2022-blake3-aes-128-gcm 需 base64(16字节)=24字符的密钥")
				return
			}
		} else if strings.HasPrefix(c.SSLandingMethod, "2022-blake3-aes-256") {
			if !validSS2022Key(c.SSLandingPassword, 32) {
				jerr(w, 400, "密钥长度错误：2022-blake3-aes-256-gcm 需 base64(32字节)=44字符的密钥")
				return
			}
		}
	}
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存失败: "+err.Error())
		return
	}
	// 重新生成 sing-box（ss outbound 变更）并重启
	// v1.5.12：确保 sing-box 已 enable（之前可能被 disable 了）
	go func() {
		_ = exec.Command(binGenConf, "sing-box").Run()
		// 若 sing-box 有任何启用的服务（ss/anytls/group2），确保它运行
		needSB := c.SvcSSEnabled == "true" || c.SvcAnyTLSEnabled == "true" || c.SvcSocksEnabled == "true" || c.Group2Enabled == "true"
		if needSB {
			_ = exec.Command("systemctl", "enable", "sing-box").Run()
			_ = exec.Command("systemctl", "restart", "sing-box").Run()
		}
	}()
	// 友好提示：落地出口只对 AnyTLS-2 生效，Naive 不参与落地
	note := ""
	if c.SSLandingEnabled == "true" && c.Group2Enabled != "true" {
		note = "落地 SS 已保存，但当前未启用落地服务，ss-out 不会生效"
	}
	jwrite(w, 200, map[string]any{"ok": true, "note": note})
}

// ===================== 第2组服务设置 =====================

func group2Handler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	if r.Method == "GET" {
		sec := readSecrets()
		jwrite(w, 200, map[string]any{"enabled": c.Group2Enabled == "true", "anytls2_port": c.AnyTLS2Port, "has_keys": sec.AnyTLS2Pass != "", "landing_on": c.SSLandingEnabled == "true"})
		return
	}
	var b struct {
		Enabled     *bool `json:"enabled"`
		AnyTLS2Port *int  `json:"anytls2_port"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	previouslyEnabled := c.Group2Enabled == "true"
	prevAnyTLS2Port := c.AnyTLS2Port
	if b.Enabled != nil {
		c.Group2Enabled = boolStr(*b.Enabled)
	}
	if b.AnyTLS2Port != nil {
		c.AnyTLS2Port = clamp(*b.AnyTLS2Port, 1, 65535)
	}
	if c.Group2Enabled == "true" {
		if c.AnyTLS2Port == 0 {
			jerr(w, 400, "启用落地服务需填写 anytls-2 端口")
			return
		}
		sec := readSecrets()
		if sec.AnyTLS2Pass == "" {
			out, err := exec.Command(binAdmin, "regen2").CombinedOutput()
			if err != nil {
				jerr(w, 500, "自动生成 AnyTLS-2 密钥失败: "+string(out))
				return
			}
		}
	}
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存失败: "+err.Error())
		return
	}
	if c.Group2Enabled == "true" {
		if msg := portConflicts(c); msg != "" {
			c.AnyTLS2Port = prevAnyTLS2Port
			_ = configSet(c)
			jerr(w, 400, "端口冲突: "+msg+"。已撤销本次端口改动，请先在面板修改冲突端口。")
			return
		}
	}
	if previouslyEnabled != (c.Group2Enabled == "true") || c.Group2Enabled == "true" {
		go func() {
			_ = exec.Command(binGenConf, "sing-box").Run()
			_ = exec.Command("systemctl", "enable", "sing-box").Run()
			_ = exec.Command("systemctl", "restart", "sing-box").Run()
		}()
	}
	jwrite(w, 200, map[string]bool{"ok": true})
}

// v1.5.12: 落地服务密钥自动生成已内联到 group2Handler 启用分支（缺失时直接调
// ansgo-admin regen2）。原先的 generateGroup2Keys 死代码已删除（从未被调用）。

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
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存配置失败: "+err.Error())
		return
	}
	if out, err := exec.Command(binGenConf, confTarget).CombinedOutput(); err != nil {
		jerr(w, 500, "生成配置失败: "+string(out))
		return
	}
	needProc := false
	if procName == "sing-box" {
		needProc = c.SvcSSEnabled == "true" || c.SvcAnyTLSEnabled == "true" || c.SvcSocksEnabled == "true" || c.Group2Enabled == "true"
	} else if procName == "caddy" {
		needProc = c.CaddyEnable == "true" || c.SvcNaiveEnabled == "true"
	}
	if b.Action == "install" || needProc {
		_ = exec.Command("systemctl", "enable", procName).Run()
		_ = exec.Command("systemctl", "restart", procName).Run()
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
