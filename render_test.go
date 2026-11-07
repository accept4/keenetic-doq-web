package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeCommandStub installs a deterministic executable in a temporary PATH.
// This keeps tests independent of tools installed on the developer's machine.
func writeCommandStub(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func setDoqdStub(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := writeCommandStub(t, dir, "doqd", body)
	t.Setenv("PATH", dir)
	return path
}

func TestRenderPageHTMLAndEscapesUserContent(t *testing.T) {
	statusMu.Lock()
	oldStatus, oldList, oldUpdated := statusText, listText, updatedAt
	statusText, listText, updatedAt = `<script>alert("status")</script>`, `<img src=x>`, time.Now()
	statusMu.Unlock()
	t.Cleanup(func() {
		statusMu.Lock()
		statusText, listText, updatedAt = oldStatus, oldList, oldUpdated
		statusMu.Unlock()
	})

	rec := httptest.NewRecorder()
	page(rec, httptest.NewRequest(http.MethodGet, "/?msg=%3Cscript%3Ealert%281%29%3C%2Fscript%3E", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.HasSuffix(strings.TrimSpace(body), "</html>") {
		t.Error("rendered page does not end with </html>")
	}
	if strings.Contains(body, "%[") {
		t.Error("unrendered format placeholder in page output")
	}
	for _, unsafe := range []string{`<script>alert("status")</script>`, `<img src=x>`, `<script>alert(1)</script>`} {
		if strings.Contains(body, unsafe) {
			t.Errorf("unescaped content in HTML: %q", unsafe)
		}
	}
}

func TestPageServesCurrentMethods(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			page(rec, httptest.NewRequest(method, "/", nil))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
		})
	}
}

