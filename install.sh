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

# 2. Проверяем утилиту curl
if ! command -v curl >/dev/null 2>&1; then
    echo "Ошибка: curl не установлен!"
    echo "Установите его командой: opkg update && opkg install curl"
    exit 1
fi

# 3. Определяем архитектуру системы.
# На Keenetic uname -m возвращает "mips" для обеихendianness.
# Entware таргеты: mipselsf-k3.4, mipssf-k3.4, aarch64-k3.10, armv7sf-k3.2, armv5sf-k3.2
detect_arch() {
    ARCH=$(uname -m)
    case "$ARCH" in
        aarch64|arm64)
            echo "aarch64-k3.10" ;;
        armv7l)
            echo "armv7sf-k3.2" ;;
        armv5*)
            echo "armv5sf-k3.2" ;;
        mipsel)
            echo "mipselsf-k3.4" ;;
        mips)
            # Проверяемendianness через /proc/cpuinfo
            if grep -qi 'little' /proc/cpuinfo 2>/dev/null; then
                echo "mipselsf-k3.4"
            else
                echo "mipssf-k3.4"
            fi
            ;;
        x86_64)
            echo "x64-k3.2" ;;
        *)
            echo "Ошибка: Неподдерживаемая архитектура: $ARCH"
            exit 1
            ;;
    esac
}

TARGET=$(detect_arch)
echo "Определённый таргет Entware: $TARGET"

# 4. Скачиваем бинарник из последней версии GitHub Releases
# /tmp на роутерах — это tmpfs (RAM), поэтому скачиваем сразу в BIN_DIR,
# иначе на моделях с 32-64 МБ RAM скачивание вызывает OOM-kill.
mkdir -p "$BIN_DIR"
TMP="$BIN_DIR/$NAME.part"
# Бинарники собираются под конкретный таргет, имя соответствует архитектуре.
case "$TARGET" in
    aarch64-k3.10)    BINARY="doq-web-linux-arm64" ;;
    armv7sf-k3.2)     BINARY="doq-web-linux-arm" ;;
    armv5sf-k3.2)     BINARY="doq-web-linux-armv5" ;;
    mipselsf-k3.4)    BINARY="doq-web-linux-mipsle" ;;
    mipssf-k3.4)      BINARY="doq-web-linux-mips" ;;
    x64-k3.2)         BINARY="doq-web-linux-amd64" ;;
    *)                BINARY="doq-web-linux-mipsle" ;;
esac
URL="https://github.com/$REPO/releases/latest/download/$BINARY"

echo "Скачиваю $BINARY (таргет $TARGET) из $URL ..."
curl -fsSL -o "$TMP" "$URL" || {
    rm -f "$TMP"
    echo "Ошибка: Не удалось скачать бинарник."
    echo "Проверьте интернет-соединение и наличие скомпилированного файла в GitHub Releases."
    exit 1
}

# 5. Устанавливаем бинарный файл
chmod +x "$TMP"
mv "$TMP" "$BIN_DIR/$NAME"
sync
echo "Установлен бинарник: $BIN_DIR/$NAME"

# 6. Создаем службу автозапуска для Entware (rc.unslung)
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

# 7. Добавляем автозапуск в rc.custom (KeeneticOS запускает этот скрипт при старте)
# На KeeneticOS /opt/etc/init.d/ скрипты не запускаются автоматически —
# нужен rc.custom хук, который вызывается ndm в процессе загрузки.
if ! grep -q "S57doq-web" /flash/rc.custom 2>/dev/null; then
    echo 'if [ -x /opt/etc/init.d/S57doq-web ]; then /opt/etc/init.d/S57doq-web start; fi' >> /flash/rc.custom
    chmod +x /flash/rc.custom
    echo "Добавлен автозапуск в /flash/rc.custom"
else
    echo "Автозапуск в /flash/rc.custom уже существует"
fi

# 8. Запускаем сервис
if netstat -tln 2>/dev/null | grep -q ':8091 '; then
    echo "ВНИМАНИЕ: порт 8091 уже занят:"
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

# 9. Определяем локальный IP-адрес роутера
IP=$(ip -4 addr show br0 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP=$(ip -4 addr show br1 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP="<IP-роутера>"

echo ""
echo "=== Установка успешно завершена! ==="
echo "Веб-интерфейс доступен по адресу: http://$IP:8091"
echo ""
