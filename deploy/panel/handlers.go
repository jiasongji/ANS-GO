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
	case rel == "api/landing":
		requireAuth(landingHandler)(w, r)
	case rel == "api/group2":
		requireAuth(group2Handler)(w, r)
	case rel == "api/svc-install":
		requireAuth(svcInstallHandler)(w, r)
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
			"panel":    svcActive("ansgo-panel"),
			"caddy":    svcActive("caddy"),
			"sing-box": svcActive("sing-box"),
		},
		"svc_enabled": map[string]bool{
			"ss":     c.SvcSSEnabled == "true",
			"anytls": c.SvcAnyTLSEnabled == "true",
			"naive":  c.SvcNaiveEnabled == "true",
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
	resp := map[string]any{
		"domain":  c.Domain,
		"ss":      map[string]any{"uri": uris["ss"], "method": c.SSMethod, "port": c.SSPort, "password": s.SSKey},
		"anytls":  map[string]any{"uri": uris["anytls"], "port": c.AnyTLSPort, "password": s.AnyTLSPass, "sni": c.Domain},
		"naive":   map[string]any{"uri": uris["naive"], "port": c.NaivePort, "user": s.NaiveUser, "pass": s.NaivePass},
		"group2_enabled": c.Group2Enabled == "true",
	}
	if c.Group2Enabled == "true" {
		resp["anytls2"] = map[string]any{"uri": uris["anytls2"], "port": c.AnyTLS2Port, "password": s.AnyTLS2Pass, "sni": c.Domain, "via": "ss-landing"}
		resp["naive2"] = map[string]any{"uri": uris["naive2"], "port": c.Naive2Port, "user": s.Naive2User, "pass": s.Naive2Pass, "via": "ss-landing"}
	}
	jwrite(w, 200, resp)
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
		svcs = []string{"ansgo-panel"}
	case "all":
		svcs = []string{"caddy", "sing-box", "ansgo-panel"}
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

// ===================== 密钥管理（委托 ansgo-admin）=====================

func regenHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct{ Target string `json:"target"` }
	json.NewDecoder(r.Body).Decode(&b)
	if b.Target != "ss" && b.Target != "anytls" && b.Target != "naive" && b.Target != "g2" {
		jerr(w, 400, "target 必须为 ss/anytls/naive/g2")
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
	out, err := exec.Command("sh", "-c", binAcme+" --renew -d "+c.Domain+" --ecc --force 2>&1; "+binGenConf+" all 2>/dev/null; /usr/local/bin/ansgo-cert-reload").CombinedOutput()
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
			"disguise_panel":       c.DisguisePanel,
			"disguise_naive":       c.DisguiseNaive,
			"cert_days_left":       certInfo(c.CertDir)["days_left"],
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
		DisguisePanel      *string `json:"disguise_panel"`
		DisguiseNaive      *string `json:"disguise_naive"`
	}
	json.NewDecoder(r.Body).Decode(&b)

	needRestart := false
	needCaddyReload := false
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
	// 伪装站点设置（格式校验：proxy:<URL> 或 page）
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
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存失败: "+err.Error())
		return
	}
	if needCaddyReload {
		// 重新生成 caddy 配置并重启（:443 与 naive 伪装可能变了）
		go func() {
			_ = exec.Command(binGenConf, "caddy").Run()
			_ = exec.Command("systemctl", "restart", "caddy").Run()
		}()
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
	case "panel", "ansgo-panel":
		return "ansgo-panel"
	}
	return "ansgo-panel"
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
	AnyTLS2Pass, AnyTLS2UUID, Naive2User, Naive2Pass string
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
		case "ANYTLS2_PASS":
			s.AnyTLS2Pass = v
		case "ANYTLS2_UUID":
			s.AnyTLS2UUID = v
		case "NAIVE2_USER":
			s.Naive2User = v
		case "NAIVE2_PASS":
			s.Naive2Pass = v
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
	// 第2组（启用时）
	if c.Group2Enabled == "true" {
		if s.AnyTLS2Pass != "" && c.AnyTLS2Port != 0 {
			u["anytls2"] = fmt.Sprintf("anytls://%s@%s:%d/?sni=%s#ANS-GO-AnyTLS2",
				s.AnyTLS2Pass, c.Domain, c.AnyTLS2Port, c.Domain)
		}
		if s.Naive2User != "" && c.Naive2Port != 0 {
			u["naive2"] = fmt.Sprintf("naive+https://%s:%s@%s:%d#ANS-GO-Naive2",
				url.QueryEscape(s.Naive2User), url.QueryEscape(s.Naive2Pass), c.Domain, c.Naive2Port)
		}
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
	go func() {
		_ = exec.Command(binGenConf, "sing-box").Run()
		_ = exec.Command("systemctl", "restart", "sing-box").Run()
	}()
	jwrite(w, 200, map[string]bool{"ok": true})
}

