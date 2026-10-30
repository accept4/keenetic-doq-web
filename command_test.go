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
