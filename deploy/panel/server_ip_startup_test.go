package main

// 启动自动探测公网 IPv4 + 共享探测逻辑（fetchPublicIP）的行为测试。
//
// 覆盖：
//   - fetchPublicIP 的 mock HTTP 检测：正常 IPv4 / 多端点兜底 / 拒绝内网 /
//     拒绝超长响应 / 拒绝非 IP / ipv4Only 拒绝 IPv6 / 忽略环境代理 / 全部失败报错
//   - 手动检测 API（api/detect-public-ip）：只返回候选值不落盘；全部不可达时 503
//   - configSetServerIPIfEmpty：空则写入且保留其他字段；已有值不覆盖；写盘失败报错
//   - probeAndPersistServerIP：成功落盘；探测期间用户已保存 → 不覆盖（锁内复查）；
//     失败不阻塞且配置不变；echo 返回 IPv6 视为失败不写入
//   - 启动链路：maybeAutoProbeServerIP 异步落盘 / 已有值跳过（零外发），
//     并用 AST 断言 main() 确实接线调用（防止只测 helper 忘了启动入口）
//   - 旧快照交错：探测落盘落在「设置保存的快照与写盘之间」时不丢用户显式 IP；
//     全量表单回显加载时的空 server_ip 不清掉探测值；显式清空仍可用；
//     探测落盘期间其他配置变更（落地）不丢失
//   - 并发保存：多 goroutine 设置保存 + 自动探测并发，最终配置完整一致

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// withMockEndpoints 用本地 mock HTTP 服务替换公网 echo 端点（测试后恢复）。
func withMockEndpoints(t *testing.T, urls []string) {
	t.Helper()
	old := publicIPEchoEndpoints
	publicIPEchoEndpoints = urls
	t.Cleanup(func() { publicIPEchoEndpoints = old })
}

// mockEchoServer 启动一个按顺序返回响应体的 mock echo 服务。
func mockEchoServer(t *testing.T, bodies ...string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	idx := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		body := bodies[0]
		if len(bodies) > 1 {
			body = bodies[idx%len(bodies)]
			idx++
		}
		mu.Unlock()
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchPublicIP_MockReturnsIPv4(t *testing.T) {
	srv := mockEchoServer(t, "203.0.113.7\n")
	withMockEndpoints(t, []string{srv.URL + "/ip"})
	ip, source, err := fetchPublicIP(context.Background(), false, 3*time.Second)
	if err != nil {
		t.Fatalf("fetchPublicIP: %v", err)
	}
	if ip != "203.0.113.7" {
		t.Errorf("ip=%q want 203.0.113.7", ip)
	}
	if !strings.Contains(source, srv.URL) {
		t.Errorf("source=%q 应为 mock 端点", source)
	}
}

func TestFetchPublicIP_FallsBackToNextEndpoint(t *testing.T) {
	bad := mockEchoServer(t, "not-an-ip")
	good := mockEchoServer(t, "198.51.100.23")
	withMockEndpoints(t, []string{bad.URL + "/ip", good.URL + "/ip"})
	ip, _, err := fetchPublicIP(context.Background(), false, 3*time.Second)
	if err != nil {
		t.Fatalf("应兜底到第二个端点: %v", err)
	}
	if ip != "198.51.100.23" {
		t.Errorf("ip=%q want 198.51.100.23", ip)
	}
}

func TestFetchPublicIP_RejectsPrivateAndInvalid(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		expect string
	}{
		{"private rfc1918", "10.0.0.5", "内网"},
		{"private cgnat", "100.64.1.2", "内网"},
		{"loopback", "127.0.0.1", "内网"},
		{"ipv6 ula", "fd00::1", "内网"},
		{"garbage", "hello", "不是合法 IP"},
		{"empty", "", "不是合法 IP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := mockEchoServer(t, tc.body)
			withMockEndpoints(t, []string{srv.URL + "/ip"})
			_, _, err := fetchPublicIP(context.Background(), false, 3*time.Second)
			if err == nil {
				t.Fatalf("响应 %q 应被拒绝，却成功了", tc.body)
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("错误信息应包含 %q: %v", tc.expect, err)
			}
		})
	}
}

func TestFetchPublicIP_RejectsOversizedBody(t *testing.T) {
	srv := mockEchoServer(t, strings.Repeat("203.0.113.", 30)) // 300 字节
	withMockEndpoints(t, []string{srv.URL + "/ip"})
	_, _, err := fetchPublicIP(context.Background(), false, 3*time.Second)
	if err == nil {
		t.Fatalf("超长响应应被拒绝")
	}
	if !strings.Contains(err.Error(), "超长") {
		t.Errorf("错误信息应说明超长: %v", err)
	}
}