func TestStatusAPIJSONContract(t *testing.T) {
	statusMu.Lock()
	oldStatus, oldList, oldUpdated := statusText, listText, updatedAt
	statusText, listText, updatedAt = "doqd running", "upstream alive", time.Now()
	statusMu.Unlock()
	t.Cleanup(func() {
		statusMu.Lock()
		statusText, listText, updatedAt = oldStatus, oldList, oldUpdated
		statusMu.Unlock()
	})

	rec := httptest.NewRecorder()
	statusAPI(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var got struct {
		OK        bool   `json:"ok"`
		Label     string `json:"label"`
		Age       int    `json:"age"`
		Status    string `json:"status"`
		List      string `json:"list"`
		Installed bool   `json:"installed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.OK || got.Status != "doqd running" || got.List != "upstream alive" || got.Age < 0 {
		t.Errorf("unexpected status response: %+v", got)
	}
	if !strings.Contains(got.Label, "running") {
		t.Errorf("label = %q, want running state", got.Label)
	}
}

func TestAddRejectsInvalidInputWithoutRunningCommand(t *testing.T) {
	// An empty PATH makes accidental command execution fail deterministically.
	t.Setenv("PATH", t.TempDir())
	rec := httptest.NewRecorder()
	add(rec, httptest.NewRequest(http.MethodPost, "/add?url=example.com.", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "msg=") {
		t.Errorf("redirect missing validation message: %q", loc)
	}
}

func TestOnlyPOSTRejectsNonPOSTMethods(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodPut, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			called := false
			h := onlyPOST(func(http.ResponseWriter, *http.Request) { called = true })
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequest(method, "/restart", nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
			if called {
				t.Error("wrapped handler was called for non-POST method")
			}
			if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
				t.Errorf("Allow = %q, want POST", allow)
			}
		})
	}
}

func TestOnlyPOSTAllowsPOST(t *testing.T) {
	called := false
	h := onlyPOST(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/restart", nil))
	if !called || rec.Code != http.StatusNoContent {
		t.Errorf("POST: called=%v status=%d", called, rec.Code)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	in := "Установка: OK\nPID=12345 & #/"
	encoded := encode(in)
	if strings.ContainsAny(encoded, "&=") {
		t.Errorf("encoded value contains unescaped query delimiters: %q", encoded)
	}
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		t.Fatalf("QueryUnescape: %v", err)
	}
	if decoded != in {
		t.Errorf("round trip = %q, want %q", decoded, in)
	}
}

func TestDoRefreshPopulatesCacheFromStub(t *testing.T) {
	setDoqdStub(t, `case "$1" in
status) printf 'doqd running (pid 123)\\n' ;;
list) printf 'upstream alive\\n' ;;
*) exit 2 ;;
esac`)
	statusMu.Lock()
	oldStatus, oldList, oldUpdated := statusText, listText, updatedAt
	statusText, listText, updatedAt = "", "", time.Time{}
	statusMu.Unlock()
	t.Cleanup(func() {
		statusMu.Lock()
		statusText, listText, updatedAt = oldStatus, oldList, oldUpdated
		statusMu.Unlock()
	})

	doRefresh()
	status, list, updated := currentStatus()
	if status != "doqd running (pid 123)" || list != "upstream alive" || updated.IsZero() {
		t.Fatalf("unexpected refreshed cache: status=%q list=%q updated=%v", status, list, updated)
	}
}

func TestInstalledChecksAllPlatformFiles(t *testing.T) {
	dir := t.TempDir()
	oldBinary, oldConf, oldInit := doqdBinary, doqdConfPath, doqdInitScript
	doqdBinary = filepath.Join(dir, "doqd")
	doqdConfPath = filepath.Join(dir, "doqd.conf")
	doqdInitScript = filepath.Join(dir, "init-doqd")
	t.Cleanup(func() { doqdBinary, doqdConfPath, doqdInitScript = oldBinary, oldConf, oldInit })

	if installed() {
		t.Fatal("installed() = true when platform files are missing")
	}
	for _, path := range []string{doqdBinary, doqdConfPath, doqdInitScript} {
		if err := os.WriteFile(path, []byte("stub"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !installed() {
		t.Error("installed() = false when all platform files exist")
	}
}

func TestHostsMode(t *testing.T) {
	dir := t.TempDir()
	old := doqdConfPath
	doqdConfPath = filepath.Join(dir, "doqd.conf")
	t.Cleanup(func() { doqdConfPath = old })

	cases := []struct {
		name, config, wantMode, wantPath string
	}{
		{"builtin", "listen 192.168.1.1:5354\nhosts builtin\n", "builtin", ""},
		{"file", "hosts /opt/etc/doqd.hosts\n", "file", "/opt/etc/doqd.hosts"},
		{"comment ignored", "# hosts /tmp/x\n", "builtin", ""},
		{"missing directive", "upstream quic://dns.example.net\n", "builtin", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(doqdConfPath, []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			mode, path := hostsMode()
			if mode != tc.wantMode || path != tc.wantPath {
				t.Errorf("hostsMode() = %q, %q; want %q, %q", mode, path, tc.wantMode, tc.wantPath)
			}
		})
	}
	// An unreadable/missing config falls back to builtin mode.
	doqdConfPath = filepath.Join(dir, "missing.conf")
	if mode, path := hostsMode(); mode != "builtin" || path != "" {
		t.Errorf("missing config: hostsMode() = %q, %q", mode, path)
	}
}

func TestSetHostsDirectiveReplacesAppendsAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	old := doqdConfPath
	doqdConfPath = filepath.Join(dir, "doqd.conf")
	t.Cleanup(func() { doqdConfPath = old })

	original := "hosts builtin\nupstream quic://a.example\n"
	if err := os.WriteFile(doqdConfPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setHostsDirective("/opt/etc/doqd.hosts"); err != nil {
		t.Fatalf("replace directive: %v", err)
	}
	got, err := os.ReadFile(doqdConfPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "hosts /opt/etc/doqd.hosts") || strings.Contains(string(got), "hosts builtin") {
		t.Errorf("directive was not replaced: %q", got)
	}
	backup, err := os.ReadFile(doqdConfPath + ".bak")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup = %q, want original %q", backup, original)
	}

	if err := os.WriteFile(doqdConfPath, []byte("upstream quic://a.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setHostsDirective("builtin"); err != nil {
		t.Fatalf("append directive: %v", err)
	}
	got, err = os.ReadFile(doqdConfPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "hosts builtin") {
		t.Errorf("directive was not appended: %q", got)
	}
}

func TestHostsSaveRejectsBuiltinMode(t *testing.T) {
	dir := t.TempDir()
	old := doqdConfPath
	doqdConfPath = filepath.Join(dir, "doqd.conf")
	t.Cleanup(func() { doqdConfPath = old })
	if err := os.WriteFile(doqdConfPath, []byte("hosts builtin\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"content": {"5.175.161.3 example.com\n"}}
	req := httptest.NewRequest(http.MethodPost, "/hosts/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	hostsSave(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	msg, err := url.QueryUnescape(strings.TrimPrefix(rec.Header().Get("Location"), "/?msg="))
	if err != nil {
		t.Fatalf("decode redirect: %v", err)
	}
	if !strings.Contains(msg, "сначала создайте свой файл") {
		t.Errorf("redirect message = %q", msg)
	}
}

func TestHostsStatusJSONUsesStubAndReportsMode(t *testing.T) {
	setDoqdStub(t, `printf 'hosts status: builtin\\n'`)
	dir := t.TempDir()
	old := doqdConfPath
	doqdConfPath = filepath.Join(dir, "missing.conf")
	t.Cleanup(func() { doqdConfPath = old })

	rec := httptest.NewRecorder()
	hostsStatusJSON(rec, httptest.NewRequest(http.MethodGet, "/hosts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["mode"] != "builtin" || got["status"] != "hosts status: builtin\n" {
		t.Errorf("unexpected hosts response: %#v", got)
	}
}

func TestHostsStatusRejectsNonGET(t *testing.T) {
	rec := httptest.NewRecorder()
	hostsStatusJSON(rec, httptest.NewRequest(http.MethodPost, "/hosts", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestPageHasHostsUIAndNoRemovedGeoUI(t *testing.T) {
	rec := httptest.NewRecorder()
	page(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	for _, banned := range []string{"/geo", "add-domain", "remove-domain", "--geo", "Geo-серверы"} {
		if strings.Contains(body, banned) {
			t.Errorf("page still references removed command %q", banned)
		}
	}
	for _, want := range []string{"/hosts/check", "/hosts/reload", "/hosts/save", "hostsEnableForm"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing hosts UI %q", want)
		}
	}
}

func TestApplyPlatform(t *testing.T) {
	oldConf, oldInit, oldBinary := doqdConfPath, doqdInitScript, doqdBinary
	oldInstaller, oldVersion, oldHosts := doqdInstaller, versionFile, customHostsPath
	t.Cleanup(func() {
		doqdConfPath, doqdInitScript, doqdBinary = oldConf, oldInit, oldBinary
		doqdInstaller, versionFile, customHostsPath = oldInstaller, oldVersion, oldHosts
	})

	applyPlatform(false)
	if doqdConfPath != "/opt/etc/doqd.conf" || doqdInitScript != "/opt/etc/init.d/S56doqd" ||
		doqdBinary != "/opt/sbin/doqd" || !strings.HasSuffix(doqdInstaller, "/install.sh") ||
		versionFile != "/opt/etc/doq-web/version.txt" || customHostsPath != "/opt/etc/doqd.hosts" {
		t.Errorf("Keenetic paths: %q %q %q %q %q %q", doqdConfPath, doqdInitScript, doqdBinary, doqdInstaller, versionFile, customHostsPath)
	}

	applyPlatform(true)
	if doqdConfPath != "/etc/doqd.conf" || doqdInitScript != "/etc/init.d/doqd" ||
		doqdBinary != "/usr/sbin/doqd" || !strings.HasSuffix(doqdInstaller, "/install-openwrt.sh") ||
		versionFile != "/etc/doq-web/version.txt" || customHostsPath != "/etc/doqd/hosts" {
		t.Errorf("OpenWRT paths: %q %q %q %q %q %q", doqdConfPath, doqdInitScript, doqdBinary, doqdInstaller, versionFile, customHostsPath)
	}
}

func TestInstallRouteGuardDoesNotRunInstallerForGET(t *testing.T) {
	called := false
	h := onlyPOST(func(http.ResponseWriter, *http.Request) { called = true })
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/install", nil))
	if rec.Code != http.StatusMethodNotAllowed || called {
		t.Errorf("GET /install: status=%d called=%v", rec.Code, called)
	}
}
