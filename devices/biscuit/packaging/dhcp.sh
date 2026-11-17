#!/bin/sh
set -eu
case "$1" in
deconfig) ifconfig "$interface" 0.0.0.0;;
bound|renew)
  ifconfig "$interface" "$ip" netmask "$subnet"
  while route del default dev "$interface" 2>/dev/null; do :; done
  [ -z "${router:-}" ] || route add default gw "${router%% *}" dev "$interface"
  : > /etc/resolv.conf
  for server in ${dns:-}; do echo "nameserver $server" >> /etc/resolv.conf; done
  ;;
esac
