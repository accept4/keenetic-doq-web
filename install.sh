#!/bin/sh
set -e

REPO="accept4/keenetic-doq-web"
BIN_DIR="/opt/sbin"
INIT_DIR="/opt/etc/init.d"
NAME="doq-web"

echo "=== keenetic-doq-web installer ==="

# 1. Проверяем наличие Entware
if [ ! -d "/opt/bin" ] && [ ! -d "/opt/sbin" ]; then
    echo "Ошибка: Среда Entware не обнаружена в /opt!"
    echo "Установите Entware на роутер перед запуском скрипта."
    exit 1
fi

# 2. Поддержка --local режима (установка из локального файла)
LOCAL_FILE=""
if [ "$1" = "--local" ]; then
    [ -n "$2" ] || { echo "Ошибка: укажите путь к бинарнику: sh install.sh --local ./doq-web-linux-<arch>"; exit 1; }
    [ -f "$2" ] || { echo "Ошибка: файл не найден: $2"; exit 1; }
    LOCAL_FILE="$2"
    echo "Режим --local: устанавливаем из $LOCAL_FILE"
fi

# 3. Проверяем утилиту curl (нужна только для онлайн-установки)
if [ -z "$LOCAL_FILE" ] && ! command -v curl >/dev/null 2>&1; then
    echo "Ошибка: curl не установлен!"
    echo "Установите его командой: opkg update && opkg install curl"
    echo "Или используйте локальный режим: sh install.sh --local ./doq-web-linux-<arch>"
    exit 1
fi

# 4. Определяем архитектуру системы.
# Entware (на котором держится весь установщик) точно знает свой таргет:
# opkg print-architecture возвращает строки вроде aarch64-k3.10,
# mipselsf-k3.4, mipssf-k3.4 и т.д. uname -m на Keenetic отдаёт "mips"
# для обеих endian-ностей, а /proc/cpuinfo на части прошивок не содержит
# слова "little" — отсюда KN-1810 (mipselsf) ошибочно уезжал в mipssf.
detect_arch() {
    arch=$(opkg print-architecture 2>/dev/null | awk '!/all|noarch/ {print $2; exit}')
    case "$arch" in
        aarch64*|arm64*) echo "aarch64-k3.10" ;;
        mips64r6*|mips64*el*) echo "mips64elsf-k5.0" ;;
        mips64*)         echo "mips64sf-k5.0" ;;
        mipsel*)         echo "mipselsf-k3.4" ;;
        mips*)           echo "mipssf-k3.4" ;;
        armv7*)          echo "armv7sf-k3.2" ;;
        armv5*)          echo "armv5sf-k3.2" ;;
        x64*|x86_64*)    echo "x64-k3.2" ;;
        *)               echo "Ошибка: неизвестный таргет Entware: $arch" >&2; exit 1 ;;
    esac
}

TARGET=$(detect_arch)
echo "Определённый таргет Entware: $TARGET"

# 5. Скачиваем бинарник из последней версии GitHub Releases
# /tmp на роутерах — это tmpfs (RAM), поэтому скачиваем сразу в BIN_DIR,
# иначе на моделях с 32-64 МБ RAM скачивание вызывает OOM-kill.
mkdir -p "$BIN_DIR"
TMP="$BIN_DIR/$NAME.part"

# Определяем имя бинарника для таргета
case "$TARGET" in
    aarch64-k3.10)       BINARY="doq-web-linux-arm64" ;;
    armv7sf-k3.2)        BINARY="doq-web-linux-arm" ;;
    armv5sf-k3.2)        BINARY="doq-web-linux-armv5" ;;
    mips64elsf-k5.0)     BINARY="doq-web-linux-mips64le" ;;
    mips64sf-k5.0)       BINARY="doq-web-linux-mips64" ;;
    mipselsf-k3.4)       BINARY="doq-web-linux-mipsle" ;;
    mipssf-k3.4)         BINARY="doq-web-linux-mips" ;;
    x64-k3.2)            BINARY="doq-web-linux-amd64" ;;
    *)                   BINARY="doq-web-linux-mipsle" ;;
