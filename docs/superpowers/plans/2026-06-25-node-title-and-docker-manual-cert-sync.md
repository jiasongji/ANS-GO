# Node Title and Docker Manual Certificate Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement node naming/title changes and Docker manual certificate host-path sync support with hidden copyable cron/BT task scripts, tests, release assets, and Docker image publishing.

**Architecture:** Keep `panel_title` as the stored compatibility field while adding display helpers for browser title and URI fragments. Docker manual certificates use fixed host sync directory `/etc/ansgo-docker/manual-certs` mounted to `/host/manual-certs`, then a container script validates/imports certs into `/etc/ssl/ansgo`; UI generates folded host-side sync scripts for system cron and BT panel tasks.

**Tech Stack:** Go panel (`deploy/panel`), vanilla JS embedded HTML, Bash install/upgrade/entrypoint scripts, Docker Compose, GitHub CLI, Docker buildx.

---

## File Structure

- Modify `deploy/panel/main.go`: add `CertHostFullchain`/`CertHostPrivkey` config fields, bump version, title/helper functions if appropriate.
- Modify `deploy/panel/handlers.go`: use display title helpers, URI fragments, cert config Docker metadata, Docker manual reload behavior.
- Modify `deploy/panel/web/index.html`: rename “网页标题” to “节点信息”, add `_ANS` title behavior, add Docker manual certificate instructions and folded copyable script blocks.
- Create `deploy/ansgo-sync-manual-cert`: container sync script for `/host/manual-certs` → `/etc/ssl/ansgo` with validation and change detection.
- Modify `deploy/Dockerfile.allinone`: include new sync script.
- Modify `deploy/docker-compose.yml`: fixed `/etc/ansgo-docker/manual-certs:/host/manual-certs:ro` mount.
- Modify `deploy/docker/entrypoint.sh`: use fixed sync path, preserve host source metadata, keep runtime cert paths in `/etc/ssl/ansgo`.
- Modify `install.sh`: bump version, create fixed sync dir, generate initial Docker manual sync file, stop dynamic host cert bind injection.
- Modify `deploy/upgrade.sh`: bump version, ensure fixed sync dir and compose mount for Docker upgrades, update release notes.
- Modify `deploy/panel/*_test.go` or add `deploy/panel/node_title_test.go`: Go tests for helper behavior and URI fragments.
- Modify docs (`README.md`, `deploy/README.md`, `AGENTS.md`): version notes and Docker manual cert workflow.

---

### Task 1: Branch and baseline verification

**Files:** none

- [ ] **Step 1: Create a feature branch from main**

Run:

```bash
git switch -c feat/node-title-docker-manual-cert-sync
```

Expected: branch switched with clean working tree except existing spec/plan files.

- [ ] **Step 2: Run current Go tests**

Run:

```bash
cd /Users/mac/Desktop/SRV/Proxy-VPS/BV-LAX/deploy/panel && go test ./...
```

Expected: PASS before implementation or existing failures recorded.

---

### Task 2: Add TDD coverage for node names and titles

**Files:**
- Create: `deploy/panel/node_title_test.go`
- Modify later: `deploy/panel/main.go`, `deploy/panel/handlers.go`

- [ ] **Step 1: Add failing tests**

Create `deploy/panel/node_title_test.go`:

```go
package main

import (
    "strings"
    "testing"
)

func TestNodeBaseNameAndPanelDisplayTitle(t *testing.T) {
    cases := []struct {
        name string
        in   Config
        base string
        title string
    }{
        {"empty", Config{}, "Manage", "Manage_ANS"},
        {"old default", Config{PanelTitle: "ANS-GO 管理面板"}, "Manage", "Manage_ANS"},
        {"custom", Config{PanelTitle: "NodeName"}, "NodeName", "NodeName_ANS"},
        {"trim", Config{PanelTitle: "  NodeName  "}, "NodeName", "NodeName_ANS"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            if got := nodeBaseName(tc.in); got != tc.base {
                t.Fatalf("nodeBaseName()=%q want %q", got, tc.base)
            }
            if got := panelDisplayTitle(tc.in); got != tc.title {
                t.Fatalf("panelDisplayTitle()=%q want %q", got, tc.title)
            }
        })
    }
}

func TestBuildURIsUseNodeShortFragments(t *testing.T) {
    c := Config{
        Domain: "your-domain.com", PanelTitle: "NodeName",
        SSPort: 10001, SSMethod: "2022-blake3-aes-128-gcm",
        AnyTLSPort: 10002, SocksPort: 10003, NaivePort: 10004,
    }
    s := secretData{
        SSKey: "aaaaaaaaaaaaaaaaaaaaaaaa",
        AnyTLSPass: "atpass",
        SocksUser: "suser", SocksPass: "spass",
        NaiveUser: "nuser", NaivePass: "npass",
    }
    uris := buildURIs(c, s)
    wants := map[string]string{
        "ss": "#NodeName-SS",
        "anytls": "#NodeName-AT",
        "socks": "#NodeName-SK",
        "naive": "#NodeName-NV",
    }
    for k, frag := range wants {
        if !strings.Contains(uris[k], frag) {
            t.Fatalf("%s URI %q does not contain %q", k, uris[k], frag)
        }
    }
}

func TestNodeFragmentSanitizesUnsafeCharacters(t *testing.T) {
    got := nodeFragment(Config{PanelTitle: "Node Name#/"}, "AT")
    if got != "Node_Name__-AT" {
        t.Fatalf("nodeFragment()=%q want %q", got, "Node_Name__-AT")
    }
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
cd /Users/mac/Desktop/SRV/Proxy-VPS/BV-LAX/deploy/panel && go test ./... -run 'TestNodeBaseName|TestBuildURIsUseNode|TestNodeFragment'
```

