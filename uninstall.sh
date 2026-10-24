#!/bin/sh

# Платформа определяется тем же признаком, что и в install.sh / doq-web:
# /etc/openwrt_release есть только на OpenWRT (procd, /usr/sbin), иначе это
# Keenetic с Entware (rc.func, /opt).
if [ -f /etc/openwrt_release ]; then
    echo "Останавливаю doq-web (procd)..."
    /etc/init.d/doq-web stop 2>/dev/null || true
    /etc/init.d/doq-web disable 2>/dev/null || true
    rm -f /etc/init.d/doq-web /usr/sbin/doq-web
    # Убираем свои строки из списка sysupgrade, не трогая чужие записи
    # (в частности строки doqd из апстримового установщика).
    if [ -f /etc/sysupgrade.conf ] && grep -qs "/doq-web" /etc/sysupgrade.conf; then
        grep -v "/doq-web" /etc/sysupgrade.conf > /tmp/sysupgrade.conf \
            && mv /tmp/sysupgrade.conf /etc/sysupgrade.conf
        echo "Записи doq-web убраны из /etc/sysupgrade.conf"
    fi
else
    echo "Останавливаю doq-web..."
    /opt/etc/init.d/S57doq-web stop 2>/dev/null || true
    rm -f /opt/sbin/doq-web
    rm -f /opt/etc/init.d/S57doq-web
fi

echo "keenetic-doq-web удалён."