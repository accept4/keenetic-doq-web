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
		name     string
		exe      string
		args     []string
		want     time.Duration
	}{
		{name: "doqd status", exe: "doqd", args: []string{"status"}, want: 30 * time.Second},
		{name: "absolute doqd list", exe: "/opt/sbin/doqd", args: []string{"list"}, want: 15 * time.Second},
		{name: "doqd test", exe: "doqd", args: []string{"test", "dns.example"}, want: 15 * time.Second},
		{name: "doqd hosts", exe: "doqd", args: []string{"hosts", "status"}, want: 3 * time.Second},
		{name: "doqd version", exe: "doqd", args: []string{"-version"}, want: 3 * time.Second},
		{name: "other executable", exe: "other", args: []string{"list"}, want: 3 * time.Second},
		{name: "doqd with no args", exe: "doqd", want: 3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandTimeout(tc.exe, tc.args...); got != tc.want {
				t.Fatalf("commandTimeout(%q, %v) = %s, want %s", tc.exe, tc.args, got, tc.want)
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

func TestCommandOutputAndErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix subprocess test")
	}

	t.Run("trims successful output", func(t *testing.T) {
		path := shellCommand(t, "trim-output", "printf '  result  \\n'")
		if got := runWithTimeout(path, time.Second); got != "result" {
			t.Errorf("runWithTimeout() = %q, want %q", got, "result")
		}
	})

	t.Run("includes output on nonzero exit", func(t *testing.T) {
		path := shellCommand(t, "failed", "printf 'stdout\\n'\nprintf 'stderr\\n' >&2\nexit 7")
		got := runWithTimeout(path, time.Second)
		for _, want := range []string{"stdout", "stderr", "exit status 7"} {
			if !strings.Contains(got, want) {
				t.Errorf("output %q does not contain %q", got, want)
			}
		}
	})
}

func TestUnavailableCommandIsReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has a platform-specific unavailable-command response")
	}
	t.Setenv("PATH", t.TempDir())
	got := runWithTimeout("keenetic-doq-command-that-does-not-exist", time.Second)
	if !strings.Contains(got, "недоступен") {
		t.Errorf("unavailable command response = %q", got)
	}
}

func TestCappedOutputBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name          string
		writes        []int
		wantTruncated bool
	}{
		{name: "below limit", writes: []int{maxOutput - 1}},
		{name: "exact limit", writes: []int{maxOutput}},
		{name: "one byte over", writes: []int{maxOutput, 1}, wantTruncated: true},
		{name: "over in chunks", writes: []int{4096, maxOutput}, wantTruncated: true},
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
			gotMarker := strings.HasSuffix(b.String(), "\n... (обрезано)")
			if gotMarker != tc.wantTruncated {
				t.Errorf("truncation marker = %v, want %v", gotMarker, tc.wantTruncated)
			}
		})
	}
}
