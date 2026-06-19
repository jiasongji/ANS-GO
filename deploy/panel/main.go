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

	_ "modernc.org/sqlite"
	"golang.org/x/crypto/bcrypt"
)

// ===================== 配置 =====================

const (
	confPath    = "/etc/ansgo/panel.json"
	secretsPath = "/etc/ansgo/secrets.env"
	defCertDir  = "/etc/ssl/ansgo"
)

type Config struct {
	Domain             string `json:"domain"`
	PanelPort          int    `json:"panel_port"`
	URLPath            string `json:"url_path"`
	AdminUser          string `json:"admin_user"`
	AdminPassHash      string `json:"admin_pass_hash"`
	SessionHours       int    `json:"session_hours"`
	LoginLockThreshold int    `json:"login_lock_threshold"`
	LoginLockMinutes   int    `json:"login_lock_minutes"`
	SSPort             int    `json:"ss_port"`
	SSMethod           string `json:"ss_method"`
	AnyTLSPort         int    `json:"anytls_port"`
	NaivePort          int    `json:"naive_port"`
	DisguisePanel      string `json:"disguise_panel"`
	DisguiseNaive      string `json:"disguise_naive"`
	DisguiseNaive2     string `json:"disguise_naive2"`
	// 第2组额外服务（用字符串 "true"/"false" 以兼容 genconf 的宽松判断）
	Group2Enabled      string `json:"group2_enabled"`
	AnyTLS2Port        int    `json:"anytls2_port"`
	Naive2Port         int    `json:"naive2_port"`
	// 落地 SS 出口（第2组流量走这里）
	SSLandingEnabled   string `json:"ss_landing_enabled"`
	SSLandingHost      string `json:"ss_landing_host"`
	SSLandingPort      int    `json:"ss_landing_port"`
	SSLandingMethod    string `json:"ss_landing_method"`
	SSLandingPassword  string `json:"ss_landing_password"`
	CertDir            string `json:"cert_dir"`
	DBPath             string `json:"db_path"`
}

var (
	cfg     Config
	cfgMu   sync.RWMutex
	db      *sql.DB
	version = "1.0.0"
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
		c.DisguisePanel = "proxy:https://soft.xiaoz.org"
	}
	if c.DisguiseNaive == "" {
		c.DisguiseNaive = "proxy:https://soft.xiaoz.org"
	}
	if c.CertDir == "" {
		c.CertDir = defCertDir
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
	certFile := filepath.Join(c.CertDir, "fullchain.pem")
	keyFile := filepath.Join(c.CertDir, "privkey.pem")
	if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil {
		log.Fatalf("TLS 服务失败: %v", err)
	}
}

// cliSetPass: 哈希明文密码并写入 config.json
func cliSetPass(plain string) error {
	c, err := loadConfig()
	if err != nil {
		// 配置缺失时用零值继续（允许首次设置）
		c = Config{PanelPort: 15608, SessionHours: 8, LoginLockThreshold: 5, LoginLockMinutes: 10, CertDir: defCertDir, DBPath: "/etc/ansgo/sessions.db"}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	c.AdminPassHash = string(hash)
	return saveConfig(c)
}
