#!/bin/sh
set -e

REPO="accept4/keenetic-doq-web"
BIN_DIR="/opt/sbin"
INIT_DIR="/opt/etc/init.d"
NAME="doq-web"

echo "=== keenetic-doq-web installer ==="

# Определяем архитектуру
ARCH=$(uname -m)
case "$ARCH" in
  aarch64|arm64)  BINARY="doq-web-linux-arm64" ;;
  mipsel|mips)    BINARY="doq-web-linux-mipsle" ;;
  mips64)         BINARY="doq-web-linux-mips64" ;;
  x86_64)         BINARY="doq-web-linux-amd64" ;;
  *)
    echo "Ошибка: Неподдерживаемая архитектура: $ARCH"
    exit 1
    ;;
esac

# Скачиваем бинарник из релизов
TMP="/tmp/$BINARY"
URL="https://github.com/$REPO/releases/latest/download/$BINARY"

echo "Скачиваю $BINARY ..."
curl -fsSL -o "$TMP" "$URL" || {
  echo "Ошибка: Не удалось скачать $BINARY."
  echo "Убедитесь, что в GitHub Releases выложен скомпилированный файл с таким именем."
  exit 1
}

chmod +x "$TMP"
mv "$TMP" "$BIN_DIR/$NAME"
echo "Установлен бинарник: $BIN_DIR/$NAME"

# Создаем init-скрипт для Entware (rc.unslung)
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

# Запускаем службу
"$INIT_DIR/S57doq-web" start

# Определяем IP-адрес роутера в локальной сети
IP=$(ip -4 addr show br0 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP=$(ip -4 addr show br1 2>/dev/null | awk '/inet /{print $2}' | cut -d/ -f1)
[ -z "$IP" ] && IP="IP-роутера"

echo ""
echo "Установка успешно завершена!"
echo "Веб-интерфейс доступен по адресу: http://$IP:8088"
echo ""
