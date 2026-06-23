package main

import (
	"bufio"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
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
	// v1.5.10: 服务状态 = enabled && 进程 active
	//   之前只看进程（SS 未装但 sing-box 因 AnyTLS 跑着 → SS 误显示 active）
	//   现在：未启用的服务显示 inactive（即使载体进程在跑）
	sbActive := svcActive("sing-box") == "active"
	caddyActive := svcActive("caddy") == "active"
	panelActive := svcActive("ansgo-panel") == "active"
	ssEnabled := c.SvcSSEnabled == "true"
	anytlsEnabled := c.SvcAnyTLSEnabled == "true"
	naiveEnabled := c.SvcNaiveEnabled == "true"
	// caddy 需要 active 的条件：
	//   默认模式（CaddyEnable=true）：caddy 跑 :443 伪装站，始终需要
	//   --no-caddy 模式（CaddyEnable=false）：caddy 只在 naive 启用时才需要
	caddyNeeded := c.CaddyEnable == "true" || naiveEnabled
	// 服务级状态：enabled && 载体进程 active
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
			"panel":    map[bool]string{true: "active", false: "inactive"}[panelActive],
			"caddy":    svcStatus(caddyNeeded, caddyActive),
			"sing-box": svcStatus(ssEnabled || anytlsEnabled, sbActive),
		},
		"svc_enabled": map[string]bool{
			"ss":     ssEnabled,
			"anytls": anytlsEnabled,
			"naive":  naiveEnabled,
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
		"cert":   certInfoFull(c),
	}
	jwrite(w, 200, resp)
}

// ===================== 节点信息 =====================

