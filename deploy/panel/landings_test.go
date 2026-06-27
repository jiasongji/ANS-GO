package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLandingsConfig_LoadsArray 验证含 landings 数组的 panel.json 能被正确加载到 Config.Landings。
// v1.5.26 多落地服务的配置加载回归测试。
func TestLandingsConfig_LoadsArray(t *testing.T) {
	tmp := t.TempDir()
	confPath = filepath.Join(tmp, "panel.json")
	secretsPath = filepath.Join(tmp, "secrets.env")
	panelJSON := `{
  "domain": "your-domain.com",
  "panel_port": 15608,
  "url_path": "/admin/",
  "svc_ss_enabled": "false",
  "landings": [
    {"id":"1","name":"示例SS落地","enabled":true,"port":30001,
     "remote_enabled":true,"remote_type":"ss","remote_host":"192.0.2.1","remote_port":8388,
     "remote_method":"2022-blake3-aes-128-gcm","remote_password":"AAAAAAAAAAAAAAAAAAAAAA==","remote_user":""},
    {"id":"2","name":"示例SOCKS落地","enabled":true,"port":30002,
     "remote_enabled":true,"remote_type":"socks","remote_host":"198.51.100.1","remote_port":1080,
     "remote_method":"","remote_password":"sockspass","remote_user":"socksuser"},
    {"id":"3","name":"直连落地","enabled":false,"port":30003,
     "remote_enabled":false,"remote_type":"ss","remote_host":"","remote_port":0,
     "remote_method":"","remote_password":"","remote_user":""}
  ]
}`
	os.WriteFile(confPath, []byte(panelJSON), 0600)
	os.WriteFile(secretsPath, []byte("SS_KEY=dGVzdA==\n"), 0600)

	c, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(c.Landings) != 3 {
		t.Fatalf("Landings len = %d, want 3", len(c.Landings))
	}
	// 第1个：SS 远端
	l1 := c.Landings[0]
	if l1.ID != "1" || l1.Name != "示例SS落地" || !l1.Enabled || l1.Port != 30001 {
		t.Errorf("landing[0] = %+v, want id=1 name=示例SS落地 enabled=true port=30001", l1)
	}
	if l1.RemoteType != "ss" || l1.RemoteHost != "192.0.2.1" || l1.RemoteMethod != "2022-blake3-aes-128-gcm" {
		t.Errorf("landing[0] remote mismatch: %+v", l1)
	}
	// 第2个：SOCKS5 远端
	l2 := c.Landings[1]
	if l2.RemoteType != "socks" || l2.RemoteUser != "socksuser" || l2.RemotePassword != "sockspass" {
		t.Errorf("landing[1] socks remote mismatch: %+v", l2)
	}
	// 第3个：未启用
	l3 := c.Landings[2]
	if l3.Enabled {
		t.Errorf("landing[2] should be disabled")
	}
}

// TestLandingsConfig_EmptyArray 验证无 landings 字段（新部署 install.sh 写 "landings":[]）时
// Config.Landings 为 nil/空，不 panic。
func TestLandingsConfig_EmptyArray(t *testing.T) {
	tmp := t.TempDir()
	confPath = filepath.Join(tmp, "panel.json")
	secretsPath = filepath.Join(tmp, "secrets.env")
	panelJSON := `{
  "domain": "your-domain.com",
  "panel_port": 15608,
  "url_path": "/admin/",
  "svc_ss_enabled": "false",
  "landings": []
}`
	os.WriteFile(confPath, []byte(panelJSON), 0600)
	os.WriteFile(secretsPath, []byte("SS_KEY=dGVzdA==\n"), 0600)

	c, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(c.Landings) != 0 {
		t.Errorf("Landings len = %d, want 0", len(c.Landings))
	}
	// enabledLandings 不应 panic
	if e := enabledLandings(c); len(e) != 0 {
		t.Errorf("enabledLandings len = %d, want 0", len(e))
	}
}

// TestLandingsPortConflict_Detected 验证两个启用的落地服务用相同端口时，portConflicts 报告冲突。
// v1.5.26 端口冲突检测（sing-box 同进程无法 bind 相同端口）。
func TestLandingsPortConflict_Detected(t *testing.T) {
	c := Config{
		Domain: "your-domain.com",
		Landings: []LandingService{
			{ID: "1", Name: "落地A", Enabled: true, Port: 30001},
			{ID: "2", Name: "落地B", Enabled: true, Port: 30001}, // 与 A 同端口
		},
	}
	msg := portConflicts(c)
	if msg == "" {
		t.Fatalf("portConflicts 应报告端口冲突（两个启用落地用 30001），但返回空串")
	}
	t.Logf("检测到端口冲突（符合预期）: %s", msg)
}