Expected: FAIL because helpers are undefined and URI fragments still use `ANS-GO-*`.

---

### Task 3: Implement node title and URI helpers

**Files:**
- Modify: `deploy/panel/main.go`
- Modify: `deploy/panel/handlers.go`
- Modify: `deploy/panel/web/index.html`

- [ ] **Step 1: Add helper functions in Go**

Add near `normalizePath` in `deploy/panel/main.go`:

```go
func nodeBaseName(c Config) string {
    title := strings.TrimSpace(c.PanelTitle)
    if title == "" || title == "ANS-GO 管理面板" {
        return "Manage"
    }
    return title
}

func panelDisplayTitle(c Config) string {
    return nodeBaseName(c) + "_ANS"
}

func safeFragmentPart(s string) string {
    s = strings.TrimSpace(s)
    if s == "" {
        return "Manage"
    }
    var b strings.Builder
    for _, r := range s {
        switch r {
        case ' ', '\t', '\n', '\r', '#', '?', '/', '\\', '&', '<', '>', '"', '\'':
            b.WriteRune('_')
        default:
            b.WriteRune(r)
        }
    }
    return b.String()
}

func nodeFragment(c Config, suffix string) string {
    return safeFragmentPart(nodeBaseName(c)) + "-" + safeFragmentPart(suffix)
}
```

- [ ] **Step 2: Use display title and URI fragments**

In `deploy/panel/handlers.go`:

- Replace root title fallback logic with `panelDisplayTitle(c)`.
- In `buildURIs`, replace:
  - `ANS-GO-SS` with `nodeFragment(c, "SS")`
  - `ANS-GO-AnyTLS` with `nodeFragment(c, "AT")`
  - `ANS-GO-SOCKS5` with `nodeFragment(c, "SK")`
  - `ANS-GO-Naive` with `nodeFragment(c, "NV")`
  - landing fragment with `nodeFragment(c, "LD-"+sanitizeLandingName(L.Name))` or `nodeFragment(c, "LD-"+L.ID)` when name empty.

- [ ] **Step 3: Update frontend label and title save behavior**

In `deploy/panel/web/index.html`:

- Change settings label `网页标题` to `节点信息`.
- Change placeholder from `ANS-GO 管理面板` to `例如 NodeName`.
- Add JS helper:

```js
function nodeBaseName(v){v=(v||'').trim();return (!v||v==='ANS-GO 管理面板')?'Manage':v}
function panelDisplayTitle(v){return nodeBaseName(v)+'_ANS'}
```

- Change save success from `document.title=body.panel_title||'ANS-GO 管理面板'` to `document.title=panelDisplayTitle(body.panel_title)`.

- [ ] **Step 4: Run tests**

Run:

```bash
cd /Users/mac/Desktop/SRV/Proxy-VPS/BV-LAX/deploy/panel && go test ./... -run 'TestNodeBaseName|TestBuildURIsUseNode|TestNodeFragment'
```

Expected: PASS.

---

### Task 4: Add Docker manual certificate config fields and tests

**Files:**
- Modify: `deploy/panel/main.go`
- Modify: `deploy/panel/cert_config_test.go`
- Modify: `deploy/panel/handlers.go`

- [ ] **Step 1: Add failing config tests**

Append to `deploy/panel/cert_config_test.go` a test that creates temp config, sets `CertHostFullchain`/`CertHostPrivkey`, saves and reloads via JSON, and checks fields persist.