func TestFetchPublicIP_IPv4OnlyRejectsIPv6(t *testing.T) {
	srv := mockEchoServer(t, "2001:db8::1")
	withMockEndpoints(t, []string{srv.URL + "/ip"})
	// ipv4Only=true（启动自动探测）：拒绝
	if _, _, err := fetchPublicIP(context.Background(), true, 3*time.Second); err == nil {
		t.Fatalf("ipv4Only=true 时 IPv6 响应应被拒绝")
	}
	// ipv4Only=false（手动检测候选）：接受
	ip, _, err := fetchPublicIP(context.Background(), false, 3*time.Second)
	if err != nil {
		t.Fatalf("ipv4Only=false 应接受 IPv6 候选: %v", err)
	}
	if ip != "2001:db8::1" {
		t.Errorf("ip=%q want 2001:db8::1", ip)
	}
}

// TestFetchPublicIP_IgnoresEnvProxy：环境变量指向不可达代理时探测仍必须成功
// （Transport.Proxy=nil 显式禁用 HTTP_PROXY/HTTPS_PROXY）。
func TestFetchPublicIP_IgnoresEnvProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	t.Setenv("https_proxy", "http://127.0.0.1:1")
	srv := mockEchoServer(t, "203.0.113.9")
	withMockEndpoints(t, []string{srv.URL + "/ip"})
	ip, _, err := fetchPublicIP(context.Background(), true, 3*time.Second)
	if err != nil {
		t.Fatalf("探测不应受环境代理影响: %v", err)
	}
	if ip != "203.0.113.9" {
		t.Errorf("ip=%q want 203.0.113.9", ip)
	}
}

func TestFetchPublicIP_AllEndpointsDead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // 立即关闭 → 连接拒绝
	withMockEndpoints(t, []string{srv.URL + "/ip", srv.URL + "/backup"})
	_, _, err := fetchPublicIP(context.Background(), false, 2*time.Second)
	if err == nil {
		t.Fatalf("全部端点不可达应报错")
	}
	if !strings.Contains(err.Error(), "2 个") {
		t.Errorf("错误应聚合端点数: %v", err)
	}
}

func TestFetchPublicIP_RespectsTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	withMockEndpoints(t, []string{srv.URL + "/ip"})
	start := time.Now()
	_, _, err := fetchPublicIP(context.Background(), false, 300*time.Millisecond)
	if err == nil {
		t.Fatalf("挂死端点应超时报错")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("超时未生效，耗时 %v", elapsed)
	}
}

// ---- 手动检测 API：只返回候选值，不落盘 ----

