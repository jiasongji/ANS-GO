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

// TestAuthExposesVersion verifies the auth endpoint returns the panel version
// so the frontend sidebar footer can display it (v1.5.22 feature: lets users
// confirm whether an upgrade actually took effect on the server).
func TestAuthExposesVersion(t *testing.T) {
	srv, _ := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	resp, err := http.Get(srv.URL + "/admin/api/auth")
	if err != nil {
		t.Fatalf("GET auth: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	var got map[string]any
	json.Unmarshal(out, &got)
	if got["version"] == nil {
		t.Fatalf("auth response missing version field: %s", string(out))
	}
	if got["version"] != version {
		t.Errorf("auth version = %v, want %q", got["version"], version)
	}
	t.Logf("auth returned version=%v (frontend will show v%v in sidebar)", got["version"], got["version"])
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

// TestKeyHandler_RejectsNaiveCaddyfileBreakingChars is the regression test for
// v1.5.23: NaiveProxy credentials go into the Caddyfile basic_auth directive.
// A password/user containing spaces, tabs, newlines or {} breaks Caddyfile
// syntax → caddy restart fails → "Naive 无法运行". The handler must reject
// such credentials BEFORE writing them to secrets.env (mirrors SOCKS5 check).
func TestKeyHandler_RejectsNaiveCaddyfileBreakingChars(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	cases := []struct{ name, user, pass string }{
		{"password with space", "okuser", "my pass"},
		{"username with space", "my user", "okpass"},
		{"password with tab", "okuser", "my\tpass"},
		{"password with newline", "okuser", "my\npass"},
		{"password with {", "okuser", "p{ass"},
		{"password with }", "okuser", "p}ass"},
	}
	for _, tc := range cases {
		body, _ := json.Marshal(map[string]any{"target": "naive", "user": tc.user, "pass": tc.pass})
		req, _ := http.NewRequest("POST", srv.URL+"/admin/api/key", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "bv_sess", Value: cookie})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST key (%s): %v", tc.name, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		var got map[string]any
		json.Unmarshal(out, &got)
		if resp.StatusCode == 200 {
			t.Errorf("case %q: expected 400 rejection, got 200 (would break Caddyfile). body=%s", tc.name, string(out))
		}
		if resp.StatusCode == 400 && got["error"] == nil {
			t.Errorf("case %q: 400 but no error message", tc.name)
		}
		t.Logf("case %q: status=%d (correctly rejected)", tc.name, resp.StatusCode)
	}

	// Sanity: a clean alphanumeric password passes validation (not rejected with 400).
	// Note: in the test env there's no ansgo-genconf/systemctl, so genconfRestartVerify
	// returns 500 — that's expected. We only assert the password was NOT rejected at the
	// validation layer (i.e. status != 400). A 500 here means "passed validation, tried
	// to apply, genconf missing in test env" which is the correct outcome.
	body, _ := json.Marshal(map[string]any{"target": "naive", "user": "gooduser", "pass": "GoodPass2026"})
	req, _ := http.NewRequest("POST", srv.URL+"/admin/api/key", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "bv_sess", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("clean password POST: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	var got2 map[string]any
	json.Unmarshal(out, &got2)
	if resp.StatusCode == 400 {
		t.Errorf("clean alphanumeric password should pass validation (not 400), got %d: %s", resp.StatusCode, string(out))
	}
	// Verify the credential was actually written to secrets.env (proves validation passed + setSecret ran)
	sec := readSecrets()
	if sec.NaiveUser != "gooduser" || sec.NaivePass != "GoodPass2026" {
		t.Errorf("clean password not persisted to secrets.env: user=%q pass=%q", sec.NaiveUser, sec.NaivePass)
	} else {
		t.Logf("clean password passed validation + persisted to secrets.env ✓ (status %d = genconf missing in test env, expected)", resp.StatusCode)
	}
}