```go
func TestConfig_DockerHostCertFieldsPersist(t *testing.T) {
    c := Config{
        Domain: "your-domain.com",
        CertMode: "manual",
        CertFullchain: "/etc/ssl/ansgo/fullchain.pem",
        CertPrivkey: "/etc/ssl/ansgo/privkey.pem",
        CertHostFullchain: "/www/server/panel/vhost/cert/your-domain.com/fullchain.pem",
        CertHostPrivkey: "/www/server/panel/vhost/cert/your-domain.com/privkey.pem",
    }
    b, err := json.Marshal(c)
    if err != nil { t.Fatal(err) }
    var got Config
    if err := json.Unmarshal(b, &got); err != nil { t.Fatal(err) }
    if got.CertHostFullchain != c.CertHostFullchain || got.CertHostPrivkey != c.CertHostPrivkey {
        t.Fatalf("host cert fields not persisted: %#v", got)
    }
}
```

- [ ] **Step 2: Add fields to Config**

In `deploy/panel/main.go`:

```go
CertHostFullchain string `json:"cert_host_fullchain"`
CertHostPrivkey   string `json:"cert_host_privkey"`
```

- [ ] **Step 3: Include fields in cert config GET/POST**

In `certConfigHandler` GET include:

```go
"host_fullchain": c.CertHostFullchain,
"host_privkey": c.CertHostPrivkey,
"runtime_fullchain": runtimeFullchain,
"runtime_privkey": runtimePrivkey,
"docker_mode": isDockerMode(),
```

POST body adds:

```go
HostFullchain *string `json:"host_fullchain"`
HostPrivkey *string `json:"host_privkey"`
```

For Docker manual mode: store host fields and set runtime cert paths to `/etc/ssl/ansgo/fullchain.pem` and `/etc/ssl/ansgo/privkey.pem`; skip `os.ReadFile` on host source paths.

- [ ] **Step 4: Run tests**

Run:

```bash
cd /Users/mac/Desktop/SRV/Proxy-VPS/BV-LAX/deploy/panel && go test ./...
```

Expected: PASS.

---

### Task 5: Add sync script and shell tests by direct execution

**Files:**
- Create: `deploy/ansgo-sync-manual-cert`
- Modify: `deploy/Dockerfile.allinone`
- Modify: `deploy/docker/entrypoint.sh`

- [ ] **Step 1: Create sync script**

Create `deploy/ansgo-sync-manual-cert` with Bash script that reads `/host/manual-certs/fullchain.pem` and `/host/manual-certs/privkey.pem`, validates via `openssl`, compares with `/etc/ssl/ansgo`, copies atomically, and exits 10 when changed, 0 when unchanged.

- [ ] **Step 2: Make entrypoint use the script**

In Docker manual branch of `deploy/docker/entrypoint.sh`:

- prefer `/host/manual-certs` if present;
- fallback to old env `CERT_FULLCHAIN`/`CERT_PRIVKEY` for compatibility;
- set `panel.json` runtime paths to `/etc/ssl/ansgo/...` and host metadata from env when available.

- [ ] **Step 3: Include script in image**

In `deploy/Dockerfile.allinone`, copy and chmod `deploy/ansgo-sync-manual-cert` to `/usr/local/bin/ansgo-sync-manual-cert`.

- [ ] **Step 4: Validate shell syntax**

Run:

```bash
bash -n /Users/mac/Desktop/SRV/Proxy-VPS/BV-LAX/deploy/ansgo-sync-manual-cert
bash -n /Users/mac/Desktop/SRV/Proxy-VPS/BV-LAX/deploy/docker/entrypoint.sh
```

Expected: no output, exit 0.

---

### Task 6: Update Docker install/upgrade fixed sync directory

**Files:**
- Modify: `deploy/docker-compose.yml`
- Modify: `install.sh`
- Modify: `deploy/upgrade.sh`

- [ ] **Step 1: Add compose mount**

In `deploy/docker-compose.yml` volumes add:

```yaml
      - /etc/ansgo-docker/manual-certs:/host/manual-certs:ro
```

- [ ] **Step 2: Update install.sh Docker branch**

Add creation of `/etc/ansgo-docker/manual-certs`. In Docker manual initial install, copy CLI-provided cert/key into this directory instead of injecting dynamic host bind mounts. Bump `VER` and header from `v1.5.31` to the new release version.

- [ ] **Step 3: Update upgrade.sh Docker branch**

Ensure Docker upgrade creates `/etc/ansgo-docker/manual-certs`, ensures compose mount exists idempotently, pulls new image, and force recreates. Bump `VER` and release notes.

- [ ] **Step 4: Syntax check scripts**

Run:

```bash
bash -n /Users/mac/Desktop/SRV/Proxy-VPS/BV-LAX/install.sh
bash -n /Users/mac/Desktop/SRV/Proxy-VPS/BV-LAX/deploy/upgrade.sh
```

Expected: no output, exit 0.

---

### Task 7: Update certificate UI folded scripts

