package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// ===================== 配置 =====================

const (
	defCertDir = "/etc/ssl/ansgo"
)

// confPath / secretsPath 设为变量而非常量，便于测试覆盖路径（生产运行时值不变）。
var (
	confPath    = "/etc/ansgo/panel.json"
	secretsPath = "/etc/ansgo/secrets.env"
)

type Config struct {
	Domain             string `json:"domain"`
	ServerIP           string `json:"server_ip"` // v1.5.18：用户手动填写的公网 IP（VPC 网络下 UDP 探测只能拿内网，此字段优先级最高）
	PanelPort          int    `json:"panel_port"`
	PanelTitle         string `json:"panel_title"`
	URLPath            string `json:"url_path"`
	AdminUser          string `json:"admin_user"`
	AdminPassHash      string `json:"admin_pass_hash"`
	SessionHours       int    `json:"session_hours"`
	LoginLockThreshold int    `json:"login_lock_threshold"`
	LoginLockMinutes   int    `json:"login_lock_minutes"`
	SSPort             int    `json:"ss_port"`
	SSMethod           string `json:"ss_method"`
	AnyTLSPort         int    `json:"anytls_port"`
	SocksPort          int    `json:"socks_port"`
	NaivePort          int    `json:"naive_port"`
	DisguisePanel      string `json:"disguise_panel"`
	DisguiseNaive      string `json:"disguise_naive"`
	DisguiseNaive2     string `json:"disguise_naive2"`
	// 服务开关（面板内按需安装；"true"/"false" 字符串兼容 genconf）
	SvcSSEnabled     string `json:"svc_ss_enabled"`
	SvcAnyTLSEnabled string `json:"svc_anytls_enabled"`
	SvcSocksEnabled  string `json:"svc_socks_enabled"`
	SvcNaiveEnabled  string `json:"svc_naive_enabled"`
	// v1.5.10: caddy_enable=false 表示 --no-caddy 模式（caddy 不跑 80/443 伪装站）
	//   空字符串视为 true（兼容旧部署）
	CaddyEnable string `json:"caddy_enable"`
	// v1.5.26: 多落地服务（替代旧的 group2_*/ss_landing_* 扁平字段）。
	//   每个落地服务 = 一个 anytls 入站 + 可选一个远端出站（SS/SOCKS5）。
	//   旧的 group2_enabled/anytls2_port/naive2_port/ss_landing_* 由 upgrade.sh
	//   迁移为 landings[0]，迁移后这些旧字段从 panel.json 移除。
	Landings []LandingService `json:"landings"`
	// 证书来源：cert_mode="acme"(默认) 走 cert_dir/fullchain.pem+privkey.pem；
	// "manual" 用 cert_fullchain/cert_privkey 两个绝对路径（与 acme 二选一）
	CertMode      string `json:"cert_mode"`
	CertDir       string `json:"cert_dir"`
	CertFullchain string `json:"cert_fullchain"`
	CertPrivkey   string `json:"cert_privkey"`
	// v1.5.27：Dynu DNS-01 凭证 + acme 注册邮箱。首次以 manual 模式部署的服务器，
	// 之后在面板切到 acme 时，之前没有地方填这些凭证 → 自动签发/续期都会失败。
	// 这三个字段把 Dynu 凭证收进面板（凭证文件 0600 root-only，与 secrets.env 同口径），
	// 让 manual→acme 的切换能真正完成签发；切回 manual 时也不清空（保留以便切回）。
	// 凭证经 maskDynuSecret() 写入 acme.sh 的 account.conf，续期 cron 无需再传环境变量。
	DynuAPIKey   string `json:"dynu_api_key"`
	DynuClientID string `json:"dynu_client_id"`
	DynuSecret   string `json:"dynu_secret"`
	AcmeEmail    string `json:"acme_email"`
	DBPath       string `json:"db_path"`
}

