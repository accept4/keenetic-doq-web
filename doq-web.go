package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Страница не запускает doqd сама: единственная пара форков живёт в фоновом
// обновляторе. Причина не в памяти (профиль памяти роутера я так и не
// измерил), а в том, что и `status`, и `list` делают НАСТОЯЩИЕ сетевые
// запросы: status резолвит имя через doqd и через :53, list пробует каждый
// апстрим живым QUIC-запросом. Форкать это на каждый хит страницы = держать
// в UI запрос, который в норме тянет 150-400 мс, а на мёртвом апстриме
// упирается в таймаут.
//
// Про «потерю связи»: README апстрима прямо пишет, что doqd add/remove
// ПЕРЕЗАПУСКАЮТ демона ("restarting the daemon ... alive (pid ...)"). Порт
// 5354 на несколько секунд исчезает по штатному замыслу doqd. Это не OOM и
// не баг UI, поэтому после add/remove обновление откладывается (см.
// refreshAfterRestart), а не снимает статус мгновенно.
const (
	refreshEvery        = 30 * time.Second
	refreshAfterRestart = 4 * time.Second
)

// Версия doqd:
//   - установленная — из `doqd -version` (флаг появился в SmartDNS-сборках);
//     файл version.txt, который панель писала после install/update, остался
//     как запасной вариант для старых бинарников без флага;
//   - последняя версия апстрима — из GitHub API (кэшируется, лимит
//     анонимных запросов 60/ч с одного IP).
const (
	upstreamRepo    = "necronicle/keenetic-doq"
	latestRefreshIt = 6 * time.Hour
)

// Пути и установщик doqd зависят от платформы: на Keenetic всё живёт в
// Entware (/opt), на OpenWRT апстрим ставит демона в /usr/sbin, конфиг в
// /etc/doqd.conf, а автозапуск — procd-скрипт /etc/init.d/doqd. Платформа
// определяется по /etc/openwrt_release, которого на KeeneticOS нет.
var (
	doqdConfPath    = "/opt/etc/doqd.conf"
	doqdInitScript  = "/opt/etc/init.d/S56doqd"
	doqdBinary      = "/opt/sbin/doqd"
	doqdInstaller   = "https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install.sh"
	versionFile     = "/opt/etc/doq-web/version.txt"
	customHostsPath = "/opt/etc/doqd.hosts"
)

func init() {
	_, err := os.Stat("/etc/openwrt_release")
	applyPlatform(err == nil)
}

// applyPlatform выставляет пути под платформу. Вынесено из init() и
// принимает явный флаг — тесты могут проверить обе платформы на любом хосте.
func applyPlatform(openwrt bool) {
	if !openwrt {
		doqdConfPath = "/opt/etc/doqd.conf"
		doqdInitScript = "/opt/etc/init.d/S56doqd"
		doqdBinary = "/opt/sbin/doqd"
		doqdInstaller = "https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install.sh"
		versionFile = "/opt/etc/doq-web/version.txt"
		customHostsPath = "/opt/etc/doqd.hosts"
		return
	}
	doqdConfPath = "/etc/doqd.conf"
	doqdInitScript = "/etc/init.d/doqd"
	doqdBinary = "/usr/sbin/doqd"
	doqdInstaller = "https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install-openwrt.sh"
	versionFile = "/etc/doq-web/version.txt"
	customHostsPath = "/etc/doqd/hosts"
}

// webVersion — версия самой панели; подставляется при сборке через
// -ldflags "-X main.webVersion=v1.0.x". Флаг -version отдаёт её установщику.
var webVersion = "dev"

var (
	latestMu      sync.Mutex
	latestVer     string
	latestFetched time.Time
)