**Files:**
- Modify: `deploy/panel/web/index.html`
- Modify: `deploy/panel/handlers.go` if API data shape needs adjustment

- [ ] **Step 1: Add shell quoting and script generation helpers in JS**

Add JS helpers:

```js
function shq(s){return "'"+String(s||'').replace(/'/g,"'\\''")+"'"}
function dockerManualSyncScript(full,key){/* returns BT task script */}
function dockerManualCronInstallScript(full,key){/* returns one-shot install script */}
```

The generated scripts must copy host source paths to `/etc/ansgo-docker/manual-certs`, run `docker exec ansgo ansgo-sync-manual-cert`, then reload only when sync reports changed.

- [ ] **Step 2: Add folded UI blocks**

In Docker manual section render two collapsed blocks:

- `方案一：系统自动任务一键安装`
- `方案二：宝塔计划任务脚本`

Each expands on click and has a copy button.

- [ ] **Step 3: Update reload button**

For Docker manual mode, label as `🔄 同步并重新加载证书` and call `api/cert/reload`, expecting backend to run sync before reload.

- [ ] **Step 4: JS syntax check**

Run a script extraction/check command or `node --check` on extracted inline JS.

Expected: syntax valid.

---

### Task 8: Backend Docker manual reload behavior

**Files:**
- Modify: `deploy/panel/handlers.go`

- [ ] **Step 1: Add Docker mode helper**

Implement:

```go
func isDockerMode() bool {
    if os.Getenv("ANSGO_DOCKER") == "1" { return true }
    if _, err := os.Stat("/.dockerenv"); err == nil { return true }
    return false
}
```

- [ ] **Step 2: Update certReloadHandler**

For Docker manual mode: first execute `/usr/local/bin/ansgo-sync-manual-cert`; if it exits 10 or 0, execute `/usr/local/bin/ansgo-cert-reload`; if source files missing, return actionable error telling user to run generated system/BT task script first.

- [ ] **Step 3: Run Go tests**

Run:

```bash
cd /Users/mac/Desktop/SRV/Proxy-VPS/BV-LAX/deploy/panel && go test ./...
```

Expected: PASS.

---

### Task 9: Documentation and version update

**Files:**
- Modify: `AGENTS.md`
- Modify: `README.md`
- Modify: `deploy/README.md`
- Modify: `deploy/panel/main.go`
- Modify binaries after build

- [ ] **Step 1: Set version**

Bump version to `1.5.32` in Go and scripts, and tag text to `v1.5.32`.

- [ ] **Step 2: Add release notes**

Add v1.5.32 summary:

- node info title `_ANS` and URI short suffixes;
- Docker manual cert host path sync scripts;
- fixed missing `ansgo-sync-manual-cert` command;
- upgrade script ensures Docker fixed sync mount.

- [ ] **Step 3: Sensitive info scan**

Run repository grep for real identity patterns and ensure no new sensitive info.

---

### Task 10: Build, audit, release, Docker publish, commit

**Files:** generated assets and git metadata

- [ ] **Step 1: Build panel binaries**

Run Go builds for linux amd64/arm64 with `-ldflags "-X main.version=1.5.32"` into release asset locations and update `deploy/panel/ansgo-panel-linux-*` if that is existing project convention.

- [ ] **Step 2: Run full checks**

Run:

```bash
cd deploy/panel && go test ./...
bash -n install.sh deploy/upgrade.sh deploy/docker/entrypoint.sh deploy/ansgo-sync-manual-cert
```

Expected: all pass.

- [ ] **Step 3: Subagent audit**

Dispatch at least two read-only agents:

- frontend/backend behavior audit;
- Docker shell/upgrade audit.

Fix findings, rerun checks.

- [ ] **Step 4: Commit**

Run:

```bash
git add .
git commit -m "feat(v1.5.32): 优化节点命名与 Docker 手动证书同步"
```

- [ ] **Step 5: Publish GitHub release**

Use `gh release create v1.5.32` and upload required assets:

- `ansgo-panel-linux-amd64`
- `ansgo-panel-linux-arm64`
- caddy/sing-box existing release assets if required by install script convention.

- [ ] **Step 6: Publish Docker image**

Run `docker buildx build --push` for `linux/amd64,linux/arm64` tags:

- `ghcr.io/jiasongji/ansgo:latest`
- `ghcr.io/jiasongji/ansgo:v1.5.32`

- [ ] **Step 7: Confirm upgrade path**

Verify:

```bash
gh release view v1.5.32 --json assets
docker buildx imagetools inspect ghcr.io/jiasongji/ansgo:v1.5.32
```

Expected: release assets present and multi-arch image published. `deploy/upgrade.sh` points to `v1.5.32` so users can run the one-click upgrade script.