// ===================== 第2组服务设置 =====================

func group2Handler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	if r.Method == "GET" {
		// 读取第2组密钥是否存在
		s := readSecrets()
		jwrite(w, 200, map[string]any{
			"enabled":       c.Group2Enabled == "true",
			"anytls2_port":  c.AnyTLS2Port,
			"naive2_port":   c.Naive2Port,
			"has_keys":      s.AnyTLS2Pass != "" && s.Naive2User != "",
			"landing_on":    c.SSLandingEnabled == "true",
			"disguise_naive2": c.DisguiseNaive2,
		})
		return
	}
	// POST
	var b struct {
		Enabled       *bool   `json:"enabled"`
		AnyTLS2Port   *int    `json:"anytls2_port"`
		Naive2Port    *int    `json:"naive2_port"`
		DisguiseNaive2 *string `json:"disguise_naive2"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	previouslyEnabled := c.Group2Enabled == "true"
	if b.Enabled != nil {
		if *b.Enabled {
			c.Group2Enabled = "true"
		} else {
			c.Group2Enabled = "false"
		}
	}
	if b.AnyTLS2Port != nil {
		c.AnyTLS2Port = clamp(*b.AnyTLS2Port, 1, 65535)
	}
	if b.Naive2Port != nil {
		c.Naive2Port = clamp(*b.Naive2Port, 1, 65535)
	}
	if b.DisguiseNaive2 != nil && *b.DisguiseNaive2 != "" {
		c.DisguiseNaive2 = *b.DisguiseNaive2
	}
	// 启用时校验：密钥需已生成 + 端口必填
	if c.Group2Enabled == "true" {
		s := readSecrets()
		if s.AnyTLS2Pass == "" || s.Naive2User == "" {
			jerr(w, 400, "第2组密钥未生成，请先点击「生成第2组密钥」")
			return
		}
		if c.AnyTLS2Port == 0 || c.Naive2Port == 0 {
			jerr(w, 400, "启用第2组需填写 anytls2_port / naive2_port")
			return
		}
	}
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存失败: "+err.Error())
		return
	}
	// 状态变化需重新生成两个服务配置并重启
	if previouslyEnabled != (c.Group2Enabled == "true") || c.Group2Enabled == "true" {
		go func() {
			_ = exec.Command(binGenConf, "all").Run()
			_ = exec.Command("systemctl", "restart", "sing-box").Run()
			_ = exec.Command("systemctl", "restart", "caddy").Run()
		}()
	}
	jwrite(w, 200, map[string]bool{"ok": true})
}

// generateGroup2Keys 生成第2组密钥到 secrets.env（委托 ansgo-admin regen2）
func generateGroup2Keys() error {
	return exec.Command(binAdmin, "regen2").Run()
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
	var b struct {
		Service string `json:"service"`
		Action  string `json:"action"`
	}
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
	case "naive":
		c.SvcNaiveEnabled = boolStr(b.Action == "install")
		confTarget, procName = "caddy", "caddy"
	default:
		jerr(w, 400, "service 必须为 ss/anytls/naive")
		return
	}
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存配置失败: "+err.Error())
		return
	}
	// 重新生成配置
	if out, err := exec.Command(binGenConf, confTarget).CombinedOutput(); err != nil {
		jerr(w, 500, "生成配置失败: "+string(out))
		return
	}
	// 判断该进程是否还需要运行
	// sing-box：无任何启用 inbound 时可停
	// caddy：始终需要（:443 伪装站 + :80 跳转是面板/域名基础设施，与代理服务无关）
	needProc := false
	if procName == "sing-box" {
		needProc = c.SvcSSEnabled == "true" || c.SvcAnyTLSEnabled == "true" || c.Group2Enabled == "true"
	} else { // caddy 始终运行
		needProc = true
	}
	if b.Action == "install" || needProc {
		// 启动/重启进程
		_ = exec.Command("systemctl", "enable", procName).Run()
		_ = exec.Command("systemctl", "restart", procName).Run()
	} else {
		// 无其他启用服务，停止并 disable
		_ = exec.Command("systemctl", "stop", procName).Run()
		_ = exec.Command("systemctl", "disable", procName).Run()
	}
	jwrite(w, 200, map[string]any{
		"ok":        true,
		"service":   b.Service,
		"installed": b.Action == "install",
		"proc":      procName,
		"proc_on":   needProc || b.Action == "install",
	})
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