// latestUpstream — tag_name последнего релиза keenetic-doq. Пустая строка
// означает "не удалось получить" (нет интернета/лимит API) — UI это
// переживает молча.
func latestUpstream() string {
	latestMu.Lock()
	defer latestMu.Unlock()
	if latestVer != "" && time.Since(latestFetched) < latestRefreshIt {
		return latestVer
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+upstreamRepo+"/releases/latest", nil)
	if err != nil {
		return latestVer
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return latestVer
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return latestVer
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil || rel.TagName == "" {
		return latestVer
	}
	latestVer, latestFetched = rel.TagName, time.Now()
	return latestVer
}

// installedDoqdVersion — версия doqd: сначала `doqd -version` (кэш 6 ч,
// форкать демона на каждый хит страницы незачем), при неудаче — version.txt
// от прошлых установок панели.
func installedDoqdVersion() string {
	if v := doqdVersion(); v != "" {
		return v
	}
	b, err := os.ReadFile(versionFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

var (
	doqdVerMu      sync.Mutex
	doqdVer        string
	doqdVerFetched time.Time
)

// doqdVersion парсит вывод `doqd -version` ("doqd 0.6.1"). run() на ошибке
// возвращает строку "Ошибка: ...", она не матчится и трактуется как пусто.
func doqdVersion() string {
	doqdVerMu.Lock()
	defer doqdVerMu.Unlock()
	if doqdVer != "" && time.Since(doqdVerFetched) < latestRefreshIt {
		return doqdVer
	}
	out := run("doqd", "-version")
	if v := strings.TrimSpace(strings.TrimPrefix(out, "doqd ")); v != out && v != "" {
		doqdVer, doqdVerFetched = v, time.Now()
		return doqdVer
	}
	return doqdVer
}

// saveInstalledDoqdVersion пишет установленную версию в version.txt.
// Вызывается после успешного install/update. Ошибку глотаем: не записанный
// файл только лишает UI строки "установлено", но не ломает работу.
func saveInstalledDoqdVersion() {
	v := latestUpstream()
	if v == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(versionFile), 0o755); err != nil {
		return
	}
	os.WriteFile(versionFile, []byte(v+"\n"), 0o644)
}

// listenAddr — адрес веб-интерфейса. ":8091" означает "все интерфейсы",
// иначе UI недоступен с LAN (127.0.0.1 = только сам роутер).
// 8091 согласован с install.sh.
const listenAddr = ":8091"

var (
	statusMu   sync.Mutex
	statusText string
	listText   string
	updatedAt  time.Time
	refresh    = make(chan struct{}, 1)
)

// currentStatus только читает кэш — блокировки на время форков нет,
// параллельные запросы страницы не встают в очередь на секунды.
func currentStatus() (string, string, time.Time) {
	statusMu.Lock()
	defer statusMu.Unlock()
	return statusText, listText, updatedAt
}

// requestRefresh неблокирующий: если внеочередное обновление уже в очереди,
// второй сигнал не нужен.
func requestRefresh() {
	select {
	case refresh <- struct{}{}:
	default:
	}
}

// requestRefreshAfter — отложенное обновление. doqd add/remove/remove сами
// перезапускают демона (см. README), поэтому мгновенный status почти гарантированно
// показал бы "stopped" и "потерю связи" сразу после успешного действия.
func requestRefreshAfter(d time.Duration) {
	time.AfterFunc(d, requestRefresh)
}

// doRefresh вызывается из одной горутины: фоновые status/list не перекрываются.
func doRefresh() {
	s := run("doqd", "status")
	l := run("doqd", "list")
	statusMu.Lock()
	statusText, listText, updatedAt = s, l, time.Now()
	statusMu.Unlock()
}

func refresher() {
	// Первый снимок — сразу при старте. Без этого первые refreshEvery
	// секунд page() отдаёт пустой статус и возраст, отсчитанный от
	// нулевого time.Time: на экране "● stopped · 621... с", а на mipsle
	// int 32-битный и число ещё и переполняется.
	doRefresh()
	for {
		select {
		case <-refresh:
		case <-time.After(refreshEvery):
		}
		doRefresh()
	}
}

func commandAvailable(name string) error {
	if filepath.IsAbs(name) {
		_, err := os.Stat(name)
		return err
	}
	_, err := exec.LookPath(name)
	return err
}

// commandTimeout учитывает сетевые проверки doqd 0.6.x: list ждёт до 8 с,
// test — до 12 с, status последовательно делает два DNS-запроса по 10 с.
// Короткий общий таймаут убивал эти команды раньше их собственного дедлайна.
func commandTimeout(name string, args ...string) time.Duration {
	if filepath.Base(name) == "doqd" && len(args) > 0 {
		switch args[0] {
		case "status":
			return 30 * time.Second
		case "list", "test":
			return 15 * time.Second
		}
	}
	return 3 * time.Second
}

// run выбирает таймаут по команде. Для остальных долгих операций
// используйте runWithTimeout().
func run(name string, args ...string) string {
	return runWithTimeout(name, commandTimeout(name, args...), args...)
}

// cappedOutput ограничивает память во время чтения, а не после завершения
// процесса. Лишние байты принимаются и отбрасываются, чтобы не блокировать его.
type cappedOutput struct {
	text      strings.Builder
	truncated bool
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxOutput - b.text.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.text.Write(p)
	return n, nil
}

func (b *cappedOutput) String() string {
	if b.truncated {
		return b.text.String() + "\n... (обрезано)"
	}
	return b.text.String()
}

// runWithTimeout — как run(), но с настраиваемым таймаутом.
func runWithTimeout(name string, timeout time.Duration, args ...string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("Ошибка: %s недоступен (на ПК нет doqd, это нормально)", name)
	}
	if err := commandAvailable(name); err != nil {
		return fmt.Sprintf("Ошибка: %s недоступен (%v)", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// Не ждать бесконечно унаследованные дочерними процессами stdout/stderr.
	cmd.WaitDelay = time.Second
	var output cappedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	out := stripANSI(output.String())
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("Ошибка: %s: превышено время ожидания (%s)\n%s", name, timeout, out)
	}
	if err != nil {
		return fmt.Sprintf("Ошибка: %v\n%s", err, out)
	}
	return strings.TrimSpace(out)
}

// ansiSequence — экранные sequence вида ESC [ <параметры> <финал>, которыми
// rc.func из Entware красит «done.». Финальный байт CSI — в диапазоне @-~
// (m для SGR), параметры — цифры и ';'. Литерал '[' записан классом [[]
// чтобы не экранировать скобку.
var ansiSequence = regexp.MustCompile("\x1b[[][0-9;]*[@-~]")

// stripANSI вырезает цветовые sequence из вывода. Без этого page() гонит их
// через html.EscapeString, который съедает байт ESC и оставляет на экране
// мусор: «Restart: [1;37m Shutting down doqd... [m [1;32m done. [m».
//
// Заодно снимаются \r (rc.func на части прошивок печатает CRLF) и хвост
// sequence, оборванного усечением буфера на maxOutput: после ReplaceAllString
// любой оставшийся ESC по построению незавершён.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return strings.ReplaceAll(s, "\r", "")
	}
	s = ansiSequence.ReplaceAllString(s, "")
	if i := strings.LastIndexByte(s, 0x1b); i >= 0 {
		s = s[:i]
	}
	return strings.ReplaceAll(s, "\r", "")
}