// TestLandingsPortConflict_NaivePortConflict 验证 Naive(caddy) 与落地 AnyTLS(sing-box)
// 不能使用同一端口。v1.5.29 回归：Docker/host 网络下两进程处于同一网络命名空间，
// sing-box 已监听落地端口时 caddy 监听同端口会 bind: address already in use 并卡 activating。
func TestLandingsPortConflict_NaivePortConflict(t *testing.T) {
	c := Config{
		Domain:          "your-domain.com",
		SvcNaiveEnabled: "true",
		NaivePort:       30001,
		Landings: []LandingService{
			{ID: "1", Name: "落地A", Enabled: true, Port: 30001},
		},
	}
	msg := portConflicts(c)
	if msg == "" {
		t.Fatalf("Naive 与落地 AnyTLS 使用同端口应被拒绝")
	}
	t.Logf("检测到跨进程端口冲突（符合预期）: %s", msg)
}

func TestLandingsPortConflict_PanelPortConflict(t *testing.T) {
	c := Config{
		Domain:    "your-domain.com",
		PanelPort: 30001,
		Landings: []LandingService{
			{ID: "1", Name: "落地A", Enabled: true, Port: 30001},
		},
	}
	msg := portConflicts(c)
	if msg == "" {
		t.Fatalf("Panel 与落地 AnyTLS 使用同端口应被拒绝")
	}
}

// TestLandingsPortConflict_NoConflict 验证不同端口 + 未启用落地的端口不参与检测。
func TestLandingsPortConflict_NoConflict(t *testing.T) {
	c := Config{
		Domain:           "your-domain.com",
		SvcSSEnabled:     "true",
		SSPort:           33899,
		SvcAnyTLSEnabled: "true",
		AnyTLSPort:       21111,
		Landings: []LandingService{
			{ID: "1", Name: "落地A", Enabled: true, Port: 30001},
			{ID: "2", Name: "落地B", Enabled: false, Port: 30001}, // 未启用，不冲突
			{ID: "3", Name: "落地C", Enabled: true, Port: 30002},
		},
	}
	msg := portConflicts(c)
	if msg != "" {
		t.Errorf("portConflicts 不应报告冲突，但返回: %s", msg)
	}
}

// TestLandingRemoteProbe 验证落地服务启用远端出口时，健康检测能额外探测远端 host:port。
// v1.5.29 回归：旧版只检测本机 landing 入站端口，远端 SS 不可达时仍显示「正常」。
func TestLandingRemoteProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	ok := LandingService{RemoteEnabled: true, RemoteHost: "127.0.0.1", RemotePort: port}
	if status, ms := probeLandingRemote(ok, time.Second); status != "yes" || ms < 0 {
		t.Fatalf("可达远端应返回 yes，got status=%s ms=%d", status, ms)
	}

	direct := LandingService{RemoteEnabled: false}
	if status, _ := probeLandingRemote(direct, time.Second); status != "skip" {
		t.Fatalf("remote_enabled=false 应跳过远端探测，got %s", status)
	}

	bad := LandingService{RemoteEnabled: true, RemoteHost: "127.0.0.1", RemotePort: 1}
	if status, _ := probeLandingRemote(bad, 100*time.Millisecond); status != "no" {
		t.Fatalf("不可达远端应返回 no，got %s", status)
	}
}

// TestSS2022KeyError 验证 SS2022 密钥错误能区分「不是合法 base64」与「长度不对」。
// v1.5.30 回归：旧部署里存在非法 SS_KEY 时，sing-box check 报 decode psk，
// 面板原错误只说长度错误，用户不知道是历史密钥格式坏了。
func TestSS2022KeyError(t *testing.T) {
	if msg := ss2022KeyError("2022-blake3-aes-128-gcm", "AAAAAAAAAAAAAAAAAAAAAA=="); msg != "" {
		t.Fatalf("合法 base64(16字节) 不应报错: %s", msg)
	}
	if msg := ss2022KeyError("2022-blake3-aes-128-gcm", "ABCDEFGHIJKLMNOPQRST:"); msg == "" || !contains(msg, "不是合法 base64") {
		t.Fatalf("非法 base64 应提示格式错误，got: %q", msg)
	}
	if msg := ss2022KeyError("2022-blake3-aes-128-gcm", "c2hvcnQ="); msg == "" || !contains(msg, "需 base64(16字节)") {
		t.Fatalf("合法 base64 但长度错误应提示字节数，got: %q", msg)
	}
}

