#!/bin/sh
# Build the AArch64 userspace without running target programs or requiring QEMU.
# apk verifies repository signatures; preserve its installed database for provenance.
set -eu
test "$#" -eq 1 || { echo 'usage: base.sh OUTPUT.tar.gz' >&2; exit 2; }
out=$1
docker run --rm --network host \
  alpine@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f \
  sh -ec '
    mkdir -p /rootfs/etc/apk/keys
    cp /usr/share/apk/keys/aarch64/* /rootfs/etc/apk/keys/
    printf "%s\n" https://dl-cdn.alpinelinux.org/alpine/v3.22/main > /rootfs/etc/apk/repositories
    # Encore uses Go TLS, not busybox wget. Exclude the unused external helper
    # while its GPL-only/OpenSSL linkage grant remains unresolved.
    apk --arch aarch64 --root /rootfs --initdb --no-scripts add \
      alpine-baselayout busybox busybox-static wpa_supplicant dbus bluez \
      flock iptables iptables-legacy alpine-release alsa-utils "!ssl_client" >&2
    # Device nodes belong to the booted kernel, not the builder container.
    tar -czf - --exclude="./dev/*" -C /rootfs .
  ' > "$out.part"
mv "$out.part" "$out"
echo "AArch64 base: $out" >&2
