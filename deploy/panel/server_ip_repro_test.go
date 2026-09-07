package main

// 复现测试（先红后绿）：server_ip 已填写时，节点 URI / host 字段应优先使用连接 IP。
//
// 旧实现：buildURIs 与 nodeHandler 的 host 字段一律使用 c.Domain，
// 用户在「面板设置」填写的 server_ip 只影响节点页「连接地址」一行展示，
// 导致「连接地址显示 IP、复制出来的 URI 却是域名」不一致；
// IPv6 场景还必须给 URI authority 加 [] 括号，否则客户端解析失败。
//
// 本文件在旧实现上运行必须真实 FAIL（红灯证据），修复后原样重跑必须全部 PASS。

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFullSecrets 覆盖 secretsPath 为全量凭证（setupPanel 只写了 SS_KEY），
// 让 anytls/socks/naive/landing URI 都能生成，便于断言 host。
func writeFullSecrets(t *testing.T) {
	t.Helper()
	os.WriteFile(secretsPath, []byte(`SS_METHOD=2022-blake3-aes-128-gcm
SS_KEY=aaaaaaaaaaaaaaaaaaaaaa==
ANYTLS_PASS=atpass
ANYTLS_UUID=11111111-1111-1111-1111-111111111111
SOCKS_USER=suser
SOCKS_PASS=spass
NAIVE_USER=nuser
NAIVE_PASS=npass
LANDING_1_PASS=lp1
LANDING_1_UUID=22222222-2222-2222-2222-222222222222
`), 0600)
}

// TestRepro_BuildURIs_PreferServerIPAsHost：server_ip 为 IPv4 时，
// ss/anytls/socks/landing URI 的连接主机必须是该 IP（SNI 保留域名），naive 保留域名。
func TestRepro_BuildURIs_PreferServerIPAsHost(t *testing.T) {
	tmp := t.TempDir()
	confPath = filepath.Join(tmp, "panel.json")
	secretsPath = filepath.Join(tmp, "secrets.env")
	writeFullSecrets(t)

	os.WriteFile(confPath, []byte(`{
		"domain": "your-domain.com",
		"server_ip": "203.0.113.42",
		"panel_title": "NodeName",
		"ss_port": 33899, "anytls_port": 21111, "socks_port": 10808, "naive_port": 44333,
		"landings": [{"id":"1","name":"LD1","enabled":true,"port":21112}]
	}`), 0600)

	c, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	uris := buildURIs(c, readSecrets())

	cases := []struct{ key, wantSub string }{
		{"ss", "ss://"},
		{"anytls", "anytls://"},
		{"socks", "socks5://"},
		{"landing-1", "anytls://"},
	}
	for _, tc := range cases {
		u, ok := uris[tc.key]
		if !ok || u == "" {
			t.Errorf("%s URI 未生成（凭证已写入）", tc.key)
			continue
		}
		if !strings.Contains(u, "@203.0.113.42:") {
			t.Errorf("BUG: %s URI 连接主机未使用 server_ip（203.0.113.42）: %s", tc.key, u)
		}
		if strings.Contains(u, "@your-domain.com:") {
			t.Errorf("BUG: %s URI 仍使用域名作为连接主机: %s", tc.key, u)
		}
	}
	// SNI 保留域名（anytls / landing）
	for _, key := range []string{"anytls", "landing-1"} {
		if u := uris[key]; !strings.Contains(u, "sni=your-domain.com") {
			t.Errorf("BUG: %s URI 的 SNI 应保留域名 your-domain.com: %s", key, u)
		}
	}
	// naive 保留域名（整体不换主机）
	if u := uris["naive"]; !strings.Contains(u, "@your-domain.com:44333") || strings.Contains(u, "203.0.113.42") {
		t.Errorf("BUG: naive URI 应保留域名主机: %s", u)
	}
}