esac

# 6. Вспомогательные функции скачивания и проверки.
#
# busybox curl на Keenetic не следует за HTTPS-редиректами (302 → 302 →
# 302) без флага -L. Поэтому fetch() использует -L, а fetch_nol() — без.
#
# GITHUB_TOKEN поднимает лимит GitHub API с 60 запросов в час на IP до 5000.
# Без токена несколько установок подряд с одного белого IP (провайдерский NAT,
# офис, несколько роутеров) упираются в "curl: (22) ... error: 403".
fetch() { # $1 = url, $2 = файл — скачивание с -L (следовать редиректам)
    if [ -n "$GITHUB_TOKEN" ]; then
        curl -fsSL --connect-timeout 10 --max-time 300 \
            -H "Authorization: Bearer $GITHUB_TOKEN" -o "$2" "$1"
    else
        curl -fsSL --connect-timeout 10 --max-time 300 -o "$2" "$1"
    fi
}

fetch_nol() { # $1 = url, $2 = файл — без -L (на случай если URL не редиректит)
    if [ -n "$GITHUB_TOKEN" ]; then
        curl -fsS --connect-timeout 10 --max-time 300 \
            -H "Authorization: Bearer $GITHUB_TOKEN" -o "$2" "$1"
    else
        curl -fsS --connect-timeout 10 --max-time 300 -o "$2" "$1"
    fi
}

gh_json() { # $1 = url, ответ API в stdout
    if [ -n "$GITHUB_TOKEN" ]; then
        curl -fsSL --connect-timeout 5 --max-time 15 \
            -H "Accept: application/vnd.github+json" \
            -H "Authorization: Bearer $GITHUB_TOKEN" "$1"
    else
        curl -fsSL --connect-timeout 5 --max-time 15 \
            -H "Accept: application/vnd.github+json" "$1"
    fi
}

# Проверка ELF: неверно определённый таргет приводит к "syntax error:
# unexpected (": ядро отказывает в exec (ENOEXEC), а busybox sh читает
# бинарник как скрипт. Байт 5 (0-based) ELF-заголовка: 01 = little, 02 = big.
want=01
case "$TARGET" in
    mipssf-k3.4)   want=02 ;;
    mips64sf-k5.0) want=02 ;;
esac

is_elf() { # $1 = файл, 0 если это ELF
    [ "$(od -An -tx1 -N4 "$1" 2>/dev/null | tr -d ' \n')" = "7f454c46" ]
}

# 7. Скачиваем или копируем бинарник.
if [ -n "$LOCAL_FILE" ]; then
    # Локальный режим: копируем файл, GitHub не трогаем.
    cp "$LOCAL_FILE" "$TMP"
    echo "Скопировано из $LOCAL_FILE"
    URL="(локальный файл)"
