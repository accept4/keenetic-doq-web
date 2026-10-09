package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// pageHTML содержит 7 плейсхолдеров %[1]s … %[7]d. page() передаёт ровно 7
// аргументов: dot, title, label, msgHTML, status, list, age.
func TestRenderPageHTML(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	page(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "</html>") {
		t.Errorf("output does not end with </html>")
	}
}

func TestStatusAPI(t *testing.T) {
	rec := httptest.NewRecorder()
	statusAPI(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := rec.Body.String()
	for _, key := range []string{`"ok"`, `"label"`, `"age"`, `"status"`, `"list"`, `"installed"`} {
		if !strings.Contains(body, key) {
			t.Errorf("JSON missing key %s", key)
		}
	}
}

// page() отдаёт страницу на любой метод — в коде нет проверки r.Method.
func TestPageServesAnyMethod(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		rec := httptest.NewRecorder()
		page(rec, httptest.NewRequest(method, "/", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", method, rec.Code)
		}
	}
}

// Добавление апстрима вызывает run("doqd", "add", url) и редирект с msg.
func TestAddRedirects(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/add?url=quic://test.example.com", nil)
	req.ParseForm()
	add(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (StatusSeeOther)", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/?msg=") {
		t.Errorf("redirect Location missing msg: %q", loc)
	}
}

// Удаление апстрима вызывает run("doqd", "remove", id).
func TestRemoveRedirects(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/remove?id=1", nil)
	req.ParseForm()
	remove(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (StatusSeeOther)", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/?msg=") {
		t.Errorf("redirect Location missing msg: %q", loc)
	}
}

// Тестирование сервера не должно падать, даже если URL невалидный.
func TestTestRedirects(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/test?url=quic://bad", nil)
	req.ParseForm()
	testHandler := test // имя функции в doq-web.go — test
	testHandler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (StatusSeeOther)", rec.Code)
	}
}

// Рестарт демона вызывает run("/opt/etc/init.d/S56doqd", "restart").
func TestRestartRedirects(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/restart", nil)
	restart(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (StatusSeeOther)", rec.Code)
	}
}

// Установка из инсталлера запускает curl | sh и редирект с результатом.
func TestInstallRedirects(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/install", nil)
	install(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (StatusSeeOther)", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/?msg=") {
		t.Errorf("redirect Location missing msg: %q", loc)
	}
	// Location URL-закодирован, поэтому ищем закодированный вариант.
	if !strings.Contains(loc, "%D0%A3%D1%81%D1%82%D0%B0%D0%BD%D0%BE%D0%B2%D0%BA%D0%B0%3A") {
		t.Errorf("redirect Location missing prefix 'Установка:': %q", loc)
	}
}

// encode() URL-кодирует текст, чтобы его можно было безопасно передать в ?msg=
// после 303 Redirect.
func TestEncode(t *testing.T) {
	in := "Установка: OK\nPID=12345"
	enc := encode(in)
	// Результат не должен содержать & или = (незакодированные).
	if strings.Contains(enc, "&") || strings.Contains(enc, "=") {
		t.Errorf("encode(%q) contains unescaped chars: %q", in, enc)
	}
	// При декодировании должно получиться исходное.
	dec, err := url.QueryUnescape(enc)
	if err != nil {
		t.Fatalf("QueryUnescape: %v", err)
	}
	if dec != in {
		t.Errorf("round-trip failed: got %q", dec)
	}
}

// installed() проверяет наличие трёх файлов /opt/... — на Windows возвращает false.
func TestInstalled(t *testing.T) {
	if installed() {
		t.Errorf("installed() returned true on this platform (expected false)")
	}
}

// doRefresh заполняет кэш statusText/listText/updatedAt. Это проверяет, что
// шаг 3 (первый doRefresh внутри refresher) действительно даёт непустой статус
// вместо "stopped · 621... с" на пустом кэше.
func TestDoRefreshPopulatesCache(t *testing.T) {
	// Сброс кэша (как будто только что запустились).
	statusMu.Lock()
	statusText, listText, updatedAt = "", "", time.Time{}
	statusMu.Unlock()

	doRefresh()

	status, list, up := currentStatus()
	if status == "" {
		t.Errorf("doRefresh didn't populate statusText")
	}
	if list == "" {
		t.Errorf("doRefresh didn't populate listText")
	}
	if up.IsZero() {
		t.Errorf("doRefresh didn't set updatedAt")
	}
	t.Logf("status=%q list=%q up=%v", status, list, up)
}