// controlOutput форматирует вывод init-скриптов (restart/stop/start): rc.func
// набивает «done.» длинными отступами, и в msg вырастает простыня пробелов.
// Схлопывание пробелов применяется ТОЛЬКО здесь — «doqd list» печатает
// таблицу, у которой выравнивание по колонкам важнее экономии места.
func controlOutput(s string) string {
	lines := strings.Split(stripANSI(s), "\n")
	for i, ln := range lines {
		lines[i] = strings.Join(strings.Fields(ln), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func main() {
	// -version — как у doqd: печать версию и выйти. Установщик вызывает
	// бинарник с этим флагом, чтобы убедиться, что он запускается на данной
	// архитектуре. Без выхода main() ушёл бы в ListenAndServe и установщик
	// завис бы навсегда.
	for _, arg := range os.Args[1:] {
		switch arg {
		case "-version", "--version":
			fmt.Println("doq-web", webVersion)
			return
		case "--probe":
			os.Exit(runProbe())
		}
	}

	http.HandleFunc("/", page)
	http.HandleFunc("/api/status", statusAPI)
	http.HandleFunc("/api/version", versionAPI)
	// Мутирующие маршруты — только POST. Ссылка вида /install?id= или
	// /stop, попавшая в превью мессенджера/антивирусного сканера или
	// prefetch браузера, иначе выполняется сама по себе.
	http.HandleFunc("/add", onlyPOST(add))
	http.HandleFunc("/remove", onlyPOST(remove))
	http.HandleFunc("/test", test)
	http.HandleFunc("/restart", onlyPOST(restart))
	http.HandleFunc("/stop", onlyPOST(stop))
	http.HandleFunc("/start", onlyPOST(start))
	http.HandleFunc("/install", onlyPOST(install))
	http.HandleFunc("/update", onlyPOST(update))
	// Hosts (подмена адресов) — замена geo-domain из doqd <=0.5.x.
	http.HandleFunc("/hosts", hostsStatusJSON)
	http.HandleFunc("/hosts/check", onlyPOST(hostsCheck))
	http.HandleFunc("/hosts/reload", onlyPOST(hostsReload))
	http.HandleFunc("/hosts/save", onlyPOST(hostsSave))
	http.HandleFunc("/hosts/enable", onlyPOST(hostsEnableCustom))
	http.HandleFunc("/hosts/disable", onlyPOST(hostsDisableCustom))

	// Порт поднимаем ПЕРВЫМ, статус добираем в фоне. Порядок важен: `doqd
	// status` резолвит имя и через doqd, и через :53, а `doqd list` пробует
	// каждый апстрим живым QUIC-запросом. Синхронный doRefresh() до
	// ListenAndServe держал порт 8091 неразвязанным до 6 секунд на мёртвых
	// апстримах — и в это время UI выглядел мёртвым.
	go refresher()

	fmt.Println("keenetic-doq UI → http://" + listenAddr)
	srv := &http.Server{
		Addr:              listenAddr,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      20 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

func page(w http.ResponseWriter, r *http.Request) {
	status, list, up := currentStatus()
	age := int(time.Since(up).Seconds())
	msg := r.URL.Query().Get("msg")
	if len(msg) > 4096 {
		msg = msg[:4096]
	}

	dot, title, label := "var(--red)", "stopped", "● stopped"
	if strings.Contains(status, "running") {
		dot, title, label = "var(--green)", "running", "● running"
	}
	// Возраст снимка в лейбле: пользователь видит, что UI не висит, а
	// показывает кэш не старше 30 секунд.
	label += fmt.Sprintf(" · %d с", age)

	msgHTML := ""
	if msg != "" {
		msgHTML = `<div class="msg">` + html.EscapeString(msg) + `</div>`
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// pageHTML — СТРОКА ФОРМАТА (в ней %[1]s, %[7]d, %[8]s и 50%%). Раньше
	// здесь стоял w.Write(pageHTML), который сливал в браузер сырой шаблон:
	// пользователь видел литералы "%[1]s" вместо статуса.
	if _, err := fmt.Fprintf(w, pageHTML,
		dot,
		title,
		label,
		msgHTML,
		html.EscapeString(status),
		html.EscapeString(list),
		age,
		html.EscapeString(customHostsPath),
	); err != nil {
		log.Printf("render: %v", err)
	}
}

// addFlags — белый список флагов doqd add. Без него через подменённую
// форму в exec можно было передать что угодно: exec.CommandContext не
// знает про шелл, но произвольные аргументы doqd всё равно принимает.
// --geo удалён из doqd вместе с гео-DNS (SmartDNS-переработка).
var addFlags = map[string]bool{
	"--fallback": true,
	"--force":    true,
}

func add(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.FormValue("url"))
	typ := strings.TrimSpace(r.FormValue("type"))
	host, err := domainASCII(raw)
	if err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка: "+err.Error()), 303)
		return
	}
	if typ != "" && !addFlags[typ] {
		http.Redirect(w, r, "/?msg="+encode("Ошибка: недопустимый тип"), 303)
		return
	}
	var out string
	if typ != "" {
		out = run("doqd", "add", typ, host)
	} else {
		out = run("doqd", "add", host)
	}
	// doqd add пишет конфиг и сам дёргает рестарт докера; мгновенный опрос
	// показал бы старое состояние.
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode(out), 303)
}

func remove(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.FormValue("id"))
	host, err := domainASCII(raw)
	if err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка: "+err.Error()), 303)
		return
	}
	out := run("doqd", "remove", host)
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode(out), 303)
}

func test(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.FormValue("url"))
	host, err := domainASCII(raw)
	if err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка: "+err.Error()), 303)
		return
	}
	out := run("doqd", "test", host)
	http.Redirect(w, r, "/?msg="+encode(out), 303)
}

// ---------- Hosts (подмена адресов) ----------
//
// doqd 0.6.x (SmartDNS) удалил geo/geo-domain: вместо них директива
// `hosts builtin` или `hosts /полный/путь/к/файлу` в конфиге. Файл
// полностью заменяет встроенный снимок; SIGHUP (doqd hosts reload)
// перечитывает его атомарно, ошибочный файл не применяется.

// doqdConfPath, customHostsPath и versionFile объявлены в начале файла и
// зависят от платформы (Keenetic — /opt, OpenWRT — /etc). doqdConfPath —
// переменная: тесты подменяют её на временный файл.

// hostsEditMax — потолок размера hosts-файла в редакторе. Встроенный снимок
// ~1025 имён (< 100 КБ); 256 КБ с запасом, и protects RAM/flash на роутере.
const hostsEditMax = 256 << 10

// hostsMode читает директиву hosts из конфига: ("builtin","") если встроенный
// список (или директивы нет — doqd по умолчанию использует builtin), либо
// ("file", путь) для своего файла.
func hostsMode() (string, string) {
	b, err := os.ReadFile(doqdConfPath)
	if err != nil {
		return "builtin", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "hosts" {
			if f[1] == "builtin" {
				return "builtin", ""
			}
			return "file", f[1]
		}
	}
	return "builtin", ""
}