func nodeHandler(w http.ResponseWriter, r *http.Request) {
	c := configGet()
	s := readSecrets()
	uris := buildURIs(c, s)
	// v1.5.12: 每个服务对象加 enabled 字段，让前端据此决定是否渲染卡片
	// （未启用的服务不显示在节点信息页，避免空 URI 误导用户）
	resp := map[string]any{
		"domain":  c.Domain,
		"ss":      map[string]any{"uri": uris["ss"], "method": c.SSMethod, "port": c.SSPort, "password": s.SSKey, "enabled": c.SvcSSEnabled == "true", "host": c.Domain},
		"anytls":  map[string]any{"uri": uris["anytls"], "port": c.AnyTLSPort, "password": s.AnyTLSPass, "sni": c.Domain, "enabled": c.SvcAnyTLSEnabled == "true", "host": c.Domain},
		"naive":   map[string]any{"uri": uris["naive"], "port": c.NaivePort, "user": s.NaiveUser, "pass": s.NaivePass, "enabled": c.SvcNaiveEnabled == "true", "host": c.Domain},
		"group2_enabled": c.Group2Enabled == "true",
	}
	if c.Group2Enabled == "true" {
		// naive2 走 direct（caddy 无法经 sing-box ss-out），anytls2 走 ss-out 落地（架构约束，v1.5.12）
		resp["anytls2"] = map[string]any{"uri": uris["anytls2"], "port": c.AnyTLS2Port, "password": s.AnyTLS2Pass, "sni": c.Domain, "enabled": true, "host": c.Domain, "via": "ss-landing"}
		resp["naive2"] = map[string]any{"uri": uris["naive2"], "port": c.Naive2Port, "user": s.Naive2User, "pass": s.Naive2Pass, "enabled": true, "host": c.Domain, "via": "direct"}
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
	// v1.5.10: SS/AnyTLS 共享 sing-box 进程，不能直接 stop sing-box（会同时停掉另一个）
	//   新逻辑：单独停 SS/AnyTLS 时，设 enabled=false + genconf + restart sing-box
	//   （genconf 按 enabled 字段生成 inbound，停 SS 后 sing-box config 只剩 AnyTLS）
	//   只有当 SS+AnyTLS 都未启用时，才 stop+disable sing-box
	if b.Target == "ss" || b.Target == "anytls" {
		if b.Action == "stop" {
			c := configGet()
			if b.Target == "ss" {
				c.SvcSSEnabled = "false"
			} else {
				c.SvcAnyTLSEnabled = "false"
			}
			if err := configSet(c); err != nil {
				jerr(w, 500, "保存配置失败: "+err.Error())
				return
			}
			// 重新生成 sing-box config（移除被停用的 inbound）
			exec.Command(binGenConf, "sing-box").Run()
			// 检查 sing-box 是否还有启用的 inbound
			needSB := c.SvcSSEnabled == "true" || c.SvcAnyTLSEnabled == "true" || c.Group2Enabled == "true"
			if needSB {
				_ = exec.Command("systemctl", "restart", "sing-box").Run()
				jwrite(w, 200, map[string]any{"ok": true, "target": b.Target, "action": "stopped", "note": "已从 sing-box 移除，其它服务不受影响"})
			} else {
				_ = exec.Command("systemctl", "stop", "sing-box").Run()
				_ = exec.Command("systemctl", "disable", "sing-box").Run()
				jwrite(w, 200, map[string]any{"ok": true, "target": b.Target, "action": "stopped", "note": "sing-box 已停（无其他启用服务）"})
			}
			return
		}
		if b.Action == "start" {
			c := configGet()
			if b.Target == "ss" {
				c.SvcSSEnabled = "true"
			} else {
				c.SvcAnyTLSEnabled = "true"
			}
			if err := configSet(c); err != nil {
				jerr(w, 500, "保存配置失败: "+err.Error())
				return
			}
			exec.Command(binGenConf, "sing-box").Run()
			_ = exec.Command("systemctl", "enable", "sing-box").Run()
			_ = exec.Command("systemctl", "restart", "sing-box").Run()
			jwrite(w, 200, map[string]any{"ok": true, "target": b.Target, "action": "started"})
			return
		}
		// restart 走原逻辑（直接 restart sing-box）
	}
	// 其它 target（naive/panel/all）走原 systemctl 逻辑
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
//   - naive2  : 写 NAIVE2_USER + NAIVE2_PASS
func keyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct {
		Target string `json:"target"`
		Method string `json:"method"`
		Key    string `json:"key"`
		Pass   string `json:"pass"`
		User   string `json:"user"`
	}
	json.NewDecoder(r.Body).Decode(&b)

	// 各 target 的校验 + 写入分派
	confTarget := "" // "sing-box" 或 "caddy"
	switch b.Target {
	case "ss":
		method := strings.TrimSpace(b.Method)
		if method == "" {
			method = "2022-blake3-aes-128-gcm"
		}
		// 校验 SS2022 密钥长度（aes-128 -> 16 字节，aes-256 -> 32 字节）
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
		// method 也要同步到 panel.json（node URI / dashboard 展示用）
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
	case "naive":
		u := strings.TrimSpace(b.User)
		p := strings.TrimSpace(b.Pass)
		if u == "" || p == "" {
			jerr(w, 400, "NaiveProxy 用户名和密码均不能为空")
			return
		}
		if err := setSecret("NAIVE_USER", u); err != nil {
			jerr(w, 500, "写入 NAIVE_USER 失败: "+err.Error())
			return
		}
		if err := setSecret("NAIVE_PASS", p); err != nil {
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
	case "naive2":
		u := strings.TrimSpace(b.User)
		p := strings.TrimSpace(b.Pass)
		if u == "" || p == "" {
			jerr(w, 400, "NaiveProxy-2 用户名和密码均不能为空")
			return
		}
		if err := setSecret("NAIVE2_USER", u); err != nil {
			jerr(w, 500, "写入 NAIVE2_USER 失败: "+err.Error())
			return
		}
		if err := setSecret("NAIVE2_PASS", p); err != nil {
			jerr(w, 500, "写入 NAIVE2_PASS 失败: "+err.Error())
			return
		}
		confTarget = "caddy"
	default:
		jerr(w, 400, "target 必须为 ss/anytls/naive/anytls2/naive2")
		return
	}
	// 重新生成配置 + 重启对应服务（与 regen / landing 同样的 goroutine 模式）
	go func() {
		_ = exec.Command(binGenConf, confTarget).Run()
		_ = exec.Command("systemctl", "restart", confTarget).Run()
	}()
	c := configGet()
	s := readSecrets()
	jwrite(w, 200, map[string]any{
		"ok":   true,
		"uris": buildURIs(c, s),
	})
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
			"mode":       c.CertMode,
			"fullchain":  c.CertFullchain,
			"privkey":    c.CertPrivkey,
			"cert_info":  certInfoFull(c),
			"domain":     c.Domain,
		})
		return
	}
	// POST
	var b struct {
		Mode       *string `json:"mode"`
		Fullchain  *string `json:"fullchain"`
		Privkey    *string `json:"privkey"`
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
		"msg":     "证书设置已保存，三服务将在 3 秒后重启（面板会断开，请用新证书重新访问）。",
		"cert":    certInfoFull(c),
	})
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
			"cert_days_left":       certInfoFull(c)["days_left"],
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

// ===================== 服务健康检测（v1.5.12）=====================

