#!/bin/busybox.static sh
set -eu
BB=/bin/busybox.static
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
umask 077
$BB --install -s
slot=$(cat /run/encore/slot)
mount -t tmpfs tmpfs /run
mount -t tmpfs -o mode=1777 tmpfs /tmp
mkdir -p /run/encore /store /data /dev/pts
echo "$slot" > /run/encore/slot
mount -t devpts devpts /dev/pts 2>/dev/null || :
mount -t ext4 -o noatime /dev/mmcblk0p15 /store
mount -t ext4 -o noatime /dev/mmcblk0p16 /data
chmod 700 /data/encore
# Both IP families must be protected before any link or radio comes up.
# set -e prevents base-ready (and therefore the runtime) on install failure.
/usr/local/sbin/encore-firewall apply
ip link set lo up
# Required paths are compiled into the MediaTek driver. Create before wmtup:
# a missing WMT_SOC.cfg can crash the old driver during initialization.
fw=/store/encore/firmware
test -s "$fw/WMT_SOC.cfg"
test -s "$fw/WIFI_RAM_CODE_8163"
mkdir -p /system/vendor /lib/firmware
ln -sfn "$fw" /system/vendor/firmware
ln -sfn /system/vendor /vendor
for f in "$fw"/*; do ln -sfn "$f" /lib/firmware/; done
# These are device init services supervised by busybox init, not orb services.
/usr/local/bin/wmtup -patches "$fw" -power > /run/wmtup.log 2>&1 &
i=0
while [ ! -e /sys/class/net/wlan0 ]; do
  i=$((i+1)); [ "$i" -lt 60 ] || exit 1; sleep 1
done
if [ -s /data/encore/wpa_supplicant.conf ]; then
  cp /data/encore/wpa_supplicant.conf /run/encore/wpa_supplicant.conf
else
  echo ctrl_interface=/run/wpa_supplicant > /run/encore/wpa_supplicant.conf
fi
# Alpine omits -f support and prints usage with exit 0 for that option.
# Capture startup output in the shell and require a working control socket.
wpa_supplicant -B -D nl80211 -i wlan0 -c /run/encore/wpa_supplicant.conf > /run/wpa.log 2>&1
test "$(wpa_cli -i wlan0 ping)" = PONG
udhcpc -b -i wlan0 -s /etc/encore/dhcp.sh -p /run/udhcpc.pid
# The runtime waits for a real hardware MAC; no generated LAN identity.
touch /run/encore/base-ready