// TestRepro_BuildURIs_IPv6HostBracketed：server_ip 为 IPv6 时，
// URI authority 必须写作 [IPv6]（端口前必须先闭括号），SNI 保留域名，naive 保留域名。
func TestRepro_BuildURIs_IPv6HostBracketed(t *testing.T) {
	tmp := t.TempDir()
	confPath = filepath.Join(tmp, "panel.json")
	secretsPath = filepath.Join(tmp, "secrets.env")
	writeFullSecrets(t)

	os.WriteFile(confPath, []byte(`{
		"domain": "your-domain.com",
		"server_ip": "2001:db8::10",
		"panel_title": "NodeName",
		"ss_port": 33899, "anytls_port": 21111, "socks_port": 10808, "naive_port": 44333,
		"landings": [{"id":"1","name":"LD1","enabled":true,"port":21112}]
	}`), 0600)

	c, _ := loadConfig()
	if c.ServerIP != "2001:db8::10" {
		t.Fatalf("前置失败: server_ip 未加载: %q", c.ServerIP)
	}
	uris := buildURIs(c, readSecrets())

	for _, key := range []string{"ss", "anytls", "socks", "landing-1"} {
		u, ok := uris[key]
		if !ok || u == "" {
			t.Errorf("%s URI 未生成", key)
			continue
		}
		if !strings.Contains(u, "@[2001:db8::10]:") {
			t.Errorf("BUG: %s URI 的 IPv6 主机未加 [] 括号（客户端无法解析）: %s", key, u)
		}
	}
	for _, key := range []string{"anytls", "landing-1"} {
		if u := uris[key]; !strings.Contains(u, "sni=your-domain.com") {
			t.Errorf("BUG: %s URI 的 SNI 应保留域名: %s", key, u)
		}
	}
	if u := uris["naive"]; !strings.Contains(u, "@your-domain.com:44333") || strings.Contains(u, "2001:db8::10") {
		t.Errorf("BUG: naive URI 应保留域名主机: %s", u)
	}
}

// TestRepro_NodeHandler_HostFieldsAlignWithURI：节点 API 的 host 字段必须与
// 实际写入 URI 的连接主机一致（ss/anytls/socks/landing=IP，naive=域名），
// sni 字段保留域名。旧实现 host 一律返回域名，与 URI/连接地址展示不一致。
func TestRepro_NodeHandler_HostFieldsAlignWithURI(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()
	writeFullSecrets(t)

	// 给测试配置加一个启用的落地服务（setupPanel 默认无 landings）
	c := configGet()
	c.Landings = []LandingService{{ID: "1", Name: "LD1", Enabled: true, Port: 21112}}
	if err := configSet(c); err != nil {
		t.Fatalf("写入落地配置失败: %v", err)
	}

	// 保存 server_ip（走正式 settings API，与真实用户操作一致）
	status, resp := postSettings(t, srv, cookie, map[string]any{
		"server_ip": "203.0.113.42",
	})
	if status != 200 {
		t.Fatalf("保存 server_ip 失败: status=%d resp=%v", status, resp)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/admin/api/node", nil)
	req.AddCookie(&http.Cookie{Name: "bv_sess", Value: cookie})
	hresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET node: %v", err)
	}
	defer hresp.Body.Close()
	out, _ := io.ReadAll(hresp.Body)
	t.Logf("node resp: %.600s", string(out))

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("解析 node 响应失败: %v", err)
	}

	// host 字段对齐：ss/anytls/socks/landings 用连接 IP；naive 用域名
	for _, key := range []string{"ss", "anytls", "socks"} {
		m, _ := got[key].(map[string]any)
		if m == nil {
			t.Errorf("%s 字段缺失", key)
			continue
		}
		if m["host"] != "203.0.113.42" {
			t.Errorf("BUG: %s.host=%v（应与 URI 连接主机一致，为 server_ip 203.0.113.42）", key, m["host"])
		}
	}
	landings, _ := got["landings"].([]any)
	if len(landings) == 0 {
		t.Errorf("landings 列表为空（配置未带落地），请在本测试配置中加入落地服务")
	} else {
		l0, _ := landings[0].(map[string]any)
		if l0 != nil && l0["host"] != "203.0.113.42" {
			t.Errorf("BUG: landings[0].host=%v（应为 203.0.113.42）", l0["host"])
		}
		if l0 != nil && l0["sni"] != "your-domain.com" {
			t.Errorf("BUG: landings[0].sni=%v（应保留域名）", l0["sni"])
		}
	}
	if m, _ := got["naive"].(map[string]any); m != nil {
		if m["host"] != "your-domain.com" {
			t.Errorf("BUG: naive.host=%v（naive 应保留域名）", m["host"])
		}
		if m["sni"] != "your-domain.com" {
			t.Errorf("BUG: naive.sni=%v（应保留域名）", m["sni"])
		}
	}
	// 顶层连接地址仍为解析出的 IP（原行为）
	if got["server_ip"] != "203.0.113.42" {
		t.Errorf("BUG: 顶层 server_ip=%v（应仍为手动 IP）", got["server_ip"])
	}
}