// setHostsDirective заменяет строку hosts в doqd.conf. Оригинал сохраняется в
// .bak — миграцию гео-настроек апстрим делает свою копию, но здесь правка
// идёт из UI, и откат должен быть возможен без reinstall.
func setHostsDirective(value string) error {
	b, err := os.ReadFile(doqdConfPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	replaced := false
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") {
			continue
		}
		if f := strings.Fields(t); len(f) >= 1 && f[0] == "hosts" {
			lines[i] = "hosts " + value
			replaced = true
		}
	}
	if !replaced {
		// Конфиг без директивы: дописываем в конец. В Default() doqd
		// использует builtin, явная строка делает состояние видимым.
		lines = append(lines, "hosts "+value)
	}
	if err := os.WriteFile(doqdConfPath+".bak", b, 0o644); err != nil {
		return err
	}
	return os.WriteFile(doqdConfPath, []byte(strings.Join(lines, "\n")), 0o644)
}

// hostsStatusJSON — GET /hosts: режим, путь, вывод `doqd hosts status` и
// содержимое файла для редактора (в file-режиме).
func hostsStatusJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	mode, path := hostsMode()
	resp := map[string]any{
		"mode":   mode,
		"path":   path,
		"status": runWithTimeout("doqd", 10*time.Second, "hosts", "status"),
	}
	if mode == "file" && path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if len(b) > hostsEditMax {
				resp["content"] = string(b[:hostsEditMax]) + "\n... (обрезано)"
			} else {
				resp["content"] = string(b)
			}
		} else {
			resp["error"] = "не удалось прочитать " + path + ": " + err.Error()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func hostsCheck(w http.ResponseWriter, r *http.Request) {
	out := runWithTimeout("doqd", 15*time.Second, "hosts", "check")
	http.Redirect(w, r, "/?msg="+encode("hosts check:\n"+out), 303)
}

func hostsReload(w http.ResponseWriter, r *http.Request) {
	out := run("doqd", "hosts", "reload")
	http.Redirect(w, r, "/?msg="+encode("hosts reload:\n"+out), 303)
}

// hostsSave пишет редактор в файл из директивы hosts, проверяет его и только
// при успешной проверке даёт демону SIGHUP. Путь берётся строго из конфига —
// форма не может записать произвольный файл.
func hostsSave(w http.ResponseWriter, r *http.Request) {
	content := r.FormValue("content")
	if len(content) > hostsEditMax {
		http.Redirect(w, r, "/?msg="+encode("Ошибка: файл слишком большой (>256 КБ)"), 303)
		return
	}
	mode, path := hostsMode()
	if mode != "file" || path == "" {
		http.Redirect(w, r, "/?msg="+encode("Ошибка: сейчас используется встроенный список — сначала создайте свой файл"), 303)
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка записи "+path+": "+err.Error()), 303)
		return
	}
	// Ошибочный файл не должен уходить в демона: reload только после check.
	out := runWithTimeout("doqd", 15*time.Second, "hosts", "check")
	if strings.HasPrefix(out, "Ошибка:") {
		http.Redirect(w, r, "/?msg="+encode("Сохранено, но hosts check не прошёл — демон оставил старые правила:\n"+out), 303)
		return
	}
	reload := run("doqd", "hosts", "reload")
	http.Redirect(w, r, "/?msg="+encode("Hosts сохранены и применены:\n"+out+"\n"+reload), 303)
}

// hostsEnableCustom экспортирует встроенный снимок в customHostsPath и
// переключает конфиг на него. Смена директивы hosts требует рестарта:
// SIGHUP перечитывает файл, но сам источник фиксируется при старте.
func hostsEnableCustom(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(90 * time.Second))
	if runtime.GOOS == "windows" {
		http.Redirect(w, r, "/?msg="+encode("Ошибка: недоступно на ПК"), 303)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Пути задаёт платформа — шелл здесь только для редиректа вывода.
	// mkdir нужен на OpenWRT: каталог /etc/doqd апстрим создаёт только если
	// пользователь сам его завёл (см. README апстрима, «Свой hosts»).
	out, err := exec.CommandContext(ctx, "sh", "-c",
		"mkdir -p $(dirname "+customHostsPath+") && doqd hosts export > "+customHostsPath+".tmp && [ -s "+customHostsPath+".tmp ] && mv "+customHostsPath+".tmp "+customHostsPath).CombinedOutput()
	if err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка экспорта: "+stripANSI(string(out))+"\n"+err.Error()), 303)
		return
	}
	if err := setHostsDirective(customHostsPath); err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка конфига: "+err.Error()), 303)
		return
	}
	restartOut := controlOutput(run(doqdInitScript, "restart"))
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Свой hosts включён ("+customHostsPath+"), демон перезапущен:\n"+restartOut), 303)
}

// hostsDisableCustom возвращает конфиг на встроенный снимок.
func hostsDisableCustom(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(90 * time.Second))
	if err := setHostsDirective("builtin"); err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка конфига: "+err.Error()), 303)
		return
	}
	restartOut := controlOutput(run(doqdInitScript, "restart"))
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Включён встроенный hosts, демон перезапущен:\n"+restartOut), 303)
}

func restart(w http.ResponseWriter, r *http.Request) {
	out := controlOutput(run(doqdInitScript, "restart"))
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Restart:\n"+out), 303)
}

func stop(w http.ResponseWriter, r *http.Request) {
	out := controlOutput(run(doqdInitScript, "stop"))
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Stop:\n"+out), 303)
}

func start(w http.ResponseWriter, r *http.Request) {
	out := controlOutput(run(doqdInitScript, "start"))
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Start:\n"+out), 303)
}

func encode(s string) string {
	// URL-кодирует для передачи в ?msg= после 303 Redirect.
	// url.QueryEscape кодирует все спецсимволы (&=#+/ и т.д.) корректно.
	return url.QueryEscape(s)
}