func TestSS2022NormalizeKeyToSingBoxAcceptedStdBase64(t *testing.T) {
	// sing-box 的 password 字段不接受 RawStdEncoding（无 padding），即使 Go 能解码为 16 字节。
	// v1.5.31 回归：旧版 validSS2022Key 接受该格式，随后 sing-box check 失败于 input byte 20。
	norm, errMsg := normalizeSS2022Key("2022-blake3-aes-128-gcm", "AAAAAAAAAAAAAAAAAAAAAA")
	if errMsg != "" {
		t.Fatalf("可规范化的 raw base64 不应报错: %s", errMsg)
	}
	if norm != "AAAAAAAAAAAAAAAAAAAAAA==" {
		t.Fatalf("raw base64 应规范化为 sing-box 接受的带 padding 标准 base64，got %q", norm)
	}

	urlNorm, errMsg := normalizeSS2022Key("2022-blake3-aes-128-gcm", "________________AAAAAA")
	if errMsg != "" {
		t.Fatalf("可规范化的 urlsafe base64 不应报错: %s", errMsg)
	}
	if contains(urlNorm, "_") || len(urlNorm) != 24 {
		t.Fatalf("urlsafe base64 应规范化为标准 base64 且长度 24，got %q", urlNorm)
	}
}

// TestLandingsSSKeyValidation 验证 SS2022 密钥长度校验：短密钥应被拒绝。
// 2022-blake3-aes-128-gcm 需 base64(16字节)=24字符密钥。
func TestLandingsSSKeyValidation(t *testing.T) {
	// 正确长度（base64(16) = 24 字符）
	good := &LandingService{
		RemoteEnabled: true, RemoteType: "ss",
		RemoteHost: "192.0.2.1", RemotePort: 8388,
		RemoteMethod:   "2022-blake3-aes-128-gcm",
		RemotePassword: "AAAAAAAAAAAAAAAAAAAAAA==", // 24 字符
	}
	if msg := validateLandingRemote(good); msg != "" {
		t.Errorf("正确长度 SS2022 密钥被误拒: %s", msg)
	}

	// 错误长度（太短）
	bad := &LandingService{
		RemoteEnabled: true, RemoteType: "ss",
		RemoteHost: "192.0.2.1", RemotePort: 8388,
		RemoteMethod:   "2022-blake3-aes-128-gcm",
		RemotePassword: "c2hvcnQ=", // 合法 base64 但只有 5 字节
	}
	msg := validateLandingRemote(bad)
	if msg == "" {
		t.Errorf("错误长度 SS2022 密钥应被拒绝，但通过了校验")
	}
	if !contains(msg, "当前解码后") {
		t.Errorf("错误长度应给出详细字节数原因，got: %s", msg)
	}

	invalid := &LandingService{
		RemoteEnabled: true, RemoteType: "ss",
		RemoteHost: "192.0.2.1", RemotePort: 8388,
		RemoteMethod:   "2022-blake3-aes-128-gcm",
		RemotePassword: "ABCDEFGHIJKLMNOPQRST:",
	}
	if msg := validateLandingRemote(invalid); msg == "" || !contains(msg, "不是合法 base64") {
		t.Errorf("非法 base64 应给出格式原因，got: %s", msg)
	}

	raw := &LandingService{
		RemoteEnabled: true, RemoteType: "ss",
		RemoteHost: "192.0.2.1", RemotePort: 8388,
		RemoteMethod:   "2022-blake3-aes-128-gcm",
		RemotePassword: "AAAAAAAAAAAAAAAAAAAAAA",
	}
	if msg := validateLandingRemote(raw); msg != "" {
		t.Errorf("可规范化 raw base64 不应被拒: %s", msg)
	}
	if raw.RemotePassword != "AAAAAAAAAAAAAAAAAAAAAA==" {
		t.Errorf("落地 SS raw base64 应规范化为标准 base64，got %q", raw.RemotePassword)
	}
}

