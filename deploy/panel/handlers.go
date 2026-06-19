package main

import (
	"bufio"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	_ "embed"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/qrcode.min.js
var qrcodeJS []byte

const (
	binGenConf = "/usr/local/bin/bv-genconf"
	binAdmin   = "/usr/local/bin/bv-admin"
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
		w.Write(indexHTML)
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
	case rel == "api/cert":
		requireAuth(certHandler)(w, r)
	case rel == "api/cert/renew":
		requireAuth(certRenewHandler)(w, r)
	case rel == "api/settings":
		requireAuth(settingsHandler)(w, r)
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
		jwrite(w, 200, map[string]any{"authed": true, "user": c.AdminUser})
		return
	}
	jwrite(w, 200, map[string]any{"authed": false})
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
	resp := map[string]any{
		"services": map[string]string{
			"naive":    svcActive("caddy"),
			"ss":       svcActive("sing-box"),
			"anytls":   svcActive("sing-box"),
			"panel":    svcActive("bv-panel"),
			"caddy":    svcActive("caddy"),
			"sing-box": svcActive("sing-box"),
		},
		"ports": map[string]int{
			"naive": c.NaivePort, "anytls": c.AnyTLSPort,
			"ss": c.SSPort, "panel": c.PanelPort,
		},
		"domain": c.Domain,
		"url":    fmt.Sprintf("https://%s:%d%s", c.Domain, c.PanelPort, c.URLPath),
		"mem":    memInfo(),
		"load":   loadAvg(),
		"uptime": uptimeHours(),
		"tcp":    tcpEstabCount(),
		"cert":   certInfo(c.CertDir),
	}
	jwrite(w, 200, resp)
}

// ===================== 节点信息 =====================

func nodeHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	s := readSecrets()
	uris := buildURIs(c, s)
	jwrite(w, 200, map[string]any{
		"domain":  c.Domain,
		"ss":      map[string]any{"uri": uris["ss"], "method": c.SSMethod, "port": c.SSPort, "password": s.SSKey},
		"anytls":  map[string]any{"uri": uris["anytls"], "port": c.AnyTLSPort, "password": s.AnyTLSPass, "sni": c.Domain},
		"naive":   map[string]any{"uri": uris["naive"], "port": c.NaivePort, "user": s.NaiveUser, "pass": s.NaivePass},
	})
}

// ===================== 服务控制 =====================

func serviceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct {
		Target string `json:"target"`
		Action string `json:"action"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	if b.Action != "start" && b.Action != "stop" && b.Action != "restart" {
		jerr(w, 400, "action 必须为 start/stop/restart")
		return
	}
	var svcs []string
	switch b.Target {
	case "ss", "anytls":
		svcs = []string{"sing-box"}
	case "naive":
		svcs = []string{"caddy"}
	case "panel":
		svcs = []string{"bv-panel"}
	case "all":
		svcs = []string{"caddy", "sing-box", "bv-panel"}
	default:
		jerr(w, 400, "target 非法")
		return
	}
	var errs []string
	for _, s := range svcs {
		if err := systemctl(b.Action, s); err != nil {
			errs = append(errs, s+": "+err.Error())
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
		// 防火墙放行新端口（nft 可能不存在，忽略错误）
		_ = exec.Command("sh", "-c", "nft add rule inet filter input tcp dport "+strconv.Itoa(b.Port)+" accept 2>/dev/null || true").Run()
		newURL := fmt.Sprintf("https://%s:%d%s", c.Domain, b.Port, c.URLPath)
		scheduleSelfRestart(3)
		jwrite(w, 200, map[string]any{
			"ok":         true,
			"restart_in": 3,
			"new_url":    newURL,
			"msg":        fmt.Sprintf("面板将在 3 秒后重启到新端口 %d，请用新地址重新访问（会话已重置，需重新登录）。", b.Port),
		})
	case "ss":
		c.SSPort = b.Port
		if err := applyProto(c, "sing-box"); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		jwrite(w, 200, map[string]bool{"ok": true})
	case "anytls":
		c.AnyTLSPort = b.Port
		if err := applyProto(c, "sing-box"); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		jwrite(w, 200, map[string]bool{"ok": true})
	case "naive":
		c.NaivePort = b.Port
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

// ===================== 密钥管理（委托 bv-admin）=====================

func regenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct{ Target string `json:"target"` }
	json.NewDecoder(r.Body).Decode(&b)
	if b.Target != "ss" && b.Target != "anytls" && b.Target != "naive" {
		jerr(w, 400, "target 必须为 ss/anytls/naive")
		return
	}
	out, err := exec.Command(binAdmin, "regen", b.Target).CombinedOutput()
	if err != nil {
		jerr(w, 500, "regen 失败: "+string(out))
		return
	}
	// 重新读取并返回最新 URI
	c := configGet()
	s := readSecrets()
	uris := buildURIs(c, s)
	jwrite(w, 200, map[string]any{
		"ok":   true,
		"log":  string(out),
		"uris": uris,
	})
}

// ===================== 证书 =====================

func certHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	jwrite(w, 200, certInfo(c.CertDir))
}

func certRenewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	c := configGet()
	out, err := exec.Command("sh", "-c", binAcme+" --renew -d "+c.Domain+" --ecc --force 2>&1; "+binGenConf+" all 2>/dev/null; /usr/local/bin/bv-cert-reload").CombinedOutput()
	if err != nil {
		jwrite(w, 500, map[string]any{"ok": false, "log": string(out)})
		return
	}
	jwrite(w, 200, map[string]any{"ok": true, "log": string(out), "cert": certInfo(c.CertDir)})
}

// ===================== 面板设置 =====================

func settingsHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	if r.Method == "GET" {
		jwrite(w, 200, map[string]any{
			"domain":               c.Domain,
			"panel_port":           c.PanelPort,
			"url_path":             c.URLPath,
			"admin_user":           c.AdminUser,
			"session_hours":        c.SessionHours,
			"login_lock_threshold": c.LoginLockThreshold,
			"login_lock_minutes":   c.LoginLockMinutes,
			"ss_port":              c.SSPort, "anytls_port": c.AnyTLSPort, "naive_port": c.NaivePort,
			"cert_days_left": certInfo(c.CertDir)["days_left"],
		})
		return
	}
	// POST
	var b struct {
		URLPath            *string `json:"url_path"`
		SessionHours       *int    `json:"session_hours"`
		AdminUser          *string `json:"admin_user"`
		AdminPass          *string `json:"admin_pass"`
		PanelPort          *int    `json:"panel_port"`
		LoginLockThreshold *int    `json:"login_lock_threshold"`
		LoginLockMinutes   *int    `json:"login_lock_minutes"`
	}
	json.NewDecoder(r.Body).Decode(&b)

	needRestart := false
	newURL := ""
	if b.URLPath != nil {
		c.URLPath = normalizePath(*b.URLPath)
		needRestart = true
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
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存失败: "+err.Error())
		return
	}
	if needRestart {
		newURL = fmt.Sprintf("https://%s:%d%s", c.Domain, c.PanelPort, c.URLPath)
		scheduleSelfRestart(3)
		jwrite(w, 200, map[string]any{
			"ok": true, "restart_in": 3, "new_url": newURL,
			"msg": "设置已保存，面板将在 3 秒后重启（如改了端口/路径需用新地址重新登录）。",
		})
		return
	}
	jwrite(w, 200, map[string]bool{"ok": true})
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

// ===================== 辅助 =====================

func svcOfLog(t string) string {
	switch t {
	case "ss", "anytls", "sing-box":
		return "sing-box"
	case "naive", "caddy":
		return "caddy"
	case "panel", "bv-panel":
		return "bv-panel"
	}
	return "bv-panel"
}

func systemctl(action, svc string) error {
	return exec.Command("systemctl", action, svc).Run()
}

func svcActive(svc string) string {
	out, err := exec.Command("systemctl", "is-active", svc).Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func validPort(p int) bool { return p > 0 && p < 65536 }
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
	SSMethod, SSKey, AnyTLSPass, AnyTLSUUID, NaiveUser, NaivePass string
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

func buildURIs(c Config, s secretData) map[string]string {
	u := map[string]string{}
	if s.SSKey != "" {
		ui := base64.RawURLEncoding.EncodeToString([]byte(c.SSMethod + ":" + s.SSKey))
		u["ss"] = fmt.Sprintf("ss://%s@%s:%d#ANS-GO-SS", ui, c.Domain, c.SSPort)
	}
	if s.AnyTLSPass != "" {
		u["anytls"] = fmt.Sprintf("anytls://%s@%s:%d/?sni=%s#ANS-GO-AnyTLS",
			s.AnyTLSPass, c.Domain, c.AnyTLSPort, c.Domain)
	}
	if s.NaiveUser != "" {
		u["naive"] = fmt.Sprintf("naive+https://%s:%s@%s:%d#ANS-GO-Naive",
			url.QueryEscape(s.NaiveUser), url.QueryEscape(s.NaivePass), c.Domain, c.NaivePort)
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

func certInfo(certDir string) map[string]any {
	info := map[string]any{}
	certFile := certDir + "/fullchain.pem"
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