// onlyPOST оборачивает mutационный хендлер и отвечает 405 на всё, что
// прилетело не POST. Без этого достаточно ссылки вида
// <a href="/install?key=..."> или <img src="/restart"> на посторонней
// странице, чтобы браузер от имени залогиненного админа перезапустил
// докер или скачал и выполнил скрипт с чужого сервера.
func onlyPOST(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

// domainASCII(v) проверяет, что v — валидный домен/URL, и возвращает
// канонический ASCII-хост. Отсекает: пустое значение, домены с
// конечной точкой (IDNA ToASCII("example.com.") → "example.com."),
// host с пробелами, URL без хоста. Это защищает от отправки doqd
// команд вроде "doqd add example.com." (бесполезно) и от инъекций
// через пробел/символы в аргумент exec.CommandContext.
//
// Если входные данные — полный URL (quic://...), схема сохраняется:
// doqd remove/add/test принимают адреса вида quic://host[:port],
// и без схемы он не находит апстрим в конфиге.
func domainASCII(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("пустое значение")
	}
	// Если это просто числовой ID (удаление по номеру) — разрешаем.
	if _, err := strconv.Atoi(v); err == nil {
		return v, nil
	}
	// Пробуем распарсить как URL.
	u, err := url.Parse(v)
	if err != nil {
		return "", fmt.Errorf("невалидный URL: %v", err)
	}
	// "example.com" без схемы — это host, не path.
	host := u.Host
	if host == "" && u.Scheme == "" {
		// Пользователь ввёл просто домен без схемы.
		host = v
	}
	if host == "" {
		return "", errors.New("URL не содержит хост")
	}
	// Запрещаем конечную точку — она ломает IDNA ToASCII.
	if strings.HasSuffix(host, ".") {
		return "", errors.New("домен не должен заканчиваться на точку")
	}
	// Если входные данные — полный URL, возвращаем канонический
	// scheme://host: doqd remove/add/test ищет апстрим в конфиге по
	// полному адресу, а без схемы он не находит ("upstream geohide.ru
	// not found in the config"). Хост берём из u.Host — разбор URL
	// отсекает пробелы и прочие символы, которые doqd не примет.
	if u.Scheme != "" {
		return u.Scheme + "://" + host, nil
	}
	return host, nil
}

// statusAPI — машинно-читаемый снимок для опроса из браузера. Страница
// рисуется на сервере ровно один раз, живые данные приходят отсюда.
// statusAPI — тот же снимок, что рендерит page(), но в JSON для опроса из
// браузера. Поля подобраны под элементы страницы (lbl/age/stpre/lpre), чтобы
// скрипт обновлял ровно то, что при первом рендере написал сервер.
func statusAPI(w http.ResponseWriter, r *http.Request) {
	status, list, up := currentStatus()
	age := int(time.Since(up).Seconds())
	label := "● stopped"
	if strings.Contains(status, "running") {
		label = "● running"
	}
	label += fmt.Sprintf(" · %d с", age)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":        strings.Contains(status, "running"),
		"label":     label,
		"age":       age,
		"status":    status,
		"list":      list,
		"installed": installed(),
	})
}

