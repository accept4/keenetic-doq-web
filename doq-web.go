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

// doRefresh — единственное место, где форкается doqd. Вызывается из одной
// горутины, одновременные пары status+list исключены по построению.
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

func run(name string, args ...string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("Ошибка: %s недоступен (на ПК нет doqd, это нормально)", name)
	}
	if err := commandAvailable(name); err != nil {
		return fmt.Sprintf("Ошибка: %s недоступен (%v)", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if len(out) > maxOutput {
		out = append(out[:maxOutput], []byte("\n... (обрезано)")...)
	}
	if err != nil {
		return fmt.Sprintf("Ошибка: %v\n%s", err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "--probe" {
			os.Exit(runProbe())
		}
	}

	http.HandleFunc("/", page)
	http.HandleFunc("/api/status", statusAPI)
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
	// pageHTML — СТРОКА ФОРМАТА (в ней %[1]s, %[7]d и 50%%). Раньше здесь
	// стоял w.Write(pageHTML), который сливал в браузер сырой шаблон:
	// пользователь видел литералы "%[1]s" вместо статуса.
	if _, err := fmt.Fprintf(w, pageHTML,
		dot,
		title,
		label,
		msgHTML,
		html.EscapeString(status),
		html.EscapeString(list),
		age,
	); err != nil {
		log.Printf("render: %v", err)
	}
}

func add(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.FormValue("url"))
	host, err := domainASCII(raw)
	if err != nil {
		http.Redirect(w, r, "/?msg="+encode("Ошибка: "+err.Error()), 303)
		return
	}
	out := run("doqd", "add", host)
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

func restart(w http.ResponseWriter, r *http.Request) {
	out := run("/opt/etc/init.d/S56doqd", "restart")
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Restart:\n"+out), 303)
}

func stop(w http.ResponseWriter, r *http.Request) {
	out := run("/opt/etc/init.d/S56doqd", "stop")
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Stop:\n"+out), 303)
}

func start(w http.ResponseWriter, r *http.Request) {
	out := run("/opt/etc/init.d/S56doqd", "start")
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

// installed — признаки установки keenetic-doq по файлам инсталлера.
func installed() bool {
	for _, p := range []string{"/opt/sbin/doqd", "/opt/etc/doqd.conf", "/opt/etc/init.d/S56doqd"} {
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

// install ставит keenetic-doq одной командой. Отдельный exec, а не run():
// тут нужен именно шелл, потому что команда — пайп curl | sh.
func install(w http.ResponseWriter, r *http.Request) {
	// Серверный WriteTimeout = 20s, установка — до 3 минут. Без этого
	// редирект с результатом не дописался бы: дедлайн истёк бы во время
	// exec.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(4 * time.Minute))

	const src = "https://raw.githubusercontent.com/necronicle/keenetic-doq/main/install.sh"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", "curl -fsSL "+src+" | sh").CombinedOutput()
	if len(out) > maxOutput {
		out = append(out[:maxOutput], []byte("\n... (обрезано)")...)
	}
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			text += "\n"
		}
		text += err.Error()
	}
	requestRefreshAfter(refreshAfterRestart)
	http.Redirect(w, r, "/?msg="+encode("Установка:\n"+text), 303)
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
  </div>
  <div class="header-actions">
    <form method="POST" action="/install" style="display:inline" id="installform">
      <button class="btn btn-install" id="installbtn" type="submit">⇩ Install</button>
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
</main>

<script>
(function () {
  // id и поля JSON соответствуют серверу: dot, lbl, age, stpre, lpre и
  // /api/status -> {ok, label, age, status, list, installed}.
  var dot = document.getElementById('dot');
  var lbl = document.getElementById('lbl');
  var age = document.getElementById('age');
  var stp = document.getElementById('stpre');
  var lpr = document.getElementById('lpre');
  var form = document.getElementById('installform');
  var timer = null;
  var GREEN = '#22c55e', RED = '#ef4444';

  // /install под капотом делает curl|sh с таймаутом 3 мин. Повторный submit
  // значил бы второй параллельный установщик поверх первого, поэтому форма
  // запирается на 60 с: этого хватает, чтобы успеть увидеть результат.
  if (form) {
    form.addEventListener('submit', function (e) {
      if (form.getAttribute('data-busy')) { e.preventDefault(); return; }
      if (!window.confirm('Установить keenetic-doq на это устройство?\n\nСкрипт скажется с raw.githubusercontent.com/necronicle/keenetic-doq и перезапустит сервис.')) {
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
    if (allForms[i] === form) { continue; }
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