// LandingService 一个落地服务 = 一个 anytls 入站 + 可选一个远端出站（v1.5.26）。
// 替代旧的单个硬编码 AnyTLS-2：现在可创建多个，每个有独立端口/凭证/远端配置。
//
//   - ID      : 内部标识（如 "1","2"），用作 sing-box tag 后缀（landing-in-<id>）
//   - secrets.env key 前缀（LANDING_<id>_PASS/UUID）。新建时取 max+1，删除不回收。
//   - Enabled : 是否启用该 anytls 入站（false 则 genconf 不生成对应 inbound）。
//   - Remote* : 远端落地出口（独立开关）。RemoteEnabled=false 时该落地走 sing-box direct。
//     RemoteType="ss"（shadowsocks）或 "socks"（socks5），字段按类型复用：
//     SS 用 RemoteMethod + RemotePassword；SOCKS5 用 RemoteUser + RemotePassword。
//     远端凭证明文存 panel.json（与旧 SSLandingPassword 一致，文件已 0600 root-only）。
type LandingService struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`

	RemoteEnabled  bool   `json:"remote_enabled"`
	RemoteType     string `json:"remote_type"`
	RemoteHost     string `json:"remote_host"`
	RemotePort     int    `json:"remote_port"`
	RemoteMethod   string `json:"remote_method"`
	RemotePassword string `json:"remote_password"`
	RemoteUser     string `json:"remote_user"`
}

var (
	cfg     Config
	cfgMu   sync.RWMutex
	db      *sql.DB
	version = "1.5.28"
)

func loadConfig() (Config, error) {
	var c Config
	data, err := os.ReadFile(confPath)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	// 默认值兜底
	if c.PanelTitle == "" {
		c.PanelTitle = "ANS-GO 管理面板"
	}
	if c.SocksPort <= 0 {
		c.SocksPort = 10808
	}
	if c.SvcSocksEnabled == "" {
		c.SvcSocksEnabled = "false"
	}
	if c.SessionHours <= 0 {
		c.SessionHours = 8
	}
	if c.LoginLockThreshold <= 0 {
		c.LoginLockThreshold = 5
	}
	if c.LoginLockMinutes <= 0 {
		c.LoginLockMinutes = 10
	}
	if c.DisguisePanel == "" {
		c.DisguisePanel = "proxy:https://example.com"
	}
	if c.DisguiseNaive == "" {
		c.DisguiseNaive = "proxy:https://example.com"
	}
	// 服务开关默认：旧配置无字段时视为已启用（向后兼容现有部署）
	if c.SvcSSEnabled == "" {
		c.SvcSSEnabled = "true"
	}
	if c.SvcAnyTLSEnabled == "" {
		c.SvcAnyTLSEnabled = "true"
	}
	if c.SvcNaiveEnabled == "" {
		c.SvcNaiveEnabled = "true"
	}
	if c.CaddyEnable == "" {
		c.CaddyEnable = "true" // 兼容旧部署：未设字段视为 caddy 启用
	}
	if c.CertDir == "" {
		c.CertDir = defCertDir
	}
	if c.CertMode == "" {
		c.CertMode = "acme"
	}
	if c.DBPath == "" {
		c.DBPath = "/etc/ansgo/sessions.db"
	}
	c.URLPath = normalizePath(c.URLPath)
	return c, nil
}

func saveConfig(c Config) error {
	c.URLPath = normalizePath(c.URLPath)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := confPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, confPath)
}

// certPaths 返回面板（及其他服务通过此函数同样口径读取）应使用的证书/私钥完整路径。
// manual 模式且两路径都已配置 -> 用绝对路径；否则回退到 cert_dir/fullchain.pem + privkey.pem。
// 全项目所有证书引用点（main.go / handlers.go / ansgo-genconf / ansgo-cert-reload）均按此语义读取。
func certPaths(c Config) (fullchain, privkey string) {
	if c.CertMode == "manual" && c.CertFullchain != "" && c.CertPrivkey != "" {
		return c.CertFullchain, c.CertPrivkey
	}
	return filepath.Join(c.CertDir, "fullchain.pem"), filepath.Join(c.CertDir, "privkey.pem")
}

// normalizePath: 形如 /xxxx/（首尾斜杠）
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "/admin"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p = p + "/"
	}
	return p
}

func configGet() Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg
}

func configSet(c Config) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if err := saveConfig(c); err != nil {
		return err
	}
	cfg = c
	return nil
}

// ===================== SQLite 存储 =====================

func initDB(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	db = d
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY, ip TEXT, created INTEGER, expires INTEGER)`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS lockout (
		ip TEXT PRIMARY KEY, fails INTEGER, locked_until INTEGER)`)
	return err
}

// ---- 会话 ----
func sessionCreate(ip string, hours int) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	now := time.Now()
	_, err := db.Exec("INSERT INTO sessions(token,ip,created,expires) VALUES(?,?,?,?)",
		tok, ip, now.Unix(), now.Add(time.Duration(hours)*time.Hour).Unix())
	return tok, err
}

func sessionValid(tok string) bool {
	if tok == "" {
		return false
	}
	var expires int64
	err := db.QueryRow("SELECT expires FROM sessions WHERE token=?", tok).Scan(&expires)
	if err != nil {
		return false
	}
	if time.Now().Unix() > expires {
		db.Exec("DELETE FROM sessions WHERE token=?", tok)
		return false
	}
	return true
}

func sessionDelete(tok string) { db.Exec("DELETE FROM sessions WHERE token=?", tok) }

// ---- 登录锁定（按 IP）----
func lockStatus(ip string) (fails int, lockedUntil int64) {
	db.QueryRow("SELECT fails, locked_until FROM lockout WHERE ip=?", ip).Scan(&fails, &lockedUntil)
	return
}

func lockRecordFail(ip string, threshold, lockMin int) (bool, error) {
	// returns isLocked
	now := time.Now().Unix()
	var fails int
	var until int64
	err := db.QueryRow("SELECT fails FROM lockout WHERE ip=?", ip).Scan(&fails)
	if err == sql.ErrNoRows {
		fails = 0
	} else if err != nil {
		return false, err
	}
	fails++
	locked := fails >= threshold
	if locked {
		until = now + int64(lockMin)*60
	} else {
		until = 0
	}
	_, err = db.Exec(`INSERT INTO lockout(ip,fails,locked_until) VALUES(?,?,?)
		ON CONFLICT(ip) DO UPDATE SET fails=excluded.fails, locked_until=excluded.locked_until`,
		ip, fails, until)
	return locked, err
}

func lockReset(ip string) { db.Exec("UPDATE lockout SET fails=0, locked_until=0 WHERE ip=?", ip) }

// ===================== HTTP: 工具 =====================

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	h := r.RemoteAddr
	if i := strings.LastIndex(h, ":"); i > 0 {
		h = h[:i]
	}
	return strings.Trim(h, "[]")
}

func setSessionCookie(w http.ResponseWriter, tok string, hours int) {
	http.SetCookie(w, &http.Cookie{
		Name: "bv_sess", Value: tok, Path: "/", HttpOnly: true,
		Secure: true, SameSite: http.SameSiteStrictMode,
		MaxAge: hours * 3600,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "bv_sess", Value: "", Path: "/", HttpOnly: true, Secure: true, MaxAge: -1})
}

func jwrite(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jerr(w http.ResponseWriter, code int, msg string) {
	jwrite(w, code, map[string]string{"error": msg})
}

// 鉴权中间件：未登录返回 401（前端据此跳登录）
func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("bv_sess")
		if err != nil || !sessionValid(c.Value) {
			jerr(w, http.StatusUnauthorized, "未登录或会话过期")
			return
		}
		next(w, r)
	}
}

// ===================== 自重启（改端口/路径/账号后）=====================

func scheduleSelfRestart(seconds int) {
	cmd := exec.Command("sh", "-c", fmt.Sprintf("sleep %d; systemctl restart ansgo-panel", seconds))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		log.Printf("scheduleSelfRestart 失败: %v", err)
	}
}

// ===================== main =====================

func main() {
	// CLI 子模式: -setpass <明文>  -> bcrypt 写入 config 并打印（供 ansgo-admin panel-pass 调用）
	setpass := flag.String("setpass", "", "设置面板密码（明文），写入 bcrypt hash 到 config.json 后退出")
	flag.Parse()

	if *setpass != "" {
		if err := cliSetPass(*setpass); err != nil {
			fmt.Fprintln(os.Stderr, "setpass 失败:", err)
			os.Exit(1)
		}
		return
	}

	c, err := loadConfig()
	if err != nil {
		log.Fatalf("读取配置失败 %s: %v", confPath, err)
	}
	cfg = c
	if err := initDB(c.DBPath); err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer db.Close()

	if c.AdminPassHash == "" || strings.HasPrefix(c.AdminPassHash, "PLACEHOLDER") {
		log.Println("警告: 管理员密码尚未设置（PLACEHOLDER），请执行 ansgo-admin panel-pass 设置。")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)

	addr := fmt.Sprintf("0.0.0.0:%d", c.PanelPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("ansgo-panel v%s 监听 https://%s%s (域名 %s)", version, addr, c.URLPath, c.Domain)
	certFile, keyFile := certPaths(c)
	if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil {
		log.Fatalf("TLS 服务失败: %v", err)
	}
}

// cliSetPass: 哈希明文密码并写入 config.json
func cliSetPass(plain string) error {
	c, err := loadConfig()
	if err != nil {
		// 配置缺失时用零值继续（允许首次设置）
		c = Config{PanelPort: 15608, SessionHours: 8, LoginLockThreshold: 5, LoginLockMinutes: 10, CertMode: "acme", CertDir: defCertDir, DBPath: "/etc/ansgo/sessions.db"}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	c.AdminPassHash = string(hash)
	return saveConfig(c)
}
