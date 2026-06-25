package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCertConfig_GetExposesDynuStateOnly 验证 GET /api/cert/config 只回传 Dynu 凭证的
// 「是否存在」标志，绝不回传明文凭证（敏感信息不应经 API 外泄）。v1.5.27 回归。
func TestCertConfig_GetExposesDynuStateOnly(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	// 预置：直接写一份带 Dynu 凭证的配置
	c := configGet()
	c.DynuAPIKey = "SECRET_API_KEY_VALUE"
	c.DynuClientID = "SECRET_CID"
	c.DynuSecret = "SECRET_SECRET"
	if err := configSet(c); err != nil {
		t.Fatalf("configSet: %v", err)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/admin/api/cert/config", nil)
	req.AddCookie(&http.Cookie{Name: "bv_sess", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET cert/config: %v", err)
	}
	defer resp.Body.Close()
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)

	// 必须有 dynu_configured=true
	if got["dynu_configured"] != true {
		t.Fatalf("dynu_configured 应为 true，实际: %v", got["dynu_configured"])
	}
	dynu, ok := got["dynu"].(map[string]any)
	if !ok {
		t.Fatalf("dynu 字段缺失或类型错误: %v", got["dynu"])
	}
	if dynu["has_api_key"] != true {
		t.Errorf("has_api_key 应为 true，实际: %v", dynu["has_api_key"])
	}
	if dynu["has_oauth"] != true {
		t.Errorf("has_oauth 应为 true，实际: %v", dynu["has_oauth"])
	}

	// 关键安全断言：整个响应体不得包含任何明文凭证
	raw, _ := json.Marshal(got)
	bodyStr := string(raw)
	for _, secret := range []string{"SECRET_API_KEY_VALUE", "SECRET_CID", "SECRET_SECRET"} {
		if strings.Contains(bodyStr, secret) {
			t.Errorf("安全违规：GET cert/config 响应包含明文凭证 %q\n%s", secret, bodyStr)
		}
	}
}

// TestCertConfig_PostPersistsDynuCreds 验证 POST 提交 Dynu 凭证后被持久化到配置，
// 再次 GET 时「已配置」状态翻转。v1.5.27 回归（manual 首次部署后补凭证的核心路径）。
func TestCertConfig_PostPersistsDynuCreds(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	// 初始未配置
	c := configGet()
	if dynuConfigured(c) {
		t.Fatalf("测试前提：初始应未配置 Dynu")
	}

	// 关键：测试环境无 systemctl/acme.sh，certConfigHandler POST 末尾会起 goroutine
	// 调 systemctl restart（失败被忽略）+ scheduleSelfRestart（sleep+restart，失败忽略）。
	// 这些副作用在测试环境无害，但 scheduleSelfRestart 会 fork 一个 sleep 子进程；
	// 为避免干扰，这里直接验证 configSet 路径而非走 handler。
	// 改用 handler 验证字段解析：构造 POST，断言 configSet 之前的数据正确。
	// 由于 handler 内 configSet 成功后才起副作用，我们检查落盘文件即可。

	body := map[string]any{
		"mode":           "acme",
		"dynu_api_key":   "MY_NEW_API_KEY",
		"acme_email":     "le@example.com",
		// 密码框留空语义 = 不改，这里不提交 client_id/secret
	}
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+"/admin/api/cert/config", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "bv_sess", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST cert/config: %v", err)
	}
	defer resp.Body.Close()

	// 读落盘的 panel.json 验证字段已持久化（不依赖内存 cfg）
	data, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read panel.json: %v", err)
	}
	var saved map[string]any
	json.Unmarshal(data, &saved)
	if saved["dynu_api_key"] != "MY_NEW_API_KEY" {
		t.Errorf("dynu_api_key 未持久化: %v", saved["dynu_api_key"])
	}
	if saved["acme_email"] != "le@example.com" {
		t.Errorf("acme_email 未持久化: %v", saved["acme_email"])
	}
	if saved["cert_mode"] != "acme" {
		t.Errorf("cert_mode 应为 acme: %v", saved["cert_mode"])
	}

	// 再 loadConfig 验证 dynuConfigured 现在为 true（API Key 单独齐全即可）
	c2, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if !dynuConfigured(c2) {
		t.Errorf("提交 API Key 后 dynuConfigured 应为 true")
	}
	if c2.DynuAPIKey != "MY_NEW_API_KEY" {
		t.Errorf("DynuAPIKey 内存值不符: %q", c2.DynuAPIKey)
	}

	_ = resp
}

// TestCertIssue_RejectsWhenNoDynu 验证 certIssueHandler 在未配置 Dynu 凭证时拒绝签发，
// 而不是盲目调用（不存在的）acme.sh。v1.5.27 回归。
func TestCertIssue_RejectsWhenNoDynu(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	// 确保是 acme 模式但无凭证
	c := configGet()
	c.CertMode = "acme"
	c.DynuAPIKey = ""
	c.DynuClientID = ""
	c.DynuSecret = ""
	configSet(c)

	req, _ := http.NewRequest("POST", srv.URL+"/admin/api/cert/issue", bytes.NewReader([]byte("{}")))
	req.AddCookie(&http.Cookie{Name: "bv_sess", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST cert/issue: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Fatalf("未配置 Dynu 时应返回 400，实际 %d", resp.StatusCode)
	}
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	if got["error"] == nil {
		t.Errorf("应返回 error 提示填写凭证")
	}
}

// TestWriteAcmeAccountConf_UpsertsKeys 验证 writeAcmeAccountConf 能 upsert
// Dynu 凭证键到 account.conf（已有键替换、新键追加），为续期 cron 提供凭证。v1.5.27 回归。
func TestWriteAcmeAccountConf_UpsertsKeys(t *testing.T) {
	tmp := t.TempDir()
	orig := acmeAccountConf
	acmeAccountConf = filepath.Join(tmp, "account.conf")
	defer func() { acmeAccountConf = orig }()

	// 预置已有内容（含一个将被更新的键 + 一个无关键）
	seed := "ACCOUNT_EMAIL='old@example.com'\nSAVED_KEY='keepme'\n"
	if err := os.WriteFile(acmeAccountConf, []byte(seed), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := Config{
		AcmeEmail:    "new@example.com",
		DynuAPIKey:   "FRESHKEY",
		DynuClientID: "",
		DynuSecret:   "",
	}
	if err := writeAcmeAccountConf(c); err != nil {
		t.Fatalf("writeAcmeAccountConf: %v", err)
	}
	data, _ := os.ReadFile(acmeAccountConf)
	s := string(data)
	for _, want := range []string{
		"ACCOUNT_EMAIL='new@example.com'",
		"SAVED_KEY='keepme'",
		"DYNU_API_KEY='FRESHKEY'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("account.conf 缺少 %q\n实际内容:\n%s", want, s)
		}
	}
	// 旧邮箱应被替换而非残留两行
	if strings.Contains(s, "old@example.com") {
		t.Errorf("旧邮箱未被替换（应为 new）:\n%s", s)
	}
	// 不应写入空字段（Client ID/Secret 为空）
	if strings.Contains(s, "Dynu_ClientId=''") || strings.Contains(s, "Dynu_Secret=''") {
		t.Errorf("不应写入空 Dynu 字段:\n%s", s)
	}
}
