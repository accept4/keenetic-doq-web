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

# 6. Скачиваем или копируем бинарник — как в upstream keenetic-doq.
#
# busybox curl на Keenetic иногда не следует за HTTPS-редиректами (302) без
# явного -L. Поэтому fetch() использует -f -S -L (раздельно).
# Также проверяем размер скачанного файла: если < 1 МБ — это HTML-страница
# ошибки, а не ELF-бинарник.
fetch() { # $1 = url, $2 = output file
    if [ -n "$GITHUB_TOKEN" ]; then
        curl -f -S -L -H "Authorization: Bearer $GITHUB_TOKEN" -o "$2" "$1"
    else
        curl -f -S -L -o "$2" "$1"
    fi
}

# Проверка ELF: неверно определённый таргет приводит к "syntax error:
# unexpected (": ядро отказывает в exec (ENOEXEC), а busybox sh читает
# бинарник как скрипт. Байт 5 (0-based) ELF-заголовка: 01 = little, 02 = big.
#
# Магию читаем через head -c, а не od -N/-j: busybox od понимает эти опции
# не всегда, и на роутере проверка возвращала пустоту даже для валидного
# файла (при уже совпавшем SHA256!).
want=01
case "$TARGET" in
    mipssf-k3.4)   want=02 ;;
    mips64sf-k5.0) want=02 ;;
esac

elf_magic() { # $1 = файл, печатает 4 байта магии hex или пусто
    head -c 4 "$1" 2>/dev/null | od -An -tx1 2>/dev/null | tr -d ' 	\n\r'
}

elf_endian() { # $1 = файл, печатает байт EI_DATA (01/02) или пусто
    head -c 6 "$1" 2>/dev/null | tail -c 1 | od -An -tx1 2>/dev/null | tr -d ' 	\n\r'
}

if [ -n "$LOCAL_FILE" ]; then
    # Локальный режим: копируем файл, GitHub не трогаем.
    cp "$LOCAL_FILE" "$TMP"
    echo "Скопировано из $LOCAL_FILE"
    hash_ok=""
else
    base="https://github.com/$REPO/releases/latest/download"
    echo "Скачиваю $BINARY (таргет $TARGET)"
    fetch "$base/$BINARY" "$TMP" \
        || { rm -f "$TMP"; echo "Ошибка: download failed (нет сети? приватный репо? --local)"; exit 1; }

    # Проверяем размер: бинарник должен быть > 1 МБ. Если меньше — это
    # HTML-страница ошибки (404, 403 и т.д.), а не ELF-бинарник.
    fsize=$(wc -c < "$TMP" 2>/dev/null || echo 0)
    if [ "$fsize" -lt 1000000 ]; then
        rm -f "$TMP"
        echo "Ошибка: скачался маленький файл ($fsize байт) — это не бинарник."
        echo "  Проверьте: curl -fSL $base/$BINARY"
        echo "Офлайн-установка: скачайте файл на ПК и выполните"
        echo "  sh install.sh --local /tmp/$BINARY"
        exit 1
    fi

    # SHA256SUMS — как в upstream: если sha256sum есть, проверяем.
    # Совпавший SHA256 — достаточное доказательство подлинности и целости:
    # он снимает и ELF-проверку, и проверку endianness (эталонный хеш
    # соответствует заведомо нужному бинарнику).
    hash_ok=""
    if command -v sha256sum >/dev/null 2>&1; then
        fetch "$base/SHA256SUMS" /opt/tmp/doq-web.sums \
            || { rm -f "$TMP"; echo "Ошибка: SHA256SUMS download failed"; exit 1; }
        want_hash=$(awk -v f="$BINARY" '$2==f{print $1}' /opt/tmp/doq-web.sums)
        got_hash=$(sha256sum "$TMP" | awk '{print $1}')
        rm -f /opt/tmp/doq-web.sums
        if [ -z "$want_hash" ]; then
            rm -f "$TMP"
            echo "Ошибка: $BINARY не найден в SHA256SUMS (нет в релизе?)"
            exit 1
        fi
        [ "$got_hash" = "$want_hash" ] \
            || { rm -f "$TMP"; echo "Ошибка: SHA256 mismatch — corrupted download"; exit 1; }
        echo "SHA256 OK"
        hash_ok=1
    else
        echo "WARNING: sha256sum not found, skipping checksum"
    fi
fi

# 7. ELF и порядок байт — только если SHA256 не подтвердил файл.
if [ -z "$hash_ok" ]; then
    magic=$(elf_magic "$TMP")
    if [ "$magic" != "7f454c46" ]; then
        rm -f "$TMP"
        echo "Ошибка: файл не является ELF-бинарником (магия: '${magic:-пусто}', размер: $(wc -c < "$TMP" 2>/dev/null) байт)."
        echo "  - Нет доступа к objects.githubusercontent.com (файлы релизов)"
        echo "  - Файл не найден в релизе (проверьте, что тег опубликован)"
        echo "Офлайн-установка: скачайте файл на ПК и выполните"
        echo "  sh install.sh --local /tmp/$BINARY"
        exit 1
    fi
    got=$(elf_endian "$TMP")
    if [ -n "$got" ] && [ "$got" != "$want" ]; then
        rm -f "$TMP"
        echo "Ошибка: порядок байт бинарника ($got) не совпадает с таргетом $TARGET (ожидался $want)."
        echo "Проверьте вывод: opkg print-architecture"
        exit 1
    fi
    echo "ELF OK (endianness ${got:-не проверен})"
fi

# 8. Главная функциональная проверка: бинарник должен запуститься.
# Именно она ловит "не та архитектура" надёжнее любых разборов заголовка.
"$TMP" -version >/dev/null 2>&1 \
    || { rm -f "$TMP"; echo "Ошибка: бинарник не запускается (wrong architecture?)"; exit 1; }
echo "Binary OK"

# 9. Устанавливаем бинарный файл
chmod +x "$TMP"
mv "$TMP" "$BIN_DIR/$NAME"
sync
echo "Установлен бинарник: $BIN_DIR/$NAME"

# 10. Создаем службу автозапуска для Entware (rc.unslung)
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

# 11. Попытка добавить автозапуск в rc.custom (есть не на всех прошивках).
# На современных KeeneticOS /flash — read-only, и это не проблема: ndm при
# монтировании раздела с /opt сам запускает скрипты /opt/etc/init.d/S*,
# а S57doq-web создан на шаге 10. Неудачная запись не должна ронять
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

# 12. Перезапускаем сервис.
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

# 13. Определяем локальный IP-адрес роутера
IP=$(ip -4 addr show br0 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP=$(ip -4 addr show br1 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP="<IP-роутера>"

echo ""
echo "=== Установка успешно завершена! ==="
echo "Веб-интерфейс доступен по адресу: http://$IP:8091"
echo ""
