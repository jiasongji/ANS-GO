package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// helper to set up a fresh panel state in a temp dir and return a logged-in cookie.
func setupPanel(t *testing.T) (srv *httptest.Server, cookie string) {
	t.Helper()
	tmp := t.TempDir()
	confPath = filepath.Join(tmp, "panel.json")
	secretsPath = filepath.Join(tmp, "secrets.env")
	dbFile := filepath.Join(tmp, "sessions.db")

	panelJSON := `{
  "domain": "your-domain.com",
  "panel_port": 15608,
  "panel_title": "ANS-GO 管理面板",
  "url_path": "/admin/",
  "admin_user": "admin",
  "admin_pass_hash": "PLACEHOLDER",
  "session_hours": 8,
  "login_lock_threshold": 5,
  "login_lock_minutes": 10,
  "ss_port": 33899,
  "anytls_port": 21111,
  "socks_port": 10808,
  "naive_port": 44333,
  "disguise_panel": "proxy:https://example.com",
  "disguise_naive": "proxy:https://example.com",
  "svc_ss_enabled": "false",
  "svc_anytls_enabled": "false",
  "svc_socks_enabled": "false",
  "svc_naive_enabled": "false",
  "caddy_enable": "true",
  "cert_mode": "acme",
  "cert_dir": "` + tmp + `/ssl",
  "db_path": "` + dbFile + `"
}`
	os.MkdirAll(filepath.Join(tmp, "ssl"), 0700)
	os.WriteFile(confPath, []byte(panelJSON), 0600)
	os.WriteFile(secretsPath, []byte("SS_KEY=dGVzdA==\n"), 0600)

	c, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	hash, _ := bcryptHash("testpass123")
	c.AdminPassHash = hash
	saveConfig(c)
	cfg = c
	if err := initDB(dbFile); err != nil {
		t.Fatalf("initDB: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)
	srv = httptest.NewServer(mux)

	// login
	body, _ := json.Marshal(map[string]string{"user": "admin", "pass": "testpass123"})
	resp, err := http.Post(srv.URL+"/admin/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "bv_sess" {
			cookie = ck.Value
		}
	}
	resp.Body.Close()
	if cookie == "" {
		t.Fatalf("no session cookie")
	}
	return srv, cookie
}

func postSettings(t *testing.T, srv *httptest.Server, cookie string, body map[string]any) (status int, respBody map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+"/admin/api/settings", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "bv_sess", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST settings: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	status = resp.StatusCode
	json.Unmarshal(out, &respBody)
	return
}

// TestSettingsSave_UntouchedFieldsDoNotRestart is the regression test for the bug:
// "Docker deploy, fill in panel title + custom IP, click save → no reaction / reverts".
//
// Root cause: settingsHandler set needRestart=true whenever url_path was present in the
// POST body, but the frontend ALWAYS sends url_path (current value). So every single
// settings save triggered an unnecessary panel restart (restart_in:3 + new_url overlay +
// redirect). When the user changed only title/IP, the panel restarted for no reason,
// the restart overlay flashed / redirect landed on a restarting panel (connection refused),
// and the user perceived "no reaction".
//
// Fix: only set needRestart when url_path actually CHANGES (compare normalized value),
// mirroring how PanelPort is guarded with `*b.PanelPort != c.PanelPort`.
func TestSettingsSave_UntouchedFieldsDoNotRestart(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	// Save ONLY panel_title + server_ip, url_path unchanged at /admin/
	body := map[string]any{
		"panel_title":    "我的新标题",
		"url_path":       "/admin/", // unchanged
		"admin_user":     "admin",
		"session_hours":  8,
		"panel_port":     15608, // unchanged
		"login_lock_threshold": 5,
		"login_lock_minutes":   10,
		"disguise_panel": "proxy:https://example.com",
		"disguise_naive": "proxy:https://example.com",
		"server_ip":      "203.0.113.42",
	}
	status, resp := postSettings(t, srv, cookie, body)
	t.Logf("status=%d resp=%+v", status, resp)

	// BUG: before fix, resp would contain new_url + restart_in (needRestart=true from url_path)
	if _, hasRestart := resp["restart_in"]; hasRestart {
		t.Errorf("BUG: settings save triggered an UNNECESSARY restart (restart_in present) "+
			"even though url_path/panel_port were unchanged. resp=%+v", resp)
	}

	// title + ip must persist
	c, _ := loadConfig()
	if c.PanelTitle != "我的新标题" {
		t.Errorf("panel_title not saved: %q", c.PanelTitle)
	}
	if c.ServerIP != "203.0.113.42" {
		t.Errorf("server_ip not saved: %q", c.ServerIP)
	}
}

// TestSettingsSave_UrlPathChangeStillRestarts ensures the fix didn't break the
// legitimate case: when url_path actually changes, a restart IS expected.
func TestSettingsSave_UrlPathChangeStillRestarts(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	body := map[string]any{
		"panel_title":    "标题",
		"url_path":       "/new-path/", // CHANGED from /admin/
		"admin_user":     "admin",
		"session_hours":  8,
		"panel_port":     15608,
		"login_lock_threshold": 5,
		"login_lock_minutes":   10,
		"disguise_panel": "proxy:https://example.com",
		"disguise_naive": "proxy:https://example.com",
		"server_ip":      "",
	}
	status, resp := postSettings(t, srv, cookie, body)
	t.Logf("status=%d resp=%+v", status, resp)
	if resp["restart_in"] == nil {
		t.Errorf("url_path change should trigger restart, but restart_in missing: %+v", resp)
	}
	if resp["new_url"] == nil {
		t.Errorf("url_path change should return new_url, missing: %+v", resp)
	}
	c, _ := loadConfig()
	if c.URLPath != "/new-path/" {
		t.Errorf("url_path not updated: %q", c.URLPath)
	}
}
