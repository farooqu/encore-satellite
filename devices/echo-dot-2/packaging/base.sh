#!/bin/sh
# Build an ARMv7 userspace without running ARM programs or requiring QEMU.
# apk verifies repository signatures; preserve its installed database for provenance.
set -eu
out=${1:?usage: base.sh OUTPUT.tar.gz}
docker run --rm --network host \
  alpine@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f \
  sh -ec '
    mkdir -p /rootfs/etc/apk/keys
    cp /usr/share/apk/keys/armv7/* /rootfs/etc/apk/keys/
    printf "%s\n" https://dl-cdn.alpinelinux.org/alpine/v3.22/main > /rootfs/etc/apk/repositories
    apk --arch armv7 --root /rootfs --initdb --no-scripts add \
      alpine-baselayout busybox busybox-static wpa_supplicant dbus bluez \
      dropbear flock iptables iptables-legacy >&2
    tar -czf - -C /rootfs .
  ' > "$out.part"
mv "$out.part" "$out"
echo "ARMv7 base: $out" >&2
