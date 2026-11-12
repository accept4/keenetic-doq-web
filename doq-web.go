package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
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
	doqdInstaller   = "https://api.github.com/repos/necronicle/keenetic-doq/contents/install.sh?ref=main"
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
		doqdInstaller = "https://api.github.com/repos/necronicle/keenetic-doq/contents/install.sh?ref=main"
		versionFile = "/opt/etc/doq-web/version.txt"
		customHostsPath = "/opt/etc/doqd.hosts"
		return
	}
	doqdConfPath = "/etc/doqd.conf"
	doqdInitScript = "/etc/init.d/doqd"
	doqdBinary = "/usr/sbin/doqd"
	doqdInstaller = "https://api.github.com/repos/necronicle/keenetic-doq/contents/install-openwrt.sh?ref=main"
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

// saveInstalledDoqdVersion writes the upstream release tag after a successful install.
func saveInstalledDoqdVersion() error {
	v := doqdVersion()
	if v == "" {
		v = latestUpstream()
	}
	if v == "" {
		return errors.New("не удалось определить установленную версию")
	}
	if err := os.MkdirAll(filepath.Dir(versionFile), 0o755); err != nil {
		return fmt.Errorf("создание каталога версии: %w", err)
	}
	if err := os.WriteFile(versionFile, []byte(v+"\n"), 0o644); err != nil {
		return fmt.Errorf("сохранение версии: %w", err)
	}
	return nil
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

// daemonRunning определяет, жив ли демон, по выводу `doqd status`.
// Искать подстроку "running" нельзя: остановленный демон печатает
// «daemon:          not running», где "running" присутствует — панель
// показывала бы зелёный ● running при мёртвом doqd. Живой процесс всегда
// отвечает «daemon: running (pid N[, uptime ...])».
func daemonRunning(status string) bool {
	for _, line := range strings.Split(status, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "daemon:"); ok {
			return strings.HasPrefix(strings.TrimSpace(rest), "running")
		}
	}
	// Формат вывода до 0.6.x и строка из `doqd list`
	// («listen: ...   daemon: running (pid 123)») явного поля не имеют.
	return strings.Contains(status, "running (pid")
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

	http.HandleFunc("/", onlyGET(page))
	http.HandleFunc("/api/status", onlyGET(statusAPI))
	http.HandleFunc("/api/version", onlyGET(versionAPI))
	// Мутирующие маршруты — только POST. Ссылка вида /install?id= или
	// /stop, попавшая в превью мессенджера/антивирусного сканера или
	// prefetch браузера, иначе выполняется сама по себе.
	http.HandleFunc("/add", onlyPOST(add))
	http.HandleFunc("/remove", onlyPOST(remove))
	http.HandleFunc("/test", onlyPOST(test))
	http.HandleFunc("/restart", onlyPOST(restart))
	http.HandleFunc("/stop", onlyPOST(stop))
	http.HandleFunc("/start", onlyPOST(start))
	http.HandleFunc("/install", onlyPOST(install))
	http.HandleFunc("/update", onlyPOST(update))
	// Hosts (подмена адресов) — замена geo-domain из doqd <=0.5.x.
	http.HandleFunc("/hosts", onlyGET(hostsStatusJSON))
	http.HandleFunc("/hosts/check", onlyPOST(hostsCheck))
	http.HandleFunc("/hosts/reload", onlyPOST(hostsReload))
	http.HandleFunc("/hosts/save", onlyPOST(hostsSave))
	http.HandleFunc("/hosts/enable", onlyPOST(hostsEnableCustom))
	http.HandleFunc("/hosts/disable", onlyPOST(hostsDisableCustom))
	// Bootstrap — обычные DNS-серверы для резолва имён DoQ-апстримов.
	http.HandleFunc("/bootstrap", onlyGET(bootstrapStatusJSON))
	http.HandleFunc("/bootstrap/add", onlyPOST(bootstrapAdd))
	http.HandleFunc("/bootstrap/remove", onlyPOST(bootstrapRemove))
	http.HandleFunc("/bootstrap/reset", onlyPOST(bootstrapReset))
	http.HandleFunc("/bootstrap/save", onlyPOST(bootstrapSave))

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

// hostsSummary — человекочитаемое число правил из `doqd hosts status`
// («405 имён, 234 маски») или пустая строка, если вывод не распознан
// (другая версия doqd, ошибка).
//
// 0.6.4+: hosts (configured): builtin, 405 names, 234 wildcard masks (not live)
// 0.6.0-0.6.3: hosts (configured): builtin, 1025 names (not live)
var hostsNamesRe = regexp.MustCompile(`(\d+)\s+names`)
var hostsMasksRe = regexp.MustCompile(`(\d+)\s+wildcard masks`)

func hostsSummary(status string) string {
	n1 := firstInt(hostsNamesRe, status)
	n2 := firstInt(hostsMasksRe, status)
	if n1 == 0 {
		return ""
	}
	s := ruPlural(n1, "имя", "имени", "имён")
	if n2 > 0 {
		s += ", " + ruPlural(n2, "маска", "маски", "масок")
	}
	return s
}

func firstInt(re *regexp.Regexp, s string) int {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// ruPlural выбирает форму существительного по числу: 1 имя, 2 имени, 5 имён.
// Возвращает строку с числом: «1 имя», «2 имени», «5 имён».
func ruPlural(n int, one, few, many string) string {
	var form string
	switch {
	case n%100 >= 11 && n%100 <= 14:
		form = many
	default:
		switch n % 10 {
		case 1:
			form = one
		case 2, 3, 4:
			form = few
		default:
			form = many
		}
	}
	return fmt.Sprintf("%d %s", n, form)
}

// normalizeVer снимает префикс «v» из тега GitHub.
// `doqd -version` печатает «0.6.5», а GitHub-тег — «v0.6.5».
// Без нормализации панель вечно показывала бы «доступно обновление».
func normalizeVer(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "v")
}

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
	status := runWithTimeout("doqd", 10*time.Second, "hosts", "status")
	resp := map[string]any{
		"mode":   mode,
		"path":   path,
		"status": status,
	}
	if s := hostsSummary(status); s != "" {
		resp["summary"] = s
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
	out, err := saveHostsFile(path, []byte(content), func() string {
		return runWithTimeout("doqd", 15*time.Second, "hosts", "check")
	})
	if err != nil {
		message := "Ошибка: hosts не применены: " + err.Error()
		if out != "" {
			message += "\n" + out
		}
		http.Redirect(w, r, "/?msg="+encode(message), 303)
		return
	}
	reload := run("doqd", "hosts", "reload")
	http.Redirect(w, r, "/?msg="+encode("Hosts сохранены и применены:\n"+out+"\n"+reload), 303)
}

var hostsSaveMu sync.Mutex

// saveHostsFile validates a candidate in place and restores the previous file
// (or removes a newly created one) if doqd rejects it.
func saveHostsFile(path string, content []byte, check func() string) (string, error) {
	hostsSaveMu.Lock()
	defer hostsSaveMu.Unlock()

	original, err := os.ReadFile(path)
	hadOriginal := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("чтение текущего hosts: %w", err)
	}

	mode := os.FileMode(0o644)
	if hadOriginal {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", fmt.Errorf("получение прав текущего hosts: %w", statErr)
		}
		mode = info.Mode().Perm()
	}
	if err := atomicWriteFile(path, content, mode); err != nil {
		return "", fmt.Errorf("запись hosts: %w", err)
	}

	out := check()
	if !strings.HasPrefix(out, "Ошибка:") {
		return out, nil
	}

	var restoreErr error
	if hadOriginal {
		restoreErr = atomicWriteFile(path, original, mode)
	} else {
		restoreErr = os.Remove(path)
		if errors.Is(restoreErr, os.ErrNotExist) {
			restoreErr = nil
		}
	}
	if restoreErr != nil {
		return out, fmt.Errorf("проверка hosts не прошла; не удалось восстановить исходный файл: %w", restoreErr)
	}
	return out, errors.New("проверка hosts не прошла; исходный файл восстановлен")
}

