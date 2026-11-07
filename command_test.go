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
		args []string
		want time.Duration
	}{
		{"doqd status", "doqd", []string{"status"}, 30 * time.Second},
		{"absolute doqd list", "/opt/sbin/doqd", []string{"list"}, 15 * time.Second},
		{"doqd test", "doqd", []string{"test", "dns.example"}, 15 * time.Second},
		{"doqd hosts", "doqd", []string{"hosts", "status"}, 3 * time.Second},
		{"doqd version", "doqd", []string{"-version"}, 3 * time.Second},
		{"other executable", "other", []string{"list"}, 3 * time.Second},
		{"doqd with no args", "doqd", nil, 3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandTimeout(tc.name, tc.args...); got != tc.want {
				// Use the executable name in the actual function call; the table's
				// name is only descriptive.
				t.Fatalf("commandTimeout(%q, %v) = %s, want %s", tc.name, tc.args, got, tc.want)
			}
		})
	}
}

func shellCommand(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess test")
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSlowListCompletes(t *testing.T) {
	path := shellCommand(t, "doqd", "sleep 3.2\nprintf 'upstream alive\\n'")
	// commandTimeout chooses its long timeout by executable basename.
	if got := run(path, "list"); got != "upstream alive" {
		t.Fatalf("slow list: %q", got)
	}
}

func TestCommandDeadlineIsReportedWithPartialOutput(t *testing.T) {
	path := shellCommand(t, "slow", "printf 'partial output\\n'\nexec sleep 5")
	got := runWithTimeout(path, 50*time.Millisecond)
	if !strings.Contains(got, "превышено время ожидания") || !strings.Contains(got, "partial output") || strings.Contains(got, "signal: killed") {
		t.Fatalf("timeout lost cause or partial output: %q", got)
	}
}

func TestExternalKillIsNotReportedAsDeadline(t *testing.T) {
	path := shellCommand(t, "killed", "kill -KILL $$")
	got := runWithTimeout(path, time.Second)
	if !strings.Contains(got, "signal: killed") || strings.Contains(got, "превышено время ожидания") {
		t.Fatalf("external kill misreported: %q", got)
	}
}

func TestCappedOutputBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		writes []int
		wantTruncated bool
	}{
		{"below limit", []int{maxOutput - 1}, false},
		{"exact limit", []int{maxOutput}, false},
		{"one byte over", []int{maxOutput, 1}, true},
		{"over in chunks", []int{4096, maxOutput}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b cappedOutput
			for _, size := range tc.writes {
				p := []byte(strings.Repeat("x", size))
				if n, err := b.Write(p); n != len(p) || err != nil {
					t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(p))
				}
			}
			if b.text.Len() > maxOutput {
				t.Fatalf("buffer length = %d, exceeds %d", b.text.Len(), maxOutput)
			}
			if b.truncated != tc.wantTruncated {
				t.Errorf("truncated = %v, want %v", b.truncated, tc.wantTruncated)
			}
			if got := strings.HasSuffix(b.String(), "\n... (обрезано)"); got != tc.wantTruncated {
				t.Errorf("truncation marker = %v, want %v", got, tc.wantTruncated)
			}
		})
	}
}
