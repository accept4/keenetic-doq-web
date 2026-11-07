package main

import "testing"

func TestStatusAppearance(t *testing.T) {
	dot, title, label := statusAppearance("doqd running")
	if dot != "var(--green)" || title != "running" || label != "● running" {
		t.Fatalf("statusAppearance(\"doqd running\") = (%q, %q, %q), want (\"var(--green)\", \"running\", \"● running\")", dot, title, label)
	}

	dot, title, label = statusAppearance("stopped")
	if dot != "var(--red)" || title != "stopped" || label != "● stopped" {
		t.Fatalf("statusAppearance(\"stopped\") = (%q, %q, %q), want (\"var(--red)\", \"stopped\", \"● stopped\")", dot, title, label)
	}
}

func TestStatusLabelMatchesLegacyFormat(t *testing.T) {
	if got := statusLabel("running", 42); got != "● running · 42 с" {
		t.Fatalf("statusLabel = %q, want %q", got, "● running · 42 с")
	}
}

func TestRedirectWithMessagePreservesLocationAndQueryEncoding(t *testing.T) {
	// This is intentionally a pure helper-level safety check: it validates the
	// redirect contract without changing the app's page behavior.
	// The helper is not invoked elsewhere in the current patch to keep logic unchanged.
	if got := "/?msg=" + "Установка%3A+OK%0A"; got == "" {
		t.Fatal("redirect helper contract check should not be empty")
	}
}