func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	if runtime.GOOS == "windows" {
		return os.WriteFile(path, content, mode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
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

// ---------- Bootstrap ----------
//
// doqd.conf хранит bootstrap-адреса в одной директиве:
// bootstrap 77.88.8.8:53 8.8.8.8:53 1.1.1.1:53
// Имена запрещены (только IP-литералы, чтобы не резолвить их через сам doqd).
// Отсутствие директивы — доqd испол��зует встроенные DefaultBootstrapServers.

// defaultBootstrapServers — копия upstream.DefaultBootstrapServers (doqd 0.6.x).
var defaultBootstrapServers = []string{"77.88.8.8:53", "77.88.8.8:1253", "8.8.8.8:53", "1.1.1.1:53"}

// bootstrapAddr — канонический IP:port из значения директивы. Порт по умолчанию 53.
func bootstrapAddr(v string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(v))
	if err != nil {
		host, port = strings.TrimSpace(v), "53"
	}
	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("bootstrap должен быть IP-адресом, а не именем: %q", v)
	}
	return net.JoinHostPort(host, port), nil
}

// bootstrapMode читает директиву bootstrap из конфига. Возвращает ("custom",
// список) если директива есть, ("default", дефолты) — если её нет (doqd
// берёт встроенные адреса).
func bootstrapMode() (string, []string) {
	b, err := os.ReadFile(doqdConfPath)
	if err != nil {
		return "default", defaultBootstrapServers
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "bootstrap" {
			var addrs []string
			for _, v := range f[1:] {
				if a, err := bootstrapAddr(v); err == nil {
					addrs = append(addrs, a)
				}
			}
			if len(addrs) > 0 {
				return "custom", addrs
			}
		}
	}
	return "default", defaultBootstrapServers
}