func TestDetectPublicIPHandler_ReturnsCandidateOnly(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	mock := mockEchoServer(t, "203.0.113.30")
	withMockEndpoints(t, []string{mock.URL + "/ip"})

	req, _ := http.NewRequest("GET", srv.URL+"/admin/api/detect-public-ip", nil)
	req.AddCookie(&http.Cookie{Name: "bv_sess", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("detect-public-ip: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	var got map[string]any
	json.Unmarshal(out, &got)
	if resp.StatusCode != 200 || got["ip"] != "203.0.113.30" {
		t.Fatalf("期望 200 + 候选 IP，got status=%d body=%s", resp.StatusCode, string(out))
	}
	// 关键：不写配置（手动按钮只返回候选，由用户确认后保存）
	c, _ := loadConfig()
	if c.ServerIP != "" {
		t.Errorf("手动检测不应落盘，但 server_ip=%q", c.ServerIP)
	}
}

func TestDetectPublicIPHandler_AllDeadReturns503(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()
	withMockEndpoints(t, []string{dead.URL + "/ip"})

	req, _ := http.NewRequest("GET", srv.URL+"/admin/api/detect-public-ip", nil)
	req.AddCookie(&http.Cookie{Name: "bv_sess", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("detect-public-ip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("全部不可达应 503，got %d", resp.StatusCode)
	}
}

// ---- configSetServerIPIfEmpty ----

func TestConfigSetServerIPIfEmpty_WritesAndPreservesOtherFields(t *testing.T) {
	tmp := t.TempDir()
	confPath = filepath.Join(tmp, "panel.json")
	os.WriteFile(confPath, []byte(`{
		"domain": "your-domain.com",
		"panel_title": "NodeName",
		"panel_port": 15608,
		"landings": [{"id":"1","name":"LD","enabled":true,"port":21112}],
		"cert_mode": "manual", "cert_fullchain": "/a/b.pem", "cert_privkey": "/a/c.pem"
	}`), 0600)
	c, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	cfg = c

	set, err := configSetServerIPIfEmpty("203.0.113.42")
	if err != nil || !set {
		t.Fatalf("期望写入成功: set=%v err=%v", set, err)
	}
	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "203.0.113.42" {
		t.Errorf("server_ip 未持久化: %q", reloaded.ServerIP)
	}
	// 其他字段必须原样保留
	if reloaded.PanelTitle != "NodeName" || reloaded.Domain != "your-domain.com" || reloaded.PanelPort != 15608 {
		t.Errorf("其他设置丢失: %+v", reloaded)
	}
	if reloaded.CertMode != "manual" || reloaded.CertFullchain != "/a/b.pem" || reloaded.CertPrivkey != "/a/c.pem" {
		t.Errorf("证书设置丢失: %+v", reloaded)
	}
	if len(reloaded.Landings) != 1 || reloaded.Landings[0].ID != "1" || reloaded.Landings[0].Port != 21112 {
		t.Errorf("落地配置丢失: %+v", reloaded.Landings)
	}
}

func TestConfigSetServerIPIfEmpty_DoesNotOverwriteExisting(t *testing.T) {
	tmp := t.TempDir()
	confPath = filepath.Join(tmp, "panel.json")
	os.WriteFile(confPath, []byte(`{"domain":"d.example","server_ip":"198.51.100.9","panel_title":"Keep"}`), 0600)
	c, _ := loadConfig()
	cfg = c

	set, err := configSetServerIPIfEmpty("203.0.113.42")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if set {
		t.Errorf("已有 server_ip 时不应写入")
	}
	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "198.51.100.9" {
		t.Errorf("已有值被覆盖: %q", reloaded.ServerIP)
	}
}

func TestConfigSetServerIPIfEmpty_SaveErrorPropagates(t *testing.T) {
	tmp := t.TempDir()
	// confPath 指向不存在目录下的文件 → saveConfig 写 tmp 文件失败
	confPath = filepath.Join(tmp, "no-such-dir", "panel.json")
	os.WriteFile(filepath.Join(tmp, "keep.json"), []byte(`{}`), 0600)
	cfg = Config{Domain: "d.example", PanelTitle: "T"}

	set, err := configSetServerIPIfEmpty("203.0.113.42")
	if err == nil {
		t.Fatalf("写盘失败应返回错误")
	}
	if set {
		t.Errorf("失败时不应报告已写入")
	}
	if cfg.ServerIP != "" {
		t.Errorf("失败时内存配置不应被修改: %q", cfg.ServerIP)
	}
}

// ---- probeAndPersistServerIP（启动自动探测行为）----

// setupProbeState 在临时目录准备带完整字段的配置。
func setupProbeState(t *testing.T) Config {
	t.Helper()
	tmp := t.TempDir()
	confPath = filepath.Join(tmp, "panel.json")
	secretsPath = filepath.Join(tmp, "secrets.env")
	os.WriteFile(confPath, []byte(`{
		"domain": "your-domain.com",
		"panel_title": "NodeName",
		"panel_port": 15608,
		"url_path": "/admin/",
		"landings": [{"id":"1","name":"LD","enabled":true,"port":21112}]
	}`), 0600)
	c, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	cfg = c
	return c
}

func TestProbeAndPersistServerIP_Success(t *testing.T) {
	setupProbeState(t)
	mock := mockEchoServer(t, "203.0.113.55")
	withMockEndpoints(t, []string{mock.URL + "/ip"})

	if err := probeAndPersistServerIP(context.Background()); err != nil {
		t.Fatalf("probeAndPersistServerIP: %v", err)
	}
	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "203.0.113.55" {
		t.Errorf("server_ip 未自动持久化: %q", reloaded.ServerIP)
	}
	if reloaded.PanelTitle != "NodeName" || reloaded.Domain != "your-domain.com" {
		t.Errorf("自动保存丢失其他设置: %+v", reloaded)
	}
	if len(reloaded.Landings) != 1 {
		t.Errorf("落地配置丢失: %+v", reloaded.Landings)
	}
}

func TestProbeAndPersistServerIP_FailureDoesNotTouchConfig(t *testing.T) {
	setupProbeState(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()
	withMockEndpoints(t, []string{dead.URL + "/ip"})

	err := probeAndPersistServerIP(context.Background())
	if err == nil {
		t.Fatalf("探测失败应返回错误（供上层记日志）")
	}
	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "" {
		t.Errorf("失败时不应写入 server_ip: %q", reloaded.ServerIP)
	}
	if reloaded.PanelTitle != "NodeName" {
		t.Errorf("失败时配置不应被改动: %+v", reloaded)
	}
}

func TestProbeAndPersistServerIP_IPv6EchoRejected(t *testing.T) {
	setupProbeState(t)
	mock := mockEchoServer(t, "2001:db8::1")
	withMockEndpoints(t, []string{mock.URL + "/ip"})

	if err := probeAndPersistServerIP(context.Background()); err == nil {
		t.Fatalf("echo 返回 IPv6 时自动探测（要求 IPv4）应视为失败")
	}
	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "" {
		t.Errorf("IPv6 不应写入 server_ip: %q", reloaded.ServerIP)
	}
}

// TestProbeAndPersistServerIP_DoesNotOverwriteUserSaveDuringProbe：
// 探测 HTTP 挂起期间用户经 settings API 保存了自己的 IP → 探测完成后
// 必须在配置锁内复查发现已有值，放弃写入（不覆盖用户修改）。
func TestProbeAndPersistServerIP_DoesNotOverwriteUserSaveDuringProbe(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	release := make(chan struct{})
	var releaseOnce sync.Once
	doRelease := func() { releaseOnce.Do(func() { close(release) }) }
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release                           // 模拟公网 echo 慢响应
		io.WriteString(w, "203.0.113.77\n") // 放行后返回有效 IPv4
	}))
	t.Cleanup(func() { doRelease(); mock.Close() })
	withMockEndpoints(t, []string{mock.URL + "/ip"})

	done := make(chan error, 1)
	go func() {
		done <- probeAndPersistServerIP(context.Background())
	}()

	// 探测挂起期间，用户在面板保存了自己的公网 IP（走正式 settings API）
	time.Sleep(50 * time.Millisecond) // 确保探测已发出请求并在挂起
	status, resp := postSettings(t, srv, cookie, map[string]any{
		"server_ip":   "198.51.100.9",
		"panel_title": "UserTitle",
	})
	if status != 200 {
		t.Fatalf("用户保存失败: status=%d resp=%v", status, resp)
	}

	doRelease() // 放行 mock：探测会拿到有效 IPv4，但配置已有用户值
	if err := <-done; err != nil {
		t.Errorf("锁内复查发现已有值时应静默放弃（返回 nil），got: %v", err)
	}

	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "198.51.100.9" {
		t.Errorf("用户保存的 server_ip 被自动探测覆盖: %q", reloaded.ServerIP)
	}
	if reloaded.PanelTitle != "UserTitle" {
		t.Errorf("用户其他设置被改动: %q", reloaded.PanelTitle)
	}
}

// TestProbeAndPersistServerIP_UserSavedIP_WinsOverValidProbe：
// mock 返回有效 IPv4，但用户已先保存 → 锁内复查放弃写入并返回 nil。
func TestProbeAndPersistServerIP_UserSavedIP_WinsOverValidProbe(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	mock := mockEchoServer(t, "203.0.113.77")
	withMockEndpoints(t, []string{mock.URL + "/ip"})

	// 用户先保存
	status, resp := postSettings(t, srv, cookie, map[string]any{"server_ip": "198.51.100.9"})
	if status != 200 {
		t.Fatalf("用户保存失败: %d %v", status, resp)
	}
	// 探测拿到有效结果但配置已有值
	if err := probeAndPersistServerIP(context.Background()); err != nil {
		t.Fatalf("已有值时探测应静默放弃而非报错: %v", err)
	}
	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "198.51.100.9" {
		t.Errorf("已有值被覆盖: %q", reloaded.ServerIP)
	}
}

// ---- 并发保存 ----

// TestConcurrent_ConfigSetServerIPIfEmpty：并发多次「只在空时写入」，
// 恰好一次生效，落盘 JSON 始终完整可解析。
func TestConcurrent_ConfigSetServerIPIfEmpty(t *testing.T) {
	setupProbeState(t)
	const n = 20
	ips := make([]string, n)
	for i := range ips {
		ips[i] = fmt.Sprintf("203.0.113.%d", i+1)
	}
	var wg sync.WaitGroup
	written := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			set, err := configSetServerIPIfEmpty(ips[i])
			if err != nil {
				t.Errorf("并发写入 %s 报错: %v", ips[i], err)
			}
			written[i] = set
		}(i)
	}
	wg.Wait()
	count := 0
	for _, w := range written {
		if w {
			count++
		}
	}
	if count != 1 {
		t.Errorf("并发空值写入应恰好一次生效，实际 %d 次", count)
	}
	reloaded, err := loadConfig()
	if err != nil {
		t.Fatalf("并发后配置不可解析: %v", err)
	}
	found := false
	for _, ip := range ips {
		if reloaded.ServerIP == ip {
			found = true
		}
	}
	if !found {
		t.Errorf("server_ip=%q 不在候选集中", reloaded.ServerIP)
	}
	if reloaded.PanelTitle != "NodeName" || len(reloaded.Landings) != 1 {
		t.Errorf("并发写丢失其他设置: %+v", reloaded)
	}
}

// TestConcurrent_SettingsSavesWithAutoProbe：多个设置保存与自动探测并发，
// 最终配置必须完整（用户值优先，不出现半写/损坏）。
func TestConcurrent_SettingsSavesWithAutoProbe(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	release := make(chan struct{})
	var releaseOnce sync.Once
	doRelease := func() { releaseOnce.Do(func() { close(release) }) }
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, "203.0.113.88")
	}))
	t.Cleanup(func() { doRelease(); mock.Close() })
	withMockEndpoints(t, []string{mock.URL + "/ip"})

	probeDone := make(chan error, 1)
	go func() {
		probeDone <- probeAndPersistServerIP(context.Background())
	}()

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]any{
				"panel_title": fmt.Sprintf("T%d", i),
				"server_ip":   "198.51.100.9",
			})
			req, _ := http.NewRequest("POST", srv.URL+"/admin/api/settings", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(&http.Cookie{Name: "bv_sess", Value: cookie})
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("并发保存 %d: %v", i, err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Errorf("并发保存 %d 状态异常: %d", i, resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()
	doRelease()
	if err := <-probeDone; err != nil {
		t.Errorf("已有用户值时探测应静默放弃（nil），got: %v", err)
	}

	reloaded, err := loadConfig()
	if err != nil {
		t.Fatalf("并发后配置不可解析: %v", err)
	}
	if reloaded.ServerIP != "198.51.100.9" {
		t.Errorf("用户并发保存的 server_ip 被探测覆盖: %q", reloaded.ServerIP)
	}
	if !strings.HasPrefix(reloaded.PanelTitle, "T") || len(reloaded.PanelTitle) != 2 {
		t.Errorf("panel_title 异常: %q", reloaded.PanelTitle)
	}
}

// ---- 启动链路（main → maybeAutoProbeServerIP）----

// TestStartupWiring_MainInvokesMaybeAutoProbe 用 AST 断言 main() 确实调用了
// maybeAutoProbeServerIP：只测 helper 而启动入口没接线等于功能不存在，
// 未来重构 main 时本测试防止接线被无声删除。
func TestStartupWiring_MainInvokesMaybeAutoProbe(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 main.go 失败: %v", err)
	}
	found := false
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "main" || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if ce, ok := n.(*ast.CallExpr); ok {
				if id, ok := ce.Fun.(*ast.Ident); ok && id.Name == "maybeAutoProbeServerIP" {
					found = true
				}
			}
			return true
		})
	}
	if !found {
		t.Fatalf("main() 未调用 maybeAutoProbeServerIP：启动自动探测接线缺失")
	}
}

