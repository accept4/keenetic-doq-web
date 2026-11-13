package main

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"
	"time"
)

const pageHTML = `
<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>keenetic-doq</title>
<script>
try {
  if (localStorage.getItem('doq-web-theme') === 'light') {
    document.documentElement.dataset.theme = 'light';
  }
} catch (e) {}
</script>
<style>
  :root {
    color-scheme: dark;
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
    --control-bg: #0d1117;
    --control-text: #c9d1d9;
    --pre-bg: #0d1117;
    --pre-text: #c9d1d9;
  }
  [data-theme="light"] {
    color-scheme: light;
    --bg: #f3f6fa;
    --surface: #ffffff;
    --surface2: #e8eef5;
    --border: #cbd5e1;
    --text: #1e293b;
    --text2: #526174;
    --accent: #007f9e;
    --control-bg: #ffffff;
    --control-text: #1e293b;
    --pre-bg: #f1f5f9;
    --pre-text: #334155;
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
  [data-theme="light"] .logo span.keen { color: #007f9e; }
  [data-theme="light"] .logo span.doq { color: #1e293b; }
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
  .ver-sep {
    opacity: .45;
    font-size: 11px;
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
  [data-theme="light"] .btn:hover { background: #dce5ef; border-color: #94a3b8; }
  [data-theme="light"] .btn-green { background: #dcfce7; border-color: #86efac; color: #166534; }
  [data-theme="light"] .btn-green:hover { background: #bbf7d0; }
  [data-theme="light"] .btn-red { background: #fee2e2; border-color: #fca5a5; color: #991b1b; }
  [data-theme="light"] .btn-red:hover { background: #fecaca; }
  [data-theme="light"] .btn-orange { background: #ffedd5; border-color: #fdba74; color: #9a3412; }
  [data-theme="light"] .btn-orange:hover { background: #fed7aa; }
  [data-theme="light"] .btn-blue { background: #dbeafe; border-color: #93c5fd; color: #1e40af; }
  [data-theme="light"] .btn-blue:hover { background: #bfdbfe; }
  [data-theme="light"] .btn-install { background: #dcfce7; border-color: #86efac; color: #166534; }
  [data-theme="light"] .btn-install:hover { background: #bbf7d0; color: #14532d; }
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
  [data-theme="light"] .btn-primary { color: #ffffff; }
  [data-theme="light"] .btn-primary:hover { background: #006b84; }
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
  [data-theme="light"] .toast {
    background: #ffffff;
    border-color: #cbd5e1;
    color: #1e293b;
    box-shadow: 0 8px 24px rgba(15, 23, 42, 0.12);
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
  [data-theme="light"] .msg {
    background: #f0fdf4;
    border-color: #86efac;
    color: #166534;
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
    background: var(--pre-bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 14px;
    font-size: 12.5px;
    line-height: 1.5;
    overflow-x: auto;
    color: var(--pre-text);
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
    background: var(--control-bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--control-text);
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
    background: var(--control-bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--control-text);
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
  textarea {
    background: var(--control-bg) !important;
    color: var(--control-text) !important;
  }
  [data-theme="light"] select {
    background: var(--control-bg) !important;
    color: var(--control-text) !important;
  }
  [data-theme="light"] .version-tag.ver-new {
    color: #9a3412;
    background: #ffedd5;
    border-color: #fdba74;
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
    <span class="version-tag" id="ver" title="doqd">—</span>
    <span class="version-tag" id="verl" style="display:none">latest</span>
    <span class="ver-sep" aria-hidden="true">·</span>
    <span class="version-tag" id="webv" title="панель">—</span>
    <span class="version-tag" id="webvl" style="display:none"></span>
  </div>
  <div class="header-actions">
    <button class="btn" id="themeToggle" type="button" aria-pressed="false">☀ Светлая тема</button>
    <form method="POST" action="/webupdate" style="display:inline" id="webupdateform">
      <button class="btn btn-install" id="webupdatebtn" type="submit">⇧ Панель</button>
    </form>
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
  var webv = document.getElementById('webv');
  var webvl = document.getElementById('webvl');
  var wform = document.getElementById('webupdateform');
  var form = document.getElementById('installform');
  var utype = document.getElementById('updateform');
  var timer = null;
  var GREEN = '#22c55e', RED = '#ef4444';

  // /install скачивает и проверяет скрипт, затем запускает его с таймаутом 3 мин.
  // Повторный submit мог бы запустить второй установщик поверх первого,
  // поэтому форма запирается на 60 с.
  if (form) {
    form.addEventListener('submit', function (e) {
      if (form.getAttribute('data-busy')) { e.preventDefault(); return; }
      if (!window.confirm('Установить keenetic-doq на это устройство?\n\nСкрипт будет загружен из GitHub, проверен и запустит/перезапустит сервис.')) {
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

  // /webupdate перезапускает саму панель (process замёрзнет на несколько секунд).
  if (wform) {
    wform.addEventListener('submit', function (e) {
      if (wform.getAttribute('data-busy')) { e.preventDefault(); return; }
      if (!window.confirm('Обновить панель keenetic-doq-web до последней версии?\n\nСкрипт скачается из GitHub и перезапустит веб-панель; интерфейс станет недоступен на 20–40 с.')) {
        e.preventDefault();
        return;
      }
      wform.setAttribute('data-busy', '1');
      var b = wform.querySelector('button');
      if (b) {
        b.disabled = true;
        b.textContent = 'обновление…';
      }
      setTimeout(function () {
        wform.removeAttribute('data-busy');
        if (b) { b.disabled = false; b.textContent = '⇧ Панель'; }
      }, 60000);
    });
  }

  // /update тоже запускает проверенный скрипт; блокируем форму на 60 с.
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
      if (webv) {
        webv.textContent = d.webCurrent || '?';
        webv.title = 'панель keenetic-doq-web';
        if (webvl) {
          if (d.webLatest) {
            if (d.webUpToDate) {
              webvl.textContent = '●';
              webvl.style.color = '#22c55e';
              webvl.title = 'панель актуальна';
              webvl.style.display = '';
            } else {
              webvl.textContent = '↻ ' + d.webLatest;
              webvl.className = 'version-tag ver-new';
              webvl.title = 'доступно обновление панели — нажмите «Панель»';
              webvl.style.display = '';
            }
          } else {
            webvl.style.display = 'none';
          }
        }
        // Кнопка обновления панели нужна только когда есть новая версия или
        // установка уже идёт; иначе прячем, чтобы не провоцировать перезапуск.
        if (wform) {
          wform.style.display = (d.webUpdateBusy || (d.webLatest && !d.webUpToDate)) ? 'inline' : 'none';
        }
        if (d.webUpdateBusy) {
          var wb = wform ? wform.querySelector('button') : null;
          if (wb) { wb.disabled = true; wb.textContent = 'обновление…'; }
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
<script>
(function () {
  var button = document.getElementById('themeToggle');
  if (!button) { return; }
  function applyTheme(theme) {
    if (theme === 'light') {
      document.documentElement.dataset.theme = 'light';
      button.textContent = '☾ Тёмная тема';
      button.setAttribute('aria-pressed', 'true');
    } else {
      document.documentElement.removeAttribute('data-theme');
      button.textContent = '☀ Светлая тема';
      button.setAttribute('aria-pressed', 'false');
    }
  }
  applyTheme(document.documentElement.dataset.theme);
  button.addEventListener('click', function () {
    var theme = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
    try { localStorage.setItem('doq-web-theme', theme); } catch (e) {}
    applyTheme(theme);
  });
})();
</script>
</body>
</html>
`

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
	label += fmt.Sprintf(" · %d с", age)

	msgHTML := ""
	if msg != "" {
		msgHTML = `<div class="msg">` + html.EscapeString(msg) + `</div>`
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