// onlyPOST обязан отсечь GET до того, как хендлер тронет конфигурацию:
// префектч браузера, боты и сканеры не должны удалять домены.
func TestOnlyPOSTRejectsGET(t *testing.T) {
	called := false
	h := onlyPOST(func(w http.ResponseWriter, r *http.Request) { called = true })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/remove?d=example.com", nil)
	h(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: код %d,.want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if called {
		t.Error("GET: хендлер вызван, мутация выполнена")
	}
	if allow := rec.Header().Get("Allow"); allow != "POST" {
		t.Errorf("заголовок Allow = %q, want %q", allow, "POST")
	}
}

func TestOnlyPOSTRejectsHeadAndDelete(t *testing.T) {
	for _, m := range []string{http.MethodHead, http.MethodDelete, http.MethodPut, http.MethodOptions} {
		called := false
		h := onlyPOST(func(w http.ResponseWriter, r *http.Request) { called = true })
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(m, "/add", nil))

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: код %d, want %d", m, rec.Code, http.StatusMethodNotAllowed)
		}
		if called {
			t.Errorf("%s: хендлер вызван", m)
		}
	}
}

// POST обязан доходить до хендлера — иначе форма перестала бы работать.
func TestOnlyPOSTAllowsPOST(t *testing.T) {
	called := false
	h := onlyPOST(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/add?d=example.com", nil))

	if !called {
		t.Error("POST: хендлер не вызван")
	}
	if rec.Code == http.StatusMethodNotAllowed {
		t.Error("POST: получил 405")
	}
}

// domainASCII отсекает конечную точку (idna.ToASCII("example.com.") →
// "example.com.", что ломает домен). Числовой ID разрешён.
func TestDomainASCII(t *testing.T) {
	bad := []string{
		"example.com.",
		"",
		"   ",
		"quic://",
	}
	for _, d := range bad {
		if _, err := domainASCII(d); err == nil {
			t.Errorf("domainASCII(%q) = nil, want error", d)
		}
	}

	good := []string{
		"42",
		"quic://dns.example.com",
		"example.com",
		"xn--80ak6aa92e.com",
	}
	for _, d := range good {
		if _, err := domainASCII(d); err != nil {
			t.Errorf("domainASCII(%q) = %v, want nil", d, err)
		}
	}
}

// Строка ровно как её печатает rc.func из Entware. Без stripANSI page()
// прогоняет ESC через html.EscapeString и на экране остаётся
// «[1;37m Shutting down doqd... [m [1;32m done. [m».
func TestControlOutputStripsColor(t *testing.T) {
	in := "\x1b[1;37m Shutting down doqd... \x1b[m            \x1b[1;32m done. \x1b[m"
	want := "Shutting down doqd... done."
	if got := controlOutput(in); got != want {
		t.Errorf("controlOutput = %q, want %q", got, want)
	}
}

// Многострочный вывод и кириллица должны пережить зачистку, а sequence,
// оборванный усечением на maxOutput, обязан исчезнуть целиком — иначе
// незакрытый ESC снова доедает html.EscapeString.
func TestStripANSIMultilineAndBrokenTail(t *testing.T) {
	in := "● running (pid 1234)\nапстрим \x1b[32mquic://dns.example.com\x1b[0m\n\x1b[1;3"
	want := "● running (pid 1234)\nапстрим quic://dns.example.com\n"
	if got := stripANSI(in); got != want {
		t.Errorf("stripANSI = %q, want %q", got, want)
	}
}

// controlOutput схлопывает отступы rc.func, но НЕ склеивает строки,
// а stripANSI не трогает пробелы вовсе: «doqd list» печатает таблицу,
// у неё выравнивание по колонкам — часть смысла.
func TestControlOutputKeepsLinesAndListKeepsColumns(t *testing.T) {
	in := "Установлен бинарник:   /opt/sbin/doq-web\n   rc.custom недоступен\n"
	want := "Установлен бинарник: /opt/sbin/doq-web\nrc.custom недоступен"
	if got := controlOutput(in); got != want {
		t.Errorf("controlOutput = %q, want %q", got, want)
	}

	table := "1  quic://a.example.com  ok\n2  quic://b.example.com  timeout"
	if got := stripANSI(table); got != table {
		t.Errorf("stripANSI исказил таблицу: %q", got)
	}
}
