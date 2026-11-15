package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCommandTimeouts(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  string
		want time.Duration
	}{
		{"doqd", "status", 30 * time.Second},
		{"/opt/sbin/doqd", "list", 15 * time.Second},
		{"doqd", "test", 15 * time.Second},
		{"doqd", "-version", 3 * time.Second},
		{"other", "list", 3 * time.Second},
	} {
		if got := commandTimeout(tc.name, tc.arg); got != tc.want {
			t.Errorf("%s %s: got %s, want %s", tc.name, tc.arg, got, tc.want)
		}
	}
}

func shellCommand(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess test")
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

// Regression: a slow list used to be killed after 3 s, before it printed its table.
func TestSlowListCompletes(t *testing.T) {
	path := shellCommand(t, "doqd", "sleep 3.2\nprintf 'upstream alive\\n'")
	if got := run(path, "list"); got != "upstream alive" {
		t.Fatalf("slow list: %q", got)
	}
}

func TestCommandDeadlineIsReported(t *testing.T) {
	path := shellCommand(t, "slow", "printf 'partial output\\n'\nexec sleep 5")
	got := runWithTimeout(path, 50*time.Millisecond)
	if !strings.Contains(got, "превышено время ожидания") || !strings.Contains(got, "partial output") || strings.Contains(got, "signal: killed") {
		t.Fatalf("timeout lost cause or output: %q", got)
	}
}

func TestExternalKillIsNotReportedAsDeadline(t *testing.T) {
	path := shellCommand(t, "killed", "kill -KILL $$")
	got := runWithTimeout(path, time.Second)
	if !strings.Contains(got, "signal: killed") || strings.Contains(got, "превышено время ожидания") {
		t.Fatalf("external kill misreported: %q", got)
	}
}

func TestCappedOutput(t *testing.T) {
	var b cappedOutput
	for i := 0; i < 100; i++ {
		p := []byte(strings.Repeat("x", 4096))
		if n, err := b.Write(p); n != len(p) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if b.text.Len() != maxOutput || !strings.HasSuffix(b.String(), "\n... (обрезано)") {
		t.Fatalf("output not capped: %d bytes", b.text.Len())
	}
}

// ---------- Bootstrap tests ----------

func TestBootstrapIPValid(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{"77.88.8.8", true},
		{"77.88.8.8:53", true},
		{"77.88.8.8:1253", true},
		{"8.8.8.8:53", true},
		{"1.1.1.1:53", true},
		{"::1", true},
		{"::1:53", true},
		{"2001:db8::1:53", true},
		{"google.com", false},
		{"", false},
		{"  ", false},
		{"77.88.8.8:abc", false},
		{"77.88.8.8:0", false},
		{"77.88.8.8:65536", false},
	} {
		got := bootstrapIPValid(tc.input)
		if got != tc.want {
			t.Errorf("bootstrapIPValid(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestBootstrapParseAndValidate(t *testing.T) {
	tests := []struct {
		input      string
		wantErr    bool
		wantOutput []string
	}{
		{"77.88.8.8:53 8.8.8.8:53", false, []string{"77.88.8.8:53", "8.8.8.8:53"}},
		{"", false, nil}, // пустая строка = сброс на встроенные
		{"77.88.8.8:53", false, []string{"77.88.8.8:53"}},
		{"google.com", true, nil},
		{"77.88.8.8:53 google.com", true, nil},
	}
	for _, tc := range tests {
		got, err := bootstrapParseAndValidate(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("bootstrapParseAndValidate(%q) expected error, got %v", tc.input, got)
			}
		} else {
			if err != nil {
				t.Errorf("bootstrapParseAndValidate(%q) unexpected error: %v", tc.input, err)
			}
			if len(got) != len(tc.wantOutput) {
				t.Errorf("bootstrapParseAndValidate(%q) len=%d, want %d", tc.input, len(got), len(tc.wantOutput))
				continue
			}
			for i := range got {
				if got[i] != tc.wantOutput[i] {
					t.Errorf("bootstrapParseAndValidate(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.wantOutput[i])
				}
			}
		}
	}
}

func TestBootstrapDirective(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "doqd.conf")
	original := []byte("listen 127.0.0.1:5354\nhosts builtin\nupstream quic://dns.example.com\n")
	if err := os.WriteFile(conf, original, 0644); err != nil {
		t.Fatal(err)
	}
	// Тестируем на временном конфиге
	origConf := doqdConfPath
	doqdConfPath = conf
	defer func() { doqdConfPath = origConf }()

	// bootstrapGet пустой, если директивы нет
	if got := bootstrapGet(); len(got) != 0 {
		t.Errorf("bootstrapGet() = %v, want empty slice", got)
	}

	// Добавляем директиву
	if err := setBootstrapDirective([]string{"77.88.8.8:53", "8.8.8.8:53"}); err != nil {
		t.Fatal(err)
	}
	if got := bootstrapGet(); len(got) != 2 || got[0] != "77.88.8.8:53" || got[1] != "8.8.8.8:53" {
		t.Errorf("bootstrapGet() = %v, want [77.88.8.8:53 8.8.8.8:53]", got)
	}

	// Удаляем директиву
	if err := setBootstrapDirective(nil); err != nil {
		t.Fatal(err)
	}
	if got := bootstrapGet(); len(got) != 0 {
		t.Errorf("bootstrapGet() = %v, want empty slice", got)
	}
}