// healthHandler 检测单个服务的运行状态：
//   1. systemd 是否 active（systemctl is-active）
//   2. 配置端口是否在 LISTEN（ss -tln）
//   3. 本机 TCP 自连能否握手（net.DialTimeout）
// POST {target: ss|anytls|naive|panel|caddy|group2}
// 返回 {ok, target, enabled, active, port, port_listening, tcp_connect, tcp_ms, summary}
func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jerr(w, 405, "方法不允许")
		return
	}
	var b struct{ Target string `json:"target"` }
	json.NewDecoder(r.Body).Decode(&b)
	c := configGet()
	// target -> (systemd 单元, 是否启用, 端口)
	type svcInfo struct {
		unit, port string
		enabled    bool
	}
	info := svcInfo{}
	caddyActive := c.CaddyEnable == "true" || c.SvcNaiveEnabled == "true" || c.Group2Enabled == "true"
	switch b.Target {
	case "ss":
		info = svcInfo{"sing-box", strconv.Itoa(c.SSPort), c.SvcSSEnabled == "true"}
	case "anytls":
		info = svcInfo{"sing-box", strconv.Itoa(c.AnyTLSPort), c.SvcAnyTLSEnabled == "true"}
	case "naive":
		info = svcInfo{"caddy", strconv.Itoa(c.NaivePort), c.SvcNaiveEnabled == "true"}
	case "panel":
		info = svcInfo{"ansgo-panel", strconv.Itoa(c.PanelPort), true}
	case "caddy":
		info = svcInfo{"caddy", "443", caddyActive}
	case "anytls2":
		info = svcInfo{"sing-box", strconv.Itoa(c.AnyTLS2Port), c.Group2Enabled == "true"}
	case "naive2":
		info = svcInfo{"caddy", strconv.Itoa(c.Naive2Port), c.Group2Enabled == "true"}
	default:
		jerr(w, 400, "target 必须为 ss/anytls/naive/panel/caddy/anytls2/naive2")
		return
	}

	// 1) systemd active
	active := svcActive(info.unit)

	// 2) 端口 LISTEN 检测（ss -tlnp，取第 4 列 local addr，匹配 :端口$）
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

	// 3) TCP 本机自连（127.0.0.1:port）握手
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

	// 综合诊断
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

	jwrite(w, 200, map[string]any{
		"ok":            true,
		"target":        b.Target,
		"unit":          info.unit,
		"enabled":       info.enabled,
		"active":        active,
		"port":          info.port,
		"port_listening": portListening,
		"tcp_connect":   tcpConnect,
		"tcp_ms":        tcpMs,
		"summary":       summary,
	})
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
		needSB := c.SvcSSEnabled == "true" || c.SvcAnyTLSEnabled == "true" || c.Group2Enabled == "true"
		if needSB {
			_ = exec.Command("systemctl", "enable", "sing-box").Run()
			_ = exec.Command("systemctl", "restart", "sing-box").Run()
		}
	}()
	// 友好提示：落地出口只对 anytls-2 生效，naive-2 走 direct（架构约束）
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
	// 启用时校验：端口必填；密钥缺失则自动生成（v1.5.12：原先报错要求用户先点
	// 「生成密钥」，但用户反馈启用后无法用 → 改为自动生成，体验更顺）
	if c.Group2Enabled == "true" {
		if c.AnyTLS2Port == 0 || c.Naive2Port == 0 {
			jerr(w, 400, "启用落地服务需填写 anytls-2 端口 / naive-2 端口")
			return
		}
		s := readSecrets()
		if s.AnyTLS2Pass == "" || s.Naive2User == "" {
			out, err := exec.Command(binAdmin, "regen2").CombinedOutput()
			if err != nil {
				jerr(w, 500, "自动生成落地服务密钥失败: "+string(out))
				return
			}
		}
	}
	if err := configSet(c); err != nil {
		jerr(w, 500, "保存失败: "+err.Error())
		return
	}
	// 状态变化需重新生成两个服务配置并重启
	// v1.5.12：启用时 sing-box 可能之前因无 inbound 被 disable，这里显式 enable
	if previouslyEnabled != (c.Group2Enabled == "true") || c.Group2Enabled == "true" {
		go func() {
			_ = exec.Command(binGenConf, "all").Run()
			_ = exec.Command("systemctl", "enable", "sing-box").Run()
			_ = exec.Command("systemctl", "restart", "sing-box").Run()
			_ = exec.Command("systemctl", "enable", "caddy").Run()
			_ = exec.Command("systemctl", "restart", "caddy").Run()
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
	// sing-box：SS/AnyTLS/Group2 任一启用就需运行
	// caddy：默认模式（CaddyEnable=true）始终需要（:443 伪装站）；--no-caddy 模式只在 naive 启用时需要
	needProc := false
	if procName == "sing-box" {
		needProc = c.SvcSSEnabled == "true" || c.SvcAnyTLSEnabled == "true" || c.Group2Enabled == "true"
	} else if procName == "caddy" {
		needProc = c.CaddyEnable == "true" || c.SvcNaiveEnabled == "true"
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