// TestMaybeAutoProbeServerIP_AsyncPersistsOnEmpty：与 main 同款调用方式
// （空 ServerIP → 异步探测），轮询验证后台 goroutine 真正完成探测并原子落盘，
// 且其他字段不被破坏。
func TestMaybeAutoProbeServerIP_AsyncPersistsOnEmpty(t *testing.T) {
	setupProbeState(t) // cfg 已就绪（server_ip 为空、带标题与落地）
	mock := mockEchoServer(t, "203.0.113.66")
	withMockEndpoints(t, []string{mock.URL + "/ip"})

	maybeAutoProbeServerIP(Config{Domain: "your-domain.com"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := loadConfig()
		if err == nil && c.ServerIP == "203.0.113.66" {
			if c.PanelTitle != "NodeName" || len(c.Landings) != 1 {
				t.Errorf("启动自动保存破坏其他字段: %+v", c)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("启动链路自动探测未在 3s 内持久化 server_ip")
}

// TestMaybeAutoProbeServerIP_SkipsWhenSet：已有 server_ip 时启动编排必须
// 完全跳过（零外发请求、配置不变）。
func TestMaybeAutoProbeServerIP_SkipsWhenSet(t *testing.T) {
	setupProbeState(t)
	cfg.ServerIP = "198.51.100.9"
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, "203.0.113.66")
	}))
	t.Cleanup(srv.Close)
	withMockEndpoints(t, []string{srv.URL + "/ip"})

	maybeAutoProbeServerIP(configGet())
	time.Sleep(200 * time.Millisecond) // 给潜在的错误探测留出可观察窗口

	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("已有 server_ip 时不应发起任何探测请求，实际 %d 次", n)
	}
	c, _ := loadConfig()
	if c.ServerIP != "198.51.100.9" {
		t.Errorf("已有值被改动: %q", c.ServerIP)
	}
}

