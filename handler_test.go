package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddPassesValidatedArgumentsToDoqd(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "args.txt")
	stub := filepath.Join(dir, "doqd")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARG_LOG\"\nprintf 'added upstream\\n'\n"
	if err := os.WriteFile(stub, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ARG_LOG", logPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/add?url=quic%3A%2F%2Fdns.example.com&type=--fallback", nil)
	add(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read captured arguments: %v", err)
	}
	if string(got) != "add\n--fallback\nquic://dns.example.com\n" {
		t.Errorf("doqd arguments = %q", got)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	if msg := location.Query().Get("msg"); !strings.Contains(msg, "added upstream") {
		t.Errorf("redirect message = %q", msg)
	}
}

func TestAddRejectsUnsupportedTypeWithoutRunningDoqd(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	stub := filepath.Join(dir, "doqd")
	body := "#!/bin/sh\ntouch \"$CALL_MARKER\"\n"
	if err := os.WriteFile(stub, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CALL_MARKER", marker)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/add?url=quic%3A%2F%2Fdns.example.com&type=--unknown", nil)
	add(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("doqd stub was called for unsupported type (stat err: %v)", err)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	if msg := location.Query().Get("msg"); !strings.Contains(msg, "недопустимый тип") {
		t.Errorf("redirect message = %q", msg)
	}
}
