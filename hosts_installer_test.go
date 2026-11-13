package main

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveHostsFileRestoresRejectedCandidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	original := []byte("192.0.2.1 old.example\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := saveHostsFile(path, []byte("invalid candidate\n"), func() string {
		return "Ошибка: invalid hosts"
	})
	if err == nil || !strings.Contains(out, "invalid hosts") {
		t.Fatalf("saveHostsFile = %q, %v; want validation failure", out, err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != string(original) {
		t.Fatalf("restored file = %q, %v; want %q", got, readErr, original)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("restored mode = %v, %v; want 0600", info, statErr)
		}
	}
}

func TestSaveHostsFileRemovesRejectedNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	_, err := saveHostsFile(path, []byte("invalid candidate\n"), func() string {
		return "Ошибка: invalid hosts"
	})
	if err == nil {
		t.Fatal("saveHostsFile succeeded for invalid candidate")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("new invalid file still exists: %v", statErr)
	}
}

func TestDownloadVerifiedInstaller(t *testing.T) {
	script := []byte("#!/bin/sh\necho ok\n")
	blobHeader := fmt.Sprintf("blob %d\x00", len(script))
	sum := sha1.Sum(append([]byte(blobHeader), script...))
	shaPayload, err := json.Marshal(installerContents{
		Content:  base64.StdEncoding.EncodeToString(script),
		Encoding: "base64",
		SHA:      fmt.Sprintf("%x", sum),
	})
	if err != nil {
		t.Fatal(err)
	}

	rawSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(script)
	}))
	defer rawSrv.Close()

	shaCount := 0
	shaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		shaCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(shaPayload)
	}))
	defer shaSrv.Close()

	got, err := downloadVerifiedInstaller(context.Background(), rawSrv.URL, shaSrv.URL)
	if err != nil || string(got) != string(script) {
		t.Fatalf("downloadVerifiedInstaller = %q, %v", got, err)
	}
	if shaCount == 0 {
		t.Error("SHA endpoint was never called")
	}
}

func TestDownloadVerifiedInstallerRejectsBadSHA(t *testing.T) {
	script := []byte("#!/bin/sh\necho ok\n")
	badSHA := "0000000000000000000000000000000000000000"

	shaPayload, err := json.Marshal(installerContents{
		Content:  base64.StdEncoding.EncodeToString(script),
		Encoding: "base64",
		SHA:      badSHA,
	})
	if err != nil {
		t.Fatal(err)
	}

	rawSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(script)
	}))
	defer rawSrv.Close()

	shaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(shaPayload)
	}))
	defer shaSrv.Close()

	if _, err := downloadVerifiedInstaller(context.Background(), rawSrv.URL, shaSrv.URL); err == nil || !strings.Contains(err.Error(), "SHA") {
		t.Fatalf("downloadVerifiedInstaller error = %v, want SHA mismatch", err)
	}
}