// installed — признаки установки keenetic-doq по файлам инсталлера. Пути
// платформенные: Keenetic — /opt, OpenWRT — /etc и /usr/sbin.
func installed() bool {
	for _, p := range []string{doqdBinary, doqdConfPath, doqdInitScript} {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

// runUpstreamInstaller скачивает и выполняет установщик doqd для текущей
// платформы: на Keenetic это install.sh апстрима, на OpenWRT —
// install-openwrt.sh. На Keenetic curl обязателен (busybox-wget без TLS), на
// OpenWRT штатный uclient-fetch умеет https, поэтому там допускается wget.
// Возвращает текст вывода со встроенной ошибкой, если что-то пошло не так.
func runUpstreamInstaller() string {
	script := "curl -fsSL " + doqdInstaller
	if _, err := exec.LookPath("curl"); err != nil {
		script = "wget -qO- " + doqdInstaller
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", script+" | sh").CombinedOutput()
	if len(out) > maxOutput {
		out = append(out[:maxOutput], []byte("\n... (обрезано)")...)
	}
	text := strings.TrimSpace(stripANSI(string(out)))
	if err != nil {
		if text != "" {
			text += "\n"
		}
		text += err.Error()
	}
	saveInstalledDoqdVersion()
	return text
}

// install ставит keenetic-doq одной командой. Отдельный exec, а не run():
// тут нужен именно шелл, потому что команда — пайп curl | sh.
func install(w http.ResponseWriter, r *http.Request) {
	// Серверный WriteTimeout = 20s, установка — до 3 минут. Без этого
	// редирект с результатом не дописался бы: дедлайн истёк бы во время
	// exec.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(4 * time.Minute))

	text := runUpstreamInstaller()
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Установка:\n"+text), 303)
}

// update обновляет keenetic-doq до последней версии. Повторный запуск
// install.sh ничего не ломает: он скачивает новый бинарник, сверяет SHA256
// и перезапускает демона. Существующий конфиг сохраняется.
func update(w http.ResponseWriter, r *http.Request) {
	// Серверный WriteTimeout = 20s, обновление — до 3 минут. Без этого
	// редирект с результатом не дописался бы: дедлайн истёк бы во время
	// exec.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(4 * time.Minute))

	text := runUpstreamInstaller()
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Обновление:\n"+text), 303)
}

// versionAPI возвращает установленную и последнюю версию doqd в JSON.
func versionAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	installed := installedDoqdVersion()
	latest := latestUpstream()
	json.NewEncoder(w).Encode(map[string]any{
		"installed": installed,
		"latest":    latest,
	})
}

// maxOutput ограничивает буфер вывода до 64 КБ, чтобы длинный вывод doqd
// не жрал RAM на роутере с 32 МБ.
const maxOutput = 64 << 10

const pageHTML = `
<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>keenetic-doq</title>
<style>
  :root {
    --bg: #0f1419;
    --surface: #1a2332;
    --surface2: #243044;
    --border: #2d3a4f;
    --text: #e6edf3;
    --text2: #8b9cb3;
    --accent: #00b4d8;
    --green: #22c55e;
    --red: #ef4444;
    --orange: #f59e0b;
    --blue: #3b82f6;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: Inter, system-ui, -apple-system, sans-serif;
    background: var(--bg);
    color: var(--text);
    min-height: 100vh;
  }

  /* Header */
  header {
    background: var(--surface);
    border-bottom: 1px solid var(--border);
    padding: 12px 24px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    position: sticky;
    top: 0;
    z-index: 100;
  }
  .logo {
    display: flex;
    align-items: center;
    gap: 10px;
    font-weight: 700;
    font-size: 1.15rem;
    letter-spacing: 0.5px;
  }
  .logo span.keen { color: #00b4d8; }
  .logo span.doq { color: #fff; }
  .status-dot {
    width: 10px; height: 10px; border-radius: 50%%;
    background: %[1]s;
    box-shadow: 0 0 8px %[1]s;
  }
  .header-actions { display: flex; gap: 8px; align-items: center; }
  .version-tag {
    font-size: 11px;
    color: var(--text2);
    background: var(--surface2);
    padding: 2px 8px;
    border-radius: 4px;
    margin-left: 4px;
    font-weight: 400;
  }
  .version-tag.ver-new {
    color: #fdba74;
    background: #451a03;
    border: 1px solid #9a3412;
    cursor: pointer;
  }

  /* Buttons */
  .btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 8px 14px;
    border-radius: 6px;
    border: 1px solid var(--border);
    background: var(--surface2);
    color: var(--text);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
    text-decoration: none;
    transition: all 0.15s;
  }
  .btn:hover { background: #2a3a52; border-color: #3d4f6a; }
  .btn-green { background: #14532d; border-color: #166534; color: #86efac; }
  .btn-green:hover { background: #166534; }
  .btn-red { background: #450a0a; border-color: #7f1d1d; color: #fca5a5; }
  .btn-red:hover { background: #7f1d1d; }
  .btn-orange { background: #451a03; border-color: #9a3412; color: #fdba74; }
  .btn-orange:hover { background: #9a3412; }
  .btn-blue { background: #1e3a5f; border-color: #1e40af; color: #93c5fd; }
  .btn-blue:hover { background: #1e40af; }
  .btn-primary {
    background: var(--accent);
    border-color: var(--accent);
    color: #001018;
    font-weight: 600;
  }
  .btn-primary:hover { background: #0096c7; }
  .btn-install { background: #14532d; border-color: #16a34a; color: #86efac; }
  .btn-install:hover { background: #16a34a; color: #f0fdf4; }

  .toast {
    position: fixed;
    right: 16px;
    bottom: 16px;
    max-width: 420px;
    padding: 12px 16px;
    border-radius: 8px;
    background: #1e293b;
    border: 1px solid #334155;
    color: #e2e8f0;
    font-size: 13px;
    white-space: pre-wrap;
    word-break: break-word;
    transition: opacity 0.4s;
    z-index: 200;
  }

  /* Main */
  main { max-width: 960px; margin: 0 auto; padding: 24px 16px 60px; }

  .msg {
    background: #0c2a1a;
    border: 1px solid #166534;
    color: #86efac;
    padding: 12px 16px;
    border-radius: 8px;
    margin-bottom: 20px;
    font-size: 13px;
    white-space: pre-wrap;
  }

  /* Cards */
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 10px;
    margin-bottom: 20px;
    overflow: hidden;
  }
  .card-header {
    padding: 14px 18px;
    border-bottom: 1px solid var(--border);
    display: flex;
    align-items: center;
    justify-content: space-between;
    font-weight: 600;
    font-size: 14px;
    color: var(--text2);
  }
  .card-body { padding: 16px 18px; }

  pre {
    background: #0d1117;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 14px;
    font-size: 12.5px;
    line-height: 1.5;
    overflow-x: auto;
    color: #c9d1d9;
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  }

  /* Forms */
  .form-row {
    display: flex;
    gap: 10px;
    margin-bottom: 12px;
    flex-wrap: wrap;
  }
  input[type=text] {
    flex: 1;
    min-width: 220px;
    padding: 10px 14px;
    background: #0d1117;
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text);
    font-size: 14px;
  }
  input[type=text]:focus {
    outline: none;
    border-color: var(--accent);
    box-shadow: 0 0 0 2px rgba(0,180,216,0.2);
  }
  select {
    flex: 1;
    min-width: 220px;
    padding: 10px 14px;
    background: #0d1117;
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text);
    font-size: 14px;
  }
  select:focus {
    outline: none;
    border-color: var(--accent);
    box-shadow: 0 0 0 2px rgba(0,180,216,0.2);
  }
  label {
    display: block;
    font-size: 12px;
    color: var(--text2);
    margin-bottom: 6px;
  }

  .grid-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
  }
  @media (max-width: 640px) {
    .grid-2 { grid-template-columns: 1fr; }
    header { padding: 10px 12px; }
    .header-actions { flex-wrap: wrap; }
  }
</style>
</head>
<body>

<header>
  <div class="logo">
    <span class="keen">KEENETIC</span>
    <span class="doq">DOQ</span>
    <div class="status-dot" id="dot" title="%[2]s"></div>
    <span class="version-tag" id="ver">—</span>
    <span class="version-tag" id="verl" style="display:none">latest</span>
  </div>
  <div class="header-actions">
    <form method="POST" action="/install" style="display:inline" id="installform">
      <button class="btn btn-install" id="installbtn" type="submit">⇩ Install</button>
    </form>
    <form method="POST" action="/update" style="display:inline" id="updateform">
      <button class="btn btn-blue" id="updatebtn" type="submit">↻ Update</button>
    </form>
    <form method="POST" action="/start" style="display:inline">
      <button class="btn btn-green" type="submit">▶ Start</button>
    </form>
    <form method="POST" action="/restart" style="display:inline">
      <button class="btn btn-orange" type="submit">↻ Restart</button>
    </form>
    <form method="POST" action="/stop" style="display:inline">
      <button class="btn btn-red" type="submit">■ Stop</button>
    </form>
  </div>
</header>

<main>
  %[4]s

  <!-- Status -->
  <div class="card">
    <div class="card-header">
      <span>Status</span>
      <span style="font-size:12px;color:%[1]s" id="lbl">
        %[3]s
      </span>
      <span style="font-size:11px;color:var(--text2);margin-left:8px" id="age">
        обновлено %[7]d с назад
      </span>
    </div>
    <div class="card-body">
      <pre id="stpre">%[5]s</pre>
    </div>
  </div>

  <!-- Upstreams -->
  <div class="card">
    <div class="card-header">Upstreams</div>
    <div class="card-body"><pre id="lpre">%[6]s</pre></div>
  </div>


  <!-- Actions -->
  <div class="grid-2">
    <div class="card">
      <div class="card-header">Добавить апстрим</div>
      <div class="card-body">
        <form method="POST" action="/add">
          <label>DoQ URL</label>
          <div class="form-row">
            <input type="text" name="url" placeholder="quic://dns.example.com" required>
          </div>
          <label>Тип</label>
          <div class="form-row">
            <select name="type" style="flex:1;min-width:220px;padding:10px 14px;background:#0d1117;border:1px solid var(--border);border-radius:6px;color:var(--text);font-size:14px;">
              <option value="">upstream</option>
              <option value="--fallback">fallback (резервный)</option>
              <option value="--force">force (пропустить проверку)</option>
            </select>
            <button class="btn btn-primary" type="submit">Add</button>
          </div>
        </form>
      </div>
    </div>

    <div class="card">
      <div class="card-header">Удалить апстрим</div>
      <div class="card-body">
        <form method="POST" action="/remove">
          <label>Номер или URL</label>
          <div class="form-row">
            <input type="text" name="id" placeholder="1  или  quic://..." required>
            <button class="btn btn-red" type="submit">Remove</button>
          </div>
        </form>
      </div>
    </div>
  </div>

  <div class="card">
    <div class="card-header">Проверить сервер</div>
    <div class="card-body">
      <form method="POST" action="/test">
        <label>DoQ URL</label>
        <div class="form-row">
          <input type="text" name="url" placeholder="quic://dns.quad9.net" required>
          <button class="btn btn-blue" type="submit">Test</button>
        </div>
      </form>
    </div>
  </div>

  <!-- Hosts (подмена адресов) -->
  <div class="card">
    <div class="card-header">
      <span>Hosts — подмена адресов</span>
      <span style="font-size:11px;color:var(--text2)" id="hostsMode"></span>
    </div>
    <div class="card-body">
      <pre id="hostspre">загрузка…</pre>
      <div class="form-row" style="margin-top:12px;flex-wrap:wrap;">
        <form method="POST" action="/hosts/check" style="display:inline;margin-right:8px;">
          <button class="btn btn-primary" type="submit">✔ Check</button>
        </form>
        <form method="POST" action="/hosts/reload" style="display:inline;margin-right:8px;">
          <button class="btn btn-blue" type="submit">⟳ Reload</button>
        </form>
        <form method="POST" action="/hosts/enable" style="display:inline;margin-right:8px;" id="hostsEnableForm">
          <button class="btn btn-green" type="submit">⇩ Создать свой файл из встроенного</button>
        </form>
        <form method="POST" action="/hosts/disable" style="display:inline;" id="hostsDisableForm">
          <button class="btn btn-orange" type="submit">↩ Встроенный список</button>
        </form>
      </div>
      <div id="hostsEditor" style="display:none;margin-top:14px;">
        <form method="POST" action="/hosts/save">
          <label>Файл <span id="hostsPath"></span> (полностью заменяет встроенный снимок)</label>
          <textarea name="content" id="hostsContent" spellcheck="false" style="width:100%%;height:240px;background:#0d1117;border:1px solid var(--border);border-radius:8px;color:#c9d1d9;font-size:12px;line-height:1.5;padding:12px;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;resize:vertical;"></textarea>
          <div class="form-row" style="margin-top:10px;">
            <button class="btn btn-primary" type="submit">💾 Сохранить и применить</button>
          </div>
        </form>
      </div>
      <p style="font-size:11px;color:var(--text2);margin-top:10px;">
        Формат: <code>IP домен1 домен2 …</code> — точные имена, комментарии через #.
        Reload — SIGHUP, перечитывает файл атомарно; ошибочный файл демон не применяет.
      </p>
    </div>
  </div>
</main>

<script>
(function () {
  // Hosts-карточка: режим, статус, редактор файла. Данные с /hosts (JSON).
  var pre = document.getElementById('hostspre');
  var modeEl = document.getElementById('hostsMode');
  var editor = document.getElementById('hostsEditor');
  var content = document.getElementById('hostsContent');
  var pathEl = document.getElementById('hostsPath');
  var enableF = document.getElementById('hostsEnableForm');
  var disableF = document.getElementById('hostsDisableForm');
  function loadHosts() {
    fetch('/hosts', {cache: 'no-store'}).then(function (r) {
      if (!r.ok) { throw new Error('http'); }
      return r.json();
    }).then(function (d) {
      if (modeEl) {
        modeEl.textContent = d.mode === 'file' ? 'свой файл: ' + d.path : 'встроенный снимок';
      }
      if (pre) { pre.textContent = d.status || 'нет данных'; }
      if (d.mode === 'file') {
        if (editor) { editor.style.display = ''; }
        if (enableF) { enableF.style.display = 'none'; }
        if (pathEl) { pathEl.textContent = d.path; }
        if (content && typeof d.content === 'string') { content.value = d.content; }
        if (d.error && pre) { pre.textContent += '\n' + d.error; }
      } else {
        if (editor) { editor.style.display = 'none'; }
        if (disableF) { disableF.style.display = 'none'; }
      }
    }).catch(function () {
      if (pre) { pre.textContent = 'Не удалось получить hosts-статус'; }
    });
  }
  loadHosts();
  if (enableF) {
    enableF.addEventListener('submit', function (e) {
      if (!window.confirm('Экспортировать встроенный снимок в %[8]s\nи переключить doqd на свой файл?\n\nДемон будет перезапущен.')) {
        e.preventDefault();
      }
    });
  }
  if (disableF) {
    disableF.addEventListener('submit', function (e) {
      if (!window.confirm('Вернуться на встроенный снимок hosts?\n\nДемон будет перезапущен.')) {
        e.preventDefault();
      }
    });
  }
})();
</script>

<script>
(function () {
  // id и поля JSON соответствуют серверу: dot, lbl, age, stpre, lpre и
  // /api/status -> {ok, label, age, status, list, installed}.
  var dot = document.getElementById('dot');
  var lbl = document.getElementById('lbl');
  var age = document.getElementById('age');
  var stp = document.getElementById('stpre');
  var lpr = document.getElementById('lpre');
  var ver = document.getElementById('ver');
  var verl = document.getElementById('verl');
  var form = document.getElementById('installform');
  var utype = document.getElementById('updateform');
  var timer = null;
  var GREEN = '#22c55e', RED = '#ef4444';

  // /install под капотом делает curl|sh с таймаутом 3 мин. Повторный submit
  // значил бы второй параллельный установщик поверх первого, поэтому форма
  // запирается на 60 с: этого хватит, чтобы успеть увидеть результат.
  if (form) {
    form.addEventListener('submit', function (e) {
      if (form.getAttribute('data-busy')) { e.preventDefault(); return; }
      if (!window.confirm('Установить keenetic-doq на это устройство?\n\nСкрипт скачается с raw.githubusercontent.com/necronicle/keenetic-doq и перезапустит сервис.')) {
        e.preventDefault();
        return;
      }
      form.setAttribute('data-busy', '1');
      var b = form.querySelector('button');
      if (b) {
        b.disabled = true;
        b.textContent = 'установка…';
      }
      setTimeout(function () {
        form.removeAttribute('data-busy');
        if (b) { b.disabled = false; b.textContent = 'Install'; }
      }, 60000);
    });
  }

  // /update тоже делает curl|sh, блокируем форму на 60 с.
  if (utype) {
    utype.addEventListener('submit', function (e) {
      if (utype.getAttribute('data-busy')) { e.preventDefault(); return; }
      if (!window.confirm('Обновить keenetic-doq до последней версии?\n\nСкрипт скачается и перезапустит сервис.')) {
        e.preventDefault();
        return;
      }
      utype.setAttribute('data-busy', '1');
      var b = utype.querySelector('button');
      if (b) {
        b.disabled = true;
        b.textContent = 'обновление…';
      }
      setTimeout(function () {
        utype.removeAttribute('data-busy');
        if (b) { b.disabled = false; b.textContent = 'Update'; }
      }, 60000);
    });
  }

  // Старт/рестарт/стоп отвечают HTML-ом только когда systemctl закончит.
  // До этого момента опрос не нужен: он форкает doqd ровно тогда, когда
  // роутер и так занят. Страница перезагрузится ответом — опрос возобновится
  // сам. У /install своя логика: там опрос оставляем, он показывает прогресс.
  function stopPoll() {
    if (timer) { clearTimeout(timer); timer = null; }
  }

  // Кнопка остаётся заблокированной до перезагрузки страницы, поэтому двойной
  // клик больше не значит два параллельных systemctl.
  var allForms = document.querySelectorAll('form');
  for (var i = 0; i < allForms.length; i++) {
    if (allForms[i] === form || allForms[i] === utype) { continue; }
    allForms[i].addEventListener('submit', function () {
      stopPoll();
      var b = this.querySelector('button');
      if (b) { b.disabled = true; }
    });
  }

  function apply(d) {

    var c = d.ok ? GREEN : RED;
    if (dot) {
      dot.style.background = c;
      dot.style.boxShadow = '0 0 8px ' + c;
      dot.title = d.ok ? 'running' : 'stopped';
    }
    if (lbl) { lbl.textContent = d.label; lbl.style.color = c; }
    if (age) { age.textContent = 'обновлено ' + d.age + ' с назад'; }
    if (stp && typeof d.status === 'string') { stp.textContent = d.status; }
    if (lpr && typeof d.list === 'string') { lpr.textContent = d.list; }
    if (form && d.installed) { form.style.display = 'none'; }
    if (utype && d.installed) { utype.style.display = 'none'; }
  }

  function poll() {
    fetch('/api/status', {cache: 'no-store'}).then(function (r) {
      if (!r.ok) { throw new Error('http'); }
      return r.json();
    }).then(function (d) {
      apply(d);
      timer = setTimeout(poll, 5000);
    }).catch(function () {
      if (lbl) { lbl.textContent = '● нет связи с doq-web'; lbl.style.color = '#f59e0b'; }
      timer = setTimeout(poll, 5000);
    });
  }

  // Первый рендер делает сервер, поэтому первый опрос отложен на целый
  // интервал: иначе каждая загрузка страницы значит два форка doqd.
  timer = setTimeout(poll, 5000);

  // Версия загружается один раз при загрузке страницы.
  function loadVersion() {
    fetch('/api/version', {cache: 'no-store'}).then(function (r) {
      if (!r.ok) { throw new Error('http'); }
      return r.json();
    }).then(function (d) {
      if (ver) {
        if (d.installed) {
          ver.textContent = d.installed;
          ver.title = 'установлено';
        } else {
          ver.textContent = '?';
          ver.title = 'не определено';
        }
        if (d.latest) {
          if (verl) {
            if (d.installed && d.installed === d.latest) {
              verl.textContent = '●';
              verl.style.color = '#22c55e';
            } else {
              verl.textContent = '↻ ' + d.latest;
              verl.className = 'version-tag ver-new';
              verl.title = 'доступно обновление — нажмите Update';
            }
            verl.style.display = '';
          }
        }
      }
    }).catch(function () {
      if (ver) { ver.textContent = '?'; ver.title = 'нет связи'; }
    });
  }
  loadVersion();

  // Скрытая вкладка роутер не нагружает.
  document.addEventListener('visibilitychange', function () {
    if (document.hidden) {
      if (timer) { clearTimeout(timer); timer = null; }
    } else if (!timer) {
      poll();
    }
  });
})();
</script>
</body>
</html>
`