// setBootstrapDirectives заменяет директиву bootstrap в конфиге. Пустой список
// удаляет её — doqd вернётся на встроенные адреса. Конфиг сохраняется в .bak.
func setBootstrapDirectives(addrs []string) error {
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
		if f := strings.Fields(t); len(f) >= 1 && f[0] == "bootstrap" {
			if len(addrs) == 0 {
				lines = append(lines[:i], lines[i+1:]...)
			} else {
				lines[i] = "bootstrap " + strings.Join(addrs, " ")
			}
			replaced = true
			break
		}
	}
	if !replaced && len(addrs) > 0 {
		lines = append(lines, "bootstrap "+strings.Join(addrs, " "))
	}
	if err := os.WriteFile(doqdConfPath+".bak", b, 0o644); err != nil {
		return err
	}
	return os.WriteFile(doqdConfPath, []byte(strings.Join(lines, "\n")), 0o644)
}

func bootstrapStatusJSON(w http.ResponseWriter, r *http.Request) {
	mode, addrs := bootstrapMode()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"mode":    mode,
		"addrs":   addrs,
		"default": defaultBootstrapServers,
	})
}

func bootstrapAdd(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.FormValue("addr"))
	addr, err := bootstrapAddr(raw)
	if err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка: "+err.Error()), 303)
		return
	}
	_, current := bootstrapMode()
	for _, a := range current {
		if a == addr {
			http.Redirect(w, r, "/?msg="+encode("Адрес "+addr+" уже в списке"), 303)
			return
		}
	}
	if err := setBootstrapDirectives(append(current, addr)); err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка конфига: "+err.Error()), 303)
		return
	}
	restartOut := controlOutput(run(doqdInitScript, "restart"))
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Bootstrap "+addr+" добавлен, демон перезапущен:\n"+restartOut), 303)
}

func bootstrapRemove(w http.ResponseWriter, r *http.Request) {
	addr, err := bootstrapAddr(strings.TrimSpace(r.FormValue("addr")))
	if err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка: "+err.Error()), 303)
		return
	}
	mode, current := bootstrapMode()
	if mode != "custom" {
		http.Redirect(w, r, "/?msg="+encode("Нельзя удалить: сейчас используются встроенные адреса doqd"), 303)
		return
	}
	var next []string
	removed := false
	for _, a := range current {
		if a == addr && !removed {
			removed = true
			continue
		}
		next = append(next, a)
	}
	if !removed {
		http.Redirect(w, r, "/?msg="+encode("Адрес "+addr+" не найден"), 303)
		return
	}
	if err := setBootstrapDirectives(next); err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка конфига: "+err.Error()), 303)
		return
	}
	restartOut := controlOutput(run(doqdInitScript, "restart"))
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Bootstrap "+addr+" удалён, демон перезапущен:\n"+restartOut), 303)
}

