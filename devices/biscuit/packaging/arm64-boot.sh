#!/bin/busybox.static sh
# Called only after the trial initramfs has moved its RAM root and ro store.
set -eu
BB=/bin/busybox.static
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 077
exec > /run/encore/base.log 2>&1
$BB --install -s
# Do not hide the initramfs /run or remount any persistent filesystem writable.
mount -t tmpfs -o mode=0700,size=16m tmpfs /data
mount -t tmpfs -o mode=1777,size=16m tmpfs /tmp
mkdir -p /data/encore /run/encore /dev/pts
# Base failure leaves init's independently supervised USB console available.
/usr/local/sbin/encore-firewall apply
i=0
while :; do
  owner=$($BB cat /run/encore/usb-ready 2>/dev/null || :)
  case "$owner" in ''|*[!0-9]*) ;; *) kill -0 "$owner" 2>/dev/null && break;; esac
  i=$((i+1)); [ "$i" -le 20 ] || { echo 'USB console unavailable; radios not started'; exit 1; }
  sleep 1
done
ip link set lo up
fw=/store/encore/firmware
test -s "$fw/WMT_SOC.cfg"
test -s "$fw/WIFI_RAM_CODE_8163"
mkdir -p /system/vendor /lib/firmware
ln -sfn "$fw" /system/vendor/firmware
ln -sfn /system/vendor /vendor
for f in "$fw"/*; do ln -sfn "$f" /lib/firmware/; done
# The compiled vendor WLAN glue opens this directory directly, not via
# request_firmware(), so /lib/firmware alone cannot satisfy its lookup.
ln -sfn "$fw" /etc/firmware
/usr/local/bin/wmtup -patches "$fw" -power > /run/wmtup.log 2>&1 &
i=0
while [ ! -e /sys/class/net/wlan0 ]; do
  i=$((i+1)); [ "$i" -lt 60 ] || exit 1; sleep 1
done
# Deliberately do not import credentials/bonds/keys from the persistent data.
echo ctrl_interface=/run/wpa_supplicant > /run/encore/wpa_supplicant.conf
# The retained Alpine binary lacks -f and returns success after printing usage.
wpa_supplicant -B -D nl80211 -i wlan0 -c /run/encore/wpa_supplicant.conf > /run/wpa.log 2>&1
test "$(wpa_cli -i wlan0 ping)" = PONG
udhcpc -b -i wlan0 -s /etc/encore/dhcp.sh -p /run/udhcpc.pid
touch /run/encore/base-ready