func TestLandingRemoteProbeDetail_SSInvalidKeyIncludesReasonAndTime(t *testing.T) {
	L := LandingService{
		RemoteEnabled:  true,
		RemoteType:     "ss",
		RemoteHost:     "127.0.0.1",
		RemotePort:     8388,
		RemoteMethod:   "2022-blake3-aes-128-gcm",
		RemotePassword: "ABCDEFGHIJKLMNOPQRST:",
	}
	p := probeLandingRemoteDetail(L, 100*time.Millisecond)
	if p.Status != "no" || p.Protocol != "ss" {
		t.Fatalf("非法 SS 配置应返回 no/ss，got status=%s protocol=%s", p.Status, p.Protocol)
	}
	if !contains(p.Reason, "不是合法 base64") {
		t.Fatalf("应返回详细 base64 错误原因，got: %s", p.Reason)
	}
	if p.Time == nil || p.Time.NowLocal == "" || p.Time.NowUTC == "" || p.Time.Timezone == "" {
		t.Fatalf("SS 失败诊断应包含当前时间/时区信息，got: %+v", p.Time)
	}
}

func TestLandingRemoteProbeDetail_TCPFailureIncludesReason(t *testing.T) {
	L := LandingService{RemoteEnabled: true, RemoteType: "ss", RemoteHost: "127.0.0.1", RemotePort: 1, RemoteMethod: "2022-blake3-aes-128-gcm", RemotePassword: "AAAAAAAAAAAAAAAAAAAAAA=="}
	p := probeLandingRemoteDetail(L, 50*time.Millisecond)
	if p.Status != "no" || p.Reason == "" {
		t.Fatalf("TCP 不可达应返回 no 且 reason 非空，got: %+v", p)
	}
	if p.Time == nil || p.Time.NowLocal == "" {
		t.Fatalf("SS 远端失败应包含时间诊断，got: %+v", p.Time)
	}
}