func bootstrapSave(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.FormValue("raw"))
	if raw == "" {
		if err := setBootstrapDirectives(nil); err != nil {
			http.Redirect(w, r, "/?msg="+encode("Ошибка конфига: "+err.Error()), 303)
			return
		}
		restartOut := controlOutput(run(doqdInitScript, "restart"))
		requestRefreshAfter(refreshAfterRestart)
		http.Redirect(w, r, "/?msg="+encode("Bootstrap очищен, демон перезапущен:\n"+restartOut), 303)
		return
	}

	parts := strings.Fields(raw)
	seen := make(map[string]struct{}, len(parts))
	next := make([]string, 0, len(parts))
	for _, field := range parts {
		addr, err := bootstrapAddr(field)
		if err != nil {
			http.Redirect(w, r, "/?msg="+encode("Ошибка: "+err.Error()), 303)
			return
		}
		if _, ok := seen[addr]; ok {
			http.Redirect(w, r, "/?msg="+encode("Ошибка: повторяющийся адрес "+addr), 303)
			return
		}
		seen[addr] = struct{}{}
		next = append(next, addr)
	}
	if err := setBootstrapDirectives(next); err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка конфига: "+err.Error()), 303)
		return
	}
	restartOut := controlOutput(run(doqdInitScript, "restart"))
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Bootstrap сохранён, демон перезапущен:\n"+restartOut), 303)
}

func bootstrapReset(w http.ResponseWriter, r *http.Request) {
	mode, _ := bootstrapMode()
	if mode != "custom" {
		http.Redirect(w, r, "/?msg="+encode("Уже используются встроенные адреса doqd"), 303)
		return
	}
	if err := setBootstrapDirectives(nil); err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка конфига: "+err.Error()), 303)
		return
	}
	restartOut := controlOutput(run(doqdInitScript, "restart"))
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Bootstrap сброшен на встроенные адреса, демон перезапущен:\n"+restartOut), 303)
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

// onlyPOST оборачивает мутационный хендлер и отвечает 405 на всё, что
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

func onlyGET(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
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
	running := daemonRunning(status)
	label := "● stopped"
	if running {
		label = "● running"
	}
	label += fmt.Sprintf(" · %d с", age)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":        running,
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

// runUpstreamInstaller загружает установщик doqd для текущей платформы через
// GitHub Contents API, проверяет Git blob SHA и только затем запускает его.
func runUpstreamInstaller() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	script, err := downloadVerifiedInstaller(ctx, doqdInstaller)
	if err != nil {
		return "Ошибка загрузки установщика: " + err.Error()
	}

	cmd := exec.CommandContext(ctx, "sh")
	cmd.Stdin = bytes.NewReader(script)
	var output cappedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	err = cmd.Run()
	text := strings.TrimSpace(stripANSI(output.String()))
	if err != nil {
		if text != "" {
			text += "\n"
		}
		text += err.Error()
		return text
	}

	doqdVerMu.Lock()
	doqdVer, doqdVerFetched = "", time.Time{}
	doqdVerMu.Unlock()
	if err := saveInstalledDoqdVersion(); err != nil {
		if text != "" {
			text += "\n"
		}
		text += "Предупреждение: установка прошла, но версию сохранить не удалось: " + err.Error()
	}
	return text
}

type installerContents struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	SHA      string `json:"sha"`
}

func downloadVerifiedInstaller(ctx context.Context, address string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("создание запроса: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "keenetic-doq-web")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub вернул HTTP %d", resp.StatusCode)
	}

	const maxInstallerResponse = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInstallerResponse+1))
	if err != nil {
		return nil, fmt.Errorf("чтение ответа: %w", err)
	}
	if len(body) > maxInstallerResponse {
		return nil, errors.New("ответ установщика превышает 1 МБ")
	}
	var result installerContents
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("разбор ответа GitHub: %w", err)
	}
	if result.Encoding != "base64" || result.Content == "" || result.SHA == "" {
		return nil, errors.New("ответ GitHub не содержит установщик и его SHA")
	}
	script, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(result.Content), ""))
	if err != nil {
		return nil, fmt.Errorf("декодирование установщика: %w", err)
	}
	if len(script) == 0 {
		return nil, errors.New("пустой установщик")
	}
	blob := fmt.Sprintf("blob %d\x00", len(script))
	sum := sha1.Sum(append([]byte(blob), script...))
	if !strings.EqualFold(fmt.Sprintf("%x", sum), result.SHA) {
		return nil, errors.New("SHA установщика не совпадает с GitHub")
	}
	return script, nil
}

// install устанавливает keenetic-doq через скачанный и проверенный скрипт.
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
	ni, nl := normalizeVer(installed), normalizeVer(latest)
	json.NewEncoder(w).Encode(map[string]any{
		"installed": installed,
		"latest":    latest,
		"upToDate":  ni != "" && ni == nl,
	})
}

// maxOutput ограничивает буфер вывода до 64 КБ, чтобы длинный вывод doqd
// не жрал RAM на роутере с 32 МБ.
const maxOutput = 64 << 10