// ---- 旧快照交错（自动保存 × 普通 configSet）----

// postSettingsAsync 异步提交设置保存（返回完成 channel），配合 bcrypt 等耗时
// 字段制造出「请求已开始、尚未写盘」的确定性交错窗口。
func postSettingsAsync(t *testing.T, srv *httptest.Server, cookie string, body map[string]any) <-chan int {
	t.Helper()
	done := make(chan int, 1)
	go func() {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", srv.URL+"/admin/api/settings", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "bv_sess", Value: cookie})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	return done
}

// TestSettingsSave_InflightProbeAndLandingChangeSurvive：API 级 in-flight 交错——
// 设置保存请求进行中（bcrypt 哈希拖出窗口），启动自动探测落盘 server_ip、
// 落地端口被另一写入者修改。要求：POST 未携带 server_ip 字段（前端未编辑不提交）
// 时不得触碰 ServerIP（锁内重放跳过未提交字段），落地变更不被旧快照抹掉。
// 注：本测试只覆盖「请求处理窗口内」的交错，不声称覆盖页面加载早于探测的时序。
func TestSettingsSave_InflightProbeAndLandingChangeSurvive(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	// 带一个启用的落地
	c := configGet()
	c.Landings = []LandingService{{ID: "1", Name: "LD1", Enabled: true, Port: 21112}}
	if err := configSet(c); err != nil {
		t.Fatalf("写入落地配置: %v", err)
	}

	mock := mockEchoServer(t, "203.0.113.55")
	withMockEndpoints(t, []string{mock.URL + "/ip"})

	// 请求开始（此时 server_ip 为空）→ admin_pass 触发 bcrypt（数十毫秒窗口）
	done := postSettingsAsync(t, srv, cookie, map[string]any{
		"panel_title": "InflightTitle",
		"admin_pass":  "NewPass2026word",
		// 故意不携带 server_ip 字段（前端未编辑不提交）
	})

	// 在保存请求的窗口内：自动探测落盘 + 落地端口被修改
	time.Sleep(20 * time.Millisecond)
	if err := probeAndPersistServerIP(context.Background()); err != nil {
		t.Fatalf("窗口内自动探测: %v", err)
	}
	lc := configGet()
	lc.Landings[0].Port = 30099
	if err := configSet(lc); err != nil {
		t.Fatalf("窗口内落地修改: %v", err)
	}

	if status := <-done; status != 200 {
		t.Fatalf("设置保存状态异常: %d", status)
	}

	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "203.0.113.55" {
		t.Errorf("窗口内探测落盘的 server_ip 被旧快照清掉: %q", reloaded.ServerIP)
	}
	if reloaded.PanelTitle != "InflightTitle" {
		t.Errorf("用户标题未保存: %q", reloaded.PanelTitle)
	}
	if len(reloaded.Landings) != 1 || reloaded.Landings[0].Port != 30099 {
		t.Errorf("窗口内落地修改被旧快照抹掉: %+v", reloaded.Landings)
	}
	// bcrypt 密码确实生效（锁内重放保留了 AdminPassHash）
	if reloaded.AdminPassHash == "" || !bcryptOK(reloaded.AdminPassHash, "NewPass2026word") {
		t.Errorf("admin_pass 未生效或校验失败")
	}
}

// TestSettingsRebase_PreservesConcurrentWriters：函数级确定性交错——
// 模拟 settingsHandler「快照 → 窗口内其他写入者落盘 → 写回」的完整顺序，
// 验证锁内重放（configSetApplySettings）只覆盖设置页字段。
func TestSettingsRebase_PreservesConcurrentWriters(t *testing.T) {
	setupProbeState(t) // ServerIP 为空、带标题与落地

	snap := configGet() // handler 顶部的快照
	// 窗口内：探测落盘 + 落地端口修改 + 证书模式切换（均为其他写入者）
	if set, err := configSetServerIPIfEmpty("203.0.113.55"); err != nil || !set {
		t.Fatalf("探测落盘失败: set=%v err=%v", set, err)
	}
	lc := configGet()
	lc.Landings[0].Port = 30098
	lc.CertMode = "manual"
	lc.CertFullchain = "/x/full.pem"
	if err := configSet(lc); err != nil {
		t.Fatalf("窗口内其他写入者落盘: %v", err)
	}

	// handler 写回（用户提交显式 IP + 标题；显式提交 → serverIPSubmitted=true）
	snap.PanelTitle = "NewTitle"
	snap.ServerIP = "198.51.100.9"
	if err := configSetApplySettings(snap, true); err != nil {
		t.Fatalf("写回失败: %v", err)
	}

	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "198.51.100.9" {
		t.Errorf("用户显式 IP 应胜出: %q", reloaded.ServerIP)
	}
	if reloaded.PanelTitle != "NewTitle" {
		t.Errorf("用户标题丢失: %q", reloaded.PanelTitle)
	}
	if len(reloaded.Landings) != 1 || reloaded.Landings[0].Port != 30098 {
		t.Errorf("窗口内落地修改被抹掉: %+v", reloaded.Landings)
	}
	if reloaded.CertMode != "manual" || reloaded.CertFullchain != "/x/full.pem" {
		t.Errorf("窗口内证书变更被抹掉: %s %s", reloaded.CertMode, reloaded.CertFullchain)
	}
}

// TestSettingsSave_ExplicitClearStillWorks：加载时已有 IP、用户提交空值 =
// 显式清空语义，必须仍然生效（不被上述保护误伤）。
func TestSettingsSave_ExplicitClearStillWorks(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	if status, resp := postSettings(t, srv, cookie, map[string]any{"server_ip": "198.51.100.9"}); status != 200 {
		t.Fatalf("保存 IP 失败: %d %v", status, resp)
	}
	if status, resp := postSettings(t, srv, cookie, map[string]any{"server_ip": ""}); status != 200 {
		t.Fatalf("清空失败: %d %v", status, resp)
	}
	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "" {
		t.Errorf("显式清空未生效: %q", reloaded.ServerIP)
	}
}

// TestSettingsSave_UserExplicitIPWinsOverProbeInterleave：确定性交错模拟
// settingsHandler 的「快照 → 探测落盘 → 写回」顺序：探测结果先落盘、用户的
// 显式 IP 随后写回 → 最终必须是用户的显式 IP，且用户的其他改动（标题）保留。
func TestSettingsSave_UserExplicitIPWinsOverProbeInterleave(t *testing.T) {
	setupProbeState(t) // ServerIP 为空

	snap := configGet()                                  // handler 顶部的旧快照
	set, err := configSetServerIPIfEmpty("203.0.113.55") // 探测在此期间落盘
	if err != nil || !set {
		t.Fatalf("前置探测落盘失败: set=%v err=%v", set, err)
	}
	snap.PanelTitle = "NewTitle"
	snap.ServerIP = "198.51.100.9" // 用户显式提交的 IP
	if err := configSetApplySettings(snap, true); err != nil {
		t.Fatalf("用户写回失败: %v", err)
	}

	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "198.51.100.9" {
		t.Errorf("用户显式 IP 应胜出，got %q", reloaded.ServerIP)
	}
	if reloaded.PanelTitle != "NewTitle" {
		t.Errorf("用户标题丢失: %q", reloaded.PanelTitle)
	}
	if len(reloaded.Landings) != 1 {
		t.Errorf("落地配置丢失: %+v", reloaded.Landings)
	}
}

// TestProbePersist_PreservesConcurrentLandingChange：探测 HTTP 挂起期间
// 另一配置变更（落地端口修改）落盘 → 探测完成后两者都必须保留
// （锁内 clone 整对象，只补 ServerIP）。
func TestProbePersist_PreservesConcurrentLandingChange(t *testing.T) {
	setupProbeState(t)

	release := make(chan struct{})
	var releaseOnce sync.Once
	doRelease := func() { releaseOnce.Do(func() { close(release) }) }
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		io.WriteString(w, "203.0.113.99")
	}))
	t.Cleanup(func() { doRelease(); mock.Close() })
	withMockEndpoints(t, []string{mock.URL + "/ip"})

	probeDone := make(chan error, 1)
	go func() { probeDone <- probeAndPersistServerIP(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	// 探测挂起期间修改落地端口（另一 handler 的普通 configSet）
	c := configGet()
	c.Landings[0].Port = 30099
	if err := configSet(c); err != nil {
		t.Fatalf("落地修改失败: %v", err)
	}

	doRelease()
	if err := <-probeDone; err != nil {
		t.Fatalf("探测: %v", err)
	}
	reloaded, _ := loadConfig()
	if len(reloaded.Landings) != 1 || reloaded.Landings[0].Port != 30099 {
		t.Errorf("探测落盘丢失并发落地修改: %+v", reloaded.Landings)
	}
	if reloaded.ServerIP != "203.0.113.99" {
		t.Errorf("探测结果未保留: %q", reloaded.ServerIP)
	}
}

// TestSettingsSave_AbsentServerIPFieldKeepsLatest：probe 已落盘后，
// POST 只改标题、完全不携带 server_ip 字段 → 最新值必须原样保留。
func TestSettingsSave_AbsentServerIPFieldKeepsLatest(t *testing.T) {
	srv, cookie := setupPanel(t)
	defer srv.Close()
	defer db.Close()

	mock := mockEchoServer(t, "203.0.113.31")
	withMockEndpoints(t, []string{mock.URL + "/ip"})
	if err := probeAndPersistServerIP(context.Background()); err != nil {
		t.Fatalf("自动探测: %v", err)
	}

	status, resp := postSettings(t, srv, cookie, map[string]any{"panel_title": "OnlyTitle"})
	if status != 200 {
		t.Fatalf("保存失败: %d %v", status, resp)
	}
	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "203.0.113.31" {
		t.Errorf("未提交 server_ip 却丢失最新值: %q", reloaded.ServerIP)
	}
	if reloaded.PanelTitle != "OnlyTitle" {
		t.Errorf("标题未保存: %q", reloaded.PanelTitle)
	}
}

// ---- P2：fetch 校验硬化（红测）----

// TestFetchPublicIP_RejectsInvalidAddresses：echo 返回未指定/组播/广播/
// 保留段地址时必须拒绝，不得当作可用公网 IP。
func TestFetchPublicIP_RejectsInvalidAddresses(t *testing.T) {
	cases := []string{
		"0.0.0.0",         // 未指定
		"224.0.1.1",       // 组播
		"255.255.255.255", // 广播
		"240.1.2.3",       // 保留段 240.0.0.0/4
		"::",              // IPv6 未指定
	}
	for _, cand := range cases {
		t.Run(cand, func(t *testing.T) {
			srv := mockEchoServer(t, cand)
			withMockEndpoints(t, []string{srv.URL + "/ip"})
			if _, _, err := fetchPublicIP(context.Background(), false, 2*time.Second); err == nil {
				t.Errorf("echo 返回 %q 必须被拒绝，却被当作可用 IP", cand)
			}
		})
	}
}

// TestFetchPublicIP_RejectsRedirects：echo 端点返回 302 重定向时必须拒绝
// （不跟随，防被劫持端点把探测导向任意目标）。
func TestFetchPublicIP_RejectsRedirects(t *testing.T) {
	target := mockEchoServer(t, "203.0.113.5")
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/real", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	withMockEndpoints(t, []string{redirect.URL + "/ip"})
	if _, _, err := fetchPublicIP(context.Background(), false, 2*time.Second); err == nil {
		t.Fatalf("重定向响应必须被拒绝（不得跟随）")
	}
}

// TestFetchPublicIP_IPv4OnlyDialsTCP4：ipv4Only=true 必须强制 tcp4 拨号——
// 仅监听 IPv6 回环（[::1]）的 echo 即使会返回合法 IPv4 文本也不得连上；
// ipv4Only=false（手动检测候选）仍可正常经 IPv6 访问。
func TestFetchPublicIP_IPv4OnlyDialsTCP4(t *testing.T) {
	v6srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "203.0.113.5")
	}))
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("本环境无 IPv6 回环，跳过: %v", err)
	}
	v6srv.Listener = l
	v6srv.Start()
	t.Cleanup(v6srv.Close)
	url := "http://[::1]" + portOfListener(l) + "/ip"
	withMockEndpoints(t, []string{url})

	if _, _, err := fetchPublicIP(context.Background(), true, 2*time.Second); err == nil {
		t.Fatalf("ipv4Only=true 必须强制 tcp4 拨号，不得经 IPv6 端点成功")
	}
	ip, _, err := fetchPublicIP(context.Background(), false, 2*time.Second)
	if err != nil {
		t.Fatalf("ipv4Only=false 应可经 IPv6 访问: %v", err)
	}
	if ip != "203.0.113.5" {
		t.Errorf("ip=%q want 203.0.113.5", ip)
	}
}

// portOfListener 从 listener 地址提取 ":port"（拼 URL 用）。
func portOfListener(l net.Listener) string {
	addr := l.Addr().String()
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ""
}

// TestProbeAndPersistServerIP_TotalTimeout：无 deadline 的 caller ctx
// 必须被套上总超时（3 端点 × 6s = 18s），防止多个挂死端点把探测拖成 18s+。
func TestProbeAndPersistServerIP_TotalTimeout(t *testing.T) {
	setupProbeState(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	withMockEndpoints(t, []string{srv.URL + "/a", srv.URL + "/b", srv.URL + "/c"})

	old := publicIPProbeTotalTimeout
	publicIPProbeTotalTimeout = 250 * time.Millisecond
	t.Cleanup(func() { publicIPProbeTotalTimeout = old })

	start := time.Now()
	err := probeAndPersistServerIP(context.Background()) // 无 deadline ctx
	if err == nil {
		t.Fatalf("挂死端点 + 总超时应报错")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("总超时未生效，耗时 %v", elapsed)
	}
	reloaded, _ := loadConfig()
	if reloaded.ServerIP != "" {
		t.Errorf("超时失败不应写入: %q", reloaded.ServerIP)
	}
}