func TestClassifySSProbeError(t *testing.T) {
	cases := []struct{ in, want string }{
		{"shadowsocks: serve TCP: bad timestamp: diff 28800s", "时间戳校验失败"},
		{"decode psk: illegal base64 data", "认证/加密失败"},
		{"dial tcp 192.0.2.1:8388: i/o timeout", "探测超时"},
	}
	for _, tc := range cases {
		if got := classifySSProbeError(tc.in, ""); !contains(got, tc.want) {
			t.Fatalf("classifySSProbeError(%q)=%q, want contains %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeProbeLogRedactsSensitiveFields(t *testing.T) {
	log := `{"password":"secret-value","server":"127.0.0.1"} password=another-secret`
	got := sanitizeProbeLog(log)
	if contains(got, "secret-value") || contains(got, "another-secret") {
		t.Fatalf("探测日志应脱敏 password，got: %s", got)
	}
	if !contains(got, "127.0.0.1") {
		t.Fatalf("脱敏不应删除非敏感信息，got: %s", got)
	}
}

// TestNormalizeLanding_AutoEnableWhenFieldsComplete 是 v1.5.35 的核心回归测试。
// 复现用户报告的 bug：「新增第二个落地服务后，填了远端地址/端口/密钥但「启用远端」开关
// 仍是关闭 → remote_enabled=false → genconf 不生成 outbound/route → 第二个落地走 direct
// 泄漏中转 IP，且健康检测跳过远端协议探测」。修复：normalizeLanding 在远端字段齐全且
// 校验通过时自动把 RemoteEnabled 置 true。
func TestNormalizeLanding_AutoEnableWhenFieldsComplete(t *testing.T) {
	// 场景：SS 远端字段齐全但 remote_enabled=false（用户报告的脏数据）
	L := &LandingService{
		ID: "2", Name: "AL-SGP", Enabled: true, Port: 33088,
		RemoteEnabled: false, // ← 关键：用户填了字段但没开开关
		RemoteType:    "ss", RemoteHost: "198.51.100.2", RemotePort: 19922,
		RemoteMethod: "2022-blake3-aes-128-gcm", RemotePassword: "AAAAAAAAAAAAAAAAAAAAAA==",
	}
	normalizeLanding(L)
	if !L.RemoteEnabled {
		t.Fatalf("远端字段齐全时应自动启用 remote_enabled，got %+v", L)
	}
	if L.RemoteType != "ss" {
		t.Errorf("SS 类型应保留，got %q", L.RemoteType)
	}
	if L.RemoteUser != "" {
		t.Errorf("SS 类型应清空 remote_user（避免字段串味），got %q", L.RemoteUser)
	}
}

// TestNormalizeLanding_KeepsDisabledWhenFieldsIncomplete 验证字段不全时不自动启用，
// 避免误启用一个半成品配置。
func TestNormalizeLanding_KeepsDisabledWhenFieldsIncomplete(t *testing.T) {
	cases := []LandingService{
		{RemoteEnabled: false, RemoteHost: "", RemotePort: 0, RemotePassword: ""}, // 全空
		{RemoteEnabled: false, RemoteHost: "1.2.3.4", RemotePort: 0, RemotePassword: "x"}, // 缺 port
		{RemoteEnabled: false, RemoteHost: "1.2.3.4", RemotePort: 8388, RemotePassword: ""}, // 缺 password
		{RemoteEnabled: false, RemoteHost: "", RemotePort: 8388, RemotePassword: "AAAAAAAAAAAAAAAAAAAAAA=="}, // 缺 host
	}
	for i, L := range cases {
		orig := L
		normalizeLanding(&L)
		if L.RemoteEnabled {
			t.Errorf("case %d: 字段不全时不应自动启用，orig=%+v got=%+v", i, orig, L)
		}
	}
}

// TestNormalizeLanding_SOCKSAutoEnable 验证 SOCKS5 远端字段齐全也能自动启用。
func TestNormalizeLanding_SOCKSAutoEnable(t *testing.T) {
	L := &LandingService{
		ID: "3", Enabled: true, Port: 33090,
		RemoteEnabled: false,
		RemoteType:    "socks", RemoteHost: "198.51.100.7", RemotePort: 1080,
		RemoteUser: "socksuser", RemotePassword: "sockspass",
	}
	normalizeLanding(L)
	if !L.RemoteEnabled {
		t.Fatalf("SOCKS5 远端字段齐全时应自动启用，got %+v", L)
	}
	if L.RemoteMethod != "" {
		t.Errorf("SOCKS5 类型应清空 remote_method，got %q", L.RemoteMethod)
	}
}

// TestNormalizeLanding_RespectsExplicitDisable 验证用户显式清空字段后不会被强制启用。
// remote_enabled=false 且字段全空 → 保持 disabled（正常 direct 落地）。
func TestNormalizeLanding_RespectsExplicitDisable(t *testing.T) {
	L := &LandingService{
		ID: "4", Enabled: true, Port: 33091,
		RemoteEnabled: false,
		RemoteType: "ss", RemoteHost: "", RemotePort: 0, RemotePassword: "",
	}
	normalizeLanding(L)
	if L.RemoteEnabled {
		t.Errorf("字段全空时不应自动启用（保留用户 explicit direct 选择），got %+v", L)
	}
}

// TestMigrateLandings_FixesStaleEnabledFalse 是 v1.5.35 启动迁移回归测试：
// panel.json 里存在「远端字段齐全但 remote_enabled=false」的脏数据时，migrateLandings
// 应检测到变化并返回 true（main 据此落盘），确保容器/裸金属重启后旧脏数据自动修复。
func TestMigrateLandings_FixesStaleEnabledFalse(t *testing.T) {
	c := &Config{
		Domain: "your-domain.com",
		Landings: []LandingService{
			// 落地1：正常的已启用远端（不应变化）
			{ID: "1", Enabled: true, Port: 33001, RemoteEnabled: true, RemoteType: "ss",
				RemoteHost: "192.0.2.1", RemotePort: 8388, RemoteMethod: "2022-blake3-aes-128-gcm",
				RemotePassword: "AAAAAAAAAAAAAAAAAAAAAA=="},
			// 落地2：脏数据——字段齐全但 remote_enabled=false（应被修复）
			{ID: "2", Enabled: true, Port: 33002, RemoteEnabled: false, RemoteType: "ss",
				RemoteHost: "198.51.100.2", RemotePort: 8388, RemoteMethod: "2022-blake3-aes-128-gcm",
				RemotePassword: "BBBBBBBBBBBBBBBBBBBBBB=="},
			// 落地3：纯 direct 落地（字段全空，不应变化）
			{ID: "3", Enabled: true, Port: 33003, RemoteEnabled: false, RemoteType: "ss"},
		},
	}
	changed := migrateLandings(c)
	if !changed {
		t.Fatalf("存在脏数据时 migrateLandings 应返回 true")
	}
	if !c.Landings[1].RemoteEnabled {
		t.Fatalf("落地2 应被自动启用远端，got %+v", c.Landings[1])
	}
	if c.Landings[2].RemoteEnabled {
		t.Errorf("落地3 字段全空不应被启用，got %+v", c.Landings[2])
	}
}

func TestCleanupProbeTempDir(t *testing.T) {
	dir := t.TempDir()
	oldProbe := filepath.Join(dir, "ansgo-ss-probe-old.json")
	freshProbe := filepath.Join(dir, "ansgo-ss-probe-new.json")
	other := filepath.Join(dir, "keep.json")
	for _, p := range []string{oldProbe, freshProbe, other} {
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-20 * time.Minute)
	if err := os.Chtimes(oldProbe, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	cleanupProbeTempDir(dir, 10*time.Minute)
	if _, err := os.Stat(oldProbe); !os.IsNotExist(err) {
		t.Fatalf("过期 probe 临时文件应被清理，stat err=%v", err)
	}
	for _, p := range []string{freshProbe, other} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("不应清理 %s: %v", p, err)
		}
	}
}

// TestLandingsSocksValidation 验证 SOCKS5 远端必须有 user+password。
func TestLandingsSocksValidation(t *testing.T) {
	// 缺用户名
	missingUser := &LandingService{
		RemoteEnabled: true, RemoteType: "socks",
		RemoteHost: "198.51.100.1", RemotePort: 1080,
		RemoteUser: "", RemotePassword: "pass",
	}
	if msg := validateLandingRemote(missingUser); msg == "" {
		t.Errorf("SOCKS5 缺用户名应被拒绝")
	}
	// 完整
	good := &LandingService{
		RemoteEnabled: true, RemoteType: "socks",
		RemoteHost: "198.51.100.1", RemotePort: 1080,
		RemoteUser: "user", RemotePassword: "pass",
	}
	if msg := validateLandingRemote(good); msg != "" {
		t.Errorf("完整 SOCKS5 配置被误拒: %s", msg)
	}
}

// TestLandingsRemoteDisabled_NoValidation 验证 remote_enabled=false 时不校验远端（走 direct）。
func TestLandingsRemoteDisabled_NoValidation(t *testing.T) {
	l := &LandingService{
		RemoteEnabled: false, RemoteType: "ss",
		RemoteHost: "", RemotePort: 0, // 全空，但 remote 关闭
	}
	if msg := validateLandingRemote(l); msg != "" {
		t.Errorf("remote_enabled=false 时不应校验远端，但返回: %s", msg)
	}
}

// TestLandingsMultiRemoteIndependence 验证多个落地服务的远端配置各自独立、不串扰。
// 回归锁定场景：用户报"添加第二个落地服务时不走 web 面板配置的出口地址"。
// 根因排查：validateLandingRemote 按指针修改传入的 LandingService，必须确保对
// landing-2 的校验/规范化不会影响 landing-1 的配置（remote_host/port/password 各自独立）。
func TestLandingsMultiRemoteIndependence(t *testing.T) {
	l1 := &LandingService{
		ID: "1", RemoteEnabled: true, RemoteType: "ss",
		RemoteHost: "192.0.2.10", RemotePort: 8388,
		RemoteMethod:   "2022-blake3-aes-128-gcm",
		RemotePassword: "AAAAAAAAAAAAAAAAAAAAAA==",
	}
	l2 := &LandingService{
		ID: "2", RemoteEnabled: true, RemoteType: "ss",
		RemoteHost: "198.51.100.20", RemotePort: 8389,
		RemoteMethod:   "2022-blake3-aes-128-gcm",
		RemotePassword: "BBBBBBBBBBBBBBBBBBBBBB==",
	}
	// 分别校验（模拟 landingUpdateHandler 对每个落地独立调用 validateLandingRemote）
	if msg := validateLandingRemote(l1); msg != "" {
		t.Fatalf("landing-1 校验失败: %s", msg)
	}
	if msg := validateLandingRemote(l2); msg != "" {
		t.Fatalf("landing-2 校验失败: %s", msg)
	}
	// 校验后两个落地的远端配置必须各自保持原值（不串扰）
	if l1.RemoteHost != "192.0.2.10" || l1.RemotePort != 8388 {
		t.Errorf("landing-1 远端被串改: host=%s port=%d", l1.RemoteHost, l1.RemotePort)
	}
	if l2.RemoteHost != "198.51.100.20" || l2.RemotePort != 8389 {
		t.Errorf("landing-2 远端被串改: host=%s port=%d", l2.RemoteHost, l2.RemotePort)
	}
	// 密钥规范化各自独立（两个不同密钥不应变成相同）
	if l1.RemotePassword == l2.RemotePassword {
		t.Errorf("两个落地的远端密钥规范化后不应相同: l1=%s l2=%s", l1.RemotePassword, l2.RemotePassword)
	}
}

// TestLandingsRouteRulesPerLanding 验证多落地路由规则按 inbound tag 各自匹配，
// 不会让 landing-in-2 的流量错误走 landing-out-1。
// 这是 genconf python 逻辑的 Go 侧结构验证（genconf 生成 rules 按 landing id 遍历）。
func TestLandingsRouteRulesPerLanding(t *testing.T) {
	c := Config{Domain: "your-domain.com", SSPort: 33899}
	c.Landings = []LandingService{
		{ID: "1", Enabled: true, Port: 30001, RemoteEnabled: true, RemoteType: "ss", RemoteHost: "192.0.2.10", RemotePort: 8388, RemoteMethod: "2022-blake3-aes-128-gcm", RemotePassword: "AAAAAAAAAAAAAAAAAAAAAA=="},
		{ID: "2", Enabled: true, Port: 30002, RemoteEnabled: true, RemoteType: "ss", RemoteHost: "198.51.100.20", RemotePort: 8389, RemoteMethod: "2022-blake3-aes-128-gcm", RemotePassword: "BBBBBBBBBBBBBBBBBBBBBB=="},
	}
	// 模拟 genconf 的规则生成逻辑：每个 enabled+remote landing 生成独立 rule
	type rule struct{ inbound, outbound string }
	var rules []rule
	for _, L := range c.Landings {
		if !L.Enabled || !L.RemoteEnabled {
			continue
		}
		rules = append(rules, rule{inbound: "landing-in-" + L.ID, outbound: "landing-out-" + L.ID})
	}
	if len(rules) != 2 {
		t.Fatalf("两个启用的远端落地应生成 2 条规则，got %d", len(rules))
	}
	// 验证每条规则的 inbound 和 outbound tag 对应同一 id（不串）
	for _, r := range rules {
		inID := r.inbound[len("landing-in-"):]
		outID := r.outbound[len("landing-out-"):]
		if inID != outID {
			t.Errorf("路由规则 inbound/outbound id 不一致: %s -> %s", r.inbound, r.outbound)
		}
	}
	// 验证两个落地生成不同的 outbound tag
	if rules[0].outbound == rules[1].outbound {
		t.Errorf("两个落地不应生成相同 outbound tag: %s", rules[0].outbound)
	}
}

// TestRemoveSecretsByPrefix 验证删除落地服务时清理孤儿凭证。
// secrets.env 里的 LANDING_<id>_PASS/UUID 行应被删除，其他行保留。
func TestRemoveSecretsByPrefix(t *testing.T) {
	tmp := t.TempDir()
	secretsPath = filepath.Join(tmp, "secrets.env")
	content := `SS_KEY=dGVzdA==
ANYTLS_PASS=atpass
LANDING_1_PASS=l1pass
LANDING_1_UUID=uuid1
LANDING_2_PASS=l2pass
LANDING_2_UUID=uuid2
`
	os.WriteFile(secretsPath, []byte(content), 0600)

	// 删除落地服务 #2 的凭证
	if err := removeSecretsByPrefix("LANDING_2_"); err != nil {
		t.Fatalf("removeSecretsByPrefix: %v", err)
	}

	data, _ := os.ReadFile(secretsPath)
	got := string(data)
	// LANDING_2_* 应被删除
	if contains(got, "LANDING_2_PASS") || contains(got, "LANDING_2_UUID") {
		t.Errorf("LANDING_2_* 未被清理，仍存在: %s", got)
	}
	// 其他凭证应保留
	for _, want := range []string{"SS_KEY=", "ANYTLS_PASS=", "LANDING_1_PASS=", "LANDING_1_UUID="} {
		if !contains(got, want) {
			t.Errorf("清理后应保留 %s，但缺失。内容: %s", want, got)
		}
	}
}

// TestReadLandingSecrets 验证读取单个落地服务的凭证。
func TestReadLandingSecrets(t *testing.T) {
	tmp := t.TempDir()
	secretsPath = filepath.Join(tmp, "secrets.env")
	content := `SS_KEY=dGVzdA==
LANDING_1_PASS=mypass
LANDING_1_UUID=11111111-1111-1111-1111-111111111111
`
	os.WriteFile(secretsPath, []byte(content), 0600)

	pass, uuid, found := readLandingSecrets("1")
	if !found {
		t.Fatalf("readLandingSecrets(1) found=false, want true")
	}
	if pass != "mypass" {
		t.Errorf("pass = %q, want mypass", pass)
	}
	if uuid != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("uuid = %q", uuid)
	}

	// 不存在的落地 id
	_, _, found2 := readLandingSecrets("99")
	if found2 {
		t.Errorf("readLandingSecrets(99) should be not found")
	}
}

// TestBuildURIs_Landings 验证 buildURIs 为每个启用且有凭证的落地生成 URI（key=landing-<id>）。
func TestBuildURIs_Landings(t *testing.T) {
	tmp := t.TempDir()
	confPath = filepath.Join(tmp, "panel.json")
	secretsPath = filepath.Join(tmp, "secrets.env")
	// 两个落地：#1 有凭证（生成 URI），#2 无凭证（不生成），#3 未启用（不生成）
	os.WriteFile(confPath, []byte(`{"domain":"your-domain.com","landings":[
		{"id":"1","name":"A","enabled":true,"port":30001},
		{"id":"2","name":"B","enabled":true,"port":30002},
		{"id":"3","name":"C","enabled":false,"port":30003}
	]}`), 0600)
	os.WriteFile(secretsPath, []byte("LANDING_1_PASS=p1\nLANDING_1_UUID=u1\n"), 0600)

	c, _ := loadConfig()
	uris := buildURIs(c, secretData{})
	// landing-1 应有 URI
	if u, ok := uris["landing-1"]; !ok || u == "" {
		t.Errorf("landing-1 应生成 URI，got: %q (ok=%v)", u, ok)
	} else {
		t.Logf("landing-1 URI: %s", u)
	}
	// landing-2 无凭证，不应有 URI
	if u, ok := uris["landing-2"]; ok {
		t.Errorf("landing-2 无凭证不应生成 URI，got: %q", u)
	}
	// landing-3 未启用
	if u, ok := uris["landing-3"]; ok {
		t.Errorf("landing-3 未启用不应生成 URI，got: %q", u)
	}
}

// TestSanitizeLandingName 验证落地名称做 URL fragment 安全处理。
func TestSanitizeLandingName(t *testing.T) {
	cases := map[string]string{
		"示例SS落地":     "示例SS落地",     // 中文保留
		"HK Landing": "HK_Landing", // 空格 -> _
		"":           "unnamed",    // 空 -> unnamed
		"a#b?c/d":    "a_b_c_d",    // 特殊字符 -> _
		"  trim  ":   "trim",       // trim 后
	}
	for in, want := range cases {
		got := sanitizeLandingName(in)
		if got != want {
			t.Errorf("sanitizeLandingName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSetLandingPassword 验证落地服务页「AnyTLS 密码」输入框保存时，
// 会写回 LANDING_<id>_PASS，而不是只保存端口/远端配置。
// v1.5.28 回归：旧版前端有密码框，但 saveLanding 未提交，后端也没有字段处理，
// 用户改了落地 AnyTLS 密码后客户端拿到的是未生效密码，表现为新增落地不可用。
func TestSetLandingPassword(t *testing.T) {
	tmp := t.TempDir()
	secretsPath = filepath.Join(tmp, "secrets.env")
	content := `SS_KEY=dGVzdA==
LANDING_1_PASS=oldpass
LANDING_1_UUID=11111111-1111-1111-1111-111111111111
LANDING_2_PASS=keepme
`
	os.WriteFile(secretsPath, []byte(content), 0600)

	if err := setLandingPassword("1", "newpass"); err != nil {
		t.Fatalf("setLandingPassword: %v", err)
	}
	data, _ := os.ReadFile(secretsPath)
	got := string(data)
	if !contains(got, "LANDING_1_PASS=newpass") {
		t.Fatalf("LANDING_1_PASS 未更新为新密码，内容: %s", got)
	}
	if contains(got, "LANDING_1_PASS=oldpass") {
		t.Fatalf("旧 LANDING_1_PASS 仍存在，内容: %s", got)
	}
	if !contains(got, "LANDING_1_UUID=11111111-1111-1111-1111-111111111111") || !contains(got, "LANDING_2_PASS=keepme") {
		t.Fatalf("更新密码时不应破坏其他凭证，内容: %s", got)
	}
}

func TestSetLandingPasswordRejectsEmpty(t *testing.T) {
	if err := setLandingPassword("1", "   "); err == nil {
		t.Fatalf("空白落地 AnyTLS 密码应被拒绝")
	}
}

// contains 简单字符串包含（避免 import strings 的测试辅助）。
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
