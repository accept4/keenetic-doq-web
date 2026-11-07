package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// statusAppearance centralizes the page status style without changing the UI rules.
func statusAppearance(status string) (dot, title, label string) {
	if strings.Contains(status, "running") {
		return "var(--green)", "running", "● running"
	}
	return "var(--red)", "stopped", "● stopped"
}

// statusLabel keeps the same label format as the page renderer and the status API.
func statusLabel(status string, age int) string {
	_, _, label := statusAppearance(status)
	return label + fmt.Sprintf(" · %d с", age)
}

// redirectWithMessage keeps the redirect contract unchanged while removing repeated URL-encode boilerplate.
func redirectWithMessage(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/?msg="+url.QueryEscape(message), http.StatusSeeOther)
}