else
    api_url="https://api.github.com/repos/$REPO/releases/latest"
    base_url="https://github.com/$REPO/releases/latest/download"

    # JSON релиза берём ОДИН раз: из него и адрес бинарника, и адрес SHA256SUMS.
    # Ответ пуст, если API недоступен или вернул 403 (лимит запросов) — тогда
    # ставим по прямому адресу /releases/latest/download/, он отдельного
    # лимита не имеет.
    rel_json=$(gh_json "$api_url" 2>/dev/null || true)
    if [ -z "$rel_json" ]; then
        echo "GitHub API недоступен (403 — лимит 60 запросов/час на IP?) — качаю по прямому адресу."
    fi

    asset_url() { # $1 = имя файла в релизе
        [ -n "$rel_json" ] || return 0
        printf '%s' "$rel_json" | awk -v want="$1" '
            /"browser_download_url"/ {
                match($0, /https:[^"]+/)
                u = substr($0, RSTART, RLENGTH)
                if (substr(u, length(u) - length(want) + 1) == want) { print u; exit }
            }'
    }

    # busybox curl на Keenetic не следует за редиректами HTTPS, поэтому
    # пробуем с -L (curl сам перейдёт по 302). Если не сработало —
    # пробовать без -L (на случай если URL не редиректит).
    SUMS_URL=""
    URL=""
    for nol in "" "nol"; do
        for u in "$base_url/$BINARY" "$(asset_url "$BINARY")"; do
            [ -n "$u" ] || continue
            echo "Скачиваю $BINARY (таргет $TARGET): $u"
            if [ "$nol" = "nol" ]; then
                if ! fetch_nol "$u" "$TMP"; then
                    echo "  -> curl без -L вернул код $?"
                    continue
                fi
            else
                if ! fetch "$u" "$TMP"; then
                    echo "  -> curl с -L вернул код $?"
                    continue
                fi
            fi
            if is_elf "$TMP"; then
                URL="$u"
                echo "  -> OK (ELF, $(wc -c < "$TMP") байт)"
                break 2
            else
                echo "  -> не ELF (скачалась страница ошибки GitHub), пробую"
            fi
        done
    done
    [ -n "$URL" ] || {
        rm -f "$TMP"
        echo "Ошибка: не удалось скачать бинарник."
        echo "  Возможные причины:"
        echo "  - Нет доступа к github.com (DNS, фаервол, прокси)"
        echo "  - Нет сертификатов CA: opkg install ca-bundle"
        echo "  - Лимит API GitHub (403): повторите позже"
        echo "  - Файл слишком большой для RAM: шлем по частям"
        echo "Офлайн-установка: скачайте файл на ПК и выполните"
        echo "  sh install.sh --local /tmp/$BINARY"
        exit 1
    }

    # 8. Проверяем SHA256SUMS релиза (если sha256sum есть на роутере).
    if command -v sha256sum >/dev/null 2>&1; then
        SUMS_URL="$(asset_url SHA256SUMS)"
        [ -n "$SUMS_URL" ] || SUMS_URL="$base_url/SHA256SUMS"
        sums_tmp="$BIN_DIR/$NAME.sums"
        if fetch_nol "$SUMS_URL" "$sums_tmp" 2>/dev/null && [ -s "$sums_tmp" ]; then
            want_hash=$(awk -v f="$BINARY" '$2==f{print $1}' "$sums_tmp")
            got_hash=$(sha256sum "$TMP" | awk '{print $1}')
            rm -f "$sums_tmp"
            if [ -n "$want_hash" ]; then
                if [ "$got_hash" != "$want_hash" ]; then
                    rm -f "$TMP"
                    echo "Ошибка: SHA256 не совпадает (скачанный файл повреждён)."
                    exit 1
                fi
                echo "SHA256 OK"
            fi
        else
            rm -f "$sums_tmp"
            echo "SHA256SUMS недоступен — проверку пропускаю"
        fi
    fi
fi

# 9. Проверяем ELF и порядок байт.
if ! is_elf "$TMP"; then
    rm -f "$TMP"
    echo "Ошибка: файл не является ELF-бинарником (скачалась страница ошибки GitHub)."
    echo "Офлайн-установка: sh install.sh --local /tmp/$BINARY"
    exit 1
fi
got=$(od -An -tx1 -j5 -N1 "$TMP" 2>/dev/null | tr -d ' \n')
if [ "$got" != "$want" ]; then
    rm -f "$TMP"
    echo "Ошибка: порядок байт бинарника ($got) не совпадает с таргетом $TARGET (ожидался $want)."
    echo "Проверьте вывод: opkg print-architecture"
    exit 1
fi
echo "ELF OK (endianness $got)"

# 10. Проверяем, что бинарник запускается
"$TMP" -version >/dev/null 2>&1 || {
    rm -f "$TMP"
    echo "Ошибка: скачанный бинарник не запускается (возможно, не подходит для вашей архитектуры)."
    exit 1
}
echo "Binary OK"

# 11. Устанавливаем бинарный файл
chmod +x "$TMP"
mv "$TMP" "$BIN_DIR/$NAME"
sync
echo "Установлен бинарник: $BIN_DIR/$NAME"

# 12. Создаем службу автозапуска для Entware (rc.unslung)
mkdir -p "$INIT_DIR"
cat > "$INIT_DIR/S57doq-web" << 'EOF'
#!/bin/sh

ENABLED=yes
PROCS=doq-web
ARGS=""
PREARGS=""
DESC="keenetic-doq web service"
PATH=/opt/sbin:/opt/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
EOF

chmod +x "$INIT_DIR/S57doq-web"

# 13. Попытка добавить автозапуск в rc.custom (есть не на всех прошивках).
# На современных KeeneticOS /flash — read-only, и это не проблема: ndm при
# монтировании раздела с /opt сам запускает скрипты /opt/etc/init.d/S*,
# а S57doq-web создан на шаге 12. Неудачная запись не должна ронять
# установку — на set -e перенаправление >> убило бы скрипт до запуска сервиса.
RC_LINE='if [ -x /opt/etc/init.d/S57doq-web ]; then /opt/etc/init.d/S57doq-web start; fi'
if grep -qs "S57doq-web" /flash/rc.custom 2>/dev/null; then
    echo "Автозапуск в /flash/rc.custom уже существует"
elif echo "$RC_LINE" >> /flash/rc.custom 2>/dev/null; then
    chmod +x /flash/rc.custom
    echo "Добавлен автозапуск в /flash/rc.custom"
else
    echo "rc.custom недоступен (read-only) — автозапуск через /opt/etc/init.d/S57doq-web"
fi

# 14. Перезапускаем сервис.
#
# При обновлении старый doq-web уже запущен и держит порт 8091. Если просто
# вызвать start, rc.func увидит живой PID из pid-файла, решит "уже работает" и
# ничего не сделает — а проверка pidof ниже подтвердит успех, хотя в памяти
# останется старая версия. Поэтому сначала stop.
if pidof "$NAME" >/dev/null 2>&1; then
    echo "Останавливаю запущенную версию..."
    "$INIT_DIR/S57doq-web" stop || true
    sleep 1
fi

if netstat -tln 2>/dev/null | grep -q ':8091 '; then
    echo "ВНИМАНИЕ: порт 8091 занят другим процессом:"
    netstat -tlnp 2>/dev/null | grep ':8091 '
fi

echo "Запускаю службу..."
"$INIT_DIR/S57doq-web" start || true
sleep 2

# Проверка PID и диагностика при неудаче
if pidof "$NAME" >/dev/null 2>&1; then
    echo "Сервис работает, PID: $(pidof $NAME)"
else
    echo "СЕРВИС НЕ ЗАПУСТИЛСЯ. Диагностика окружения:"
    "$BIN_DIR/$NAME" --probe || echo "probe вернул код $?"
    echo "Пробую запустить в foreground (5 секунд):"
    "$BIN_DIR/$NAME" 2>&1 &
    FPID=$!
    sleep 5
    if kill -0 "$FPID" 2>/dev/null; then
        echo "В foreground процесс жив — проблема в rc.func/ENVIRONMENT."
        kill "$FPID" 2>/dev/null
    else
        wait "$FPID" 2>/dev/null
        echo "Процесс умер с кодом $? — выше должен быть текст ошибки."
    fi
    exit 1
fi

# 15. Определяем локальный IP-адрес роутера
IP=$(ip -4 addr show br0 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP=$(ip -4 addr show br1 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP="<IP-роутера>"

echo ""
echo "=== Установка успешно завершена! ==="
echo "Веб-интерфейс доступен по адресу: http://$IP:8091"
echo ""
