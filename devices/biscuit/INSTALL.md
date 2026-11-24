# Install Encore on Echo Dot (2nd gen)

This guide is for a first install or a clean reinstall on an unlocked Echo Dot
(2nd gen, Biscuit). **A clean install clears `/cache` and `/data`, including
saved Wi-Fi and Home Assistant pairing.** It keeps TWRP and both Fire OS system
partitions intact. To keep your setup, use
[signed OTA updates](packaging/README.md#signed-native-ab-pilot) instead.

## Before you start

- An unlocked Dot with Biscuit TWRP, root ADB, and the SHA-256 of the **full
  recovery partition** from a trusted independent source. Encore doesn't unlock
  the Dot or provide TWRP. See [recovery and firmware requirements](packaging/README.md#private-persistent-arm64-starter).
- [Android platform-tools](https://developer.android.com/tools/releases/platform-tools),
  Git, GitHub CLI (`gh`), Python 3, and OpenSSL with Ed25519 support.
- Space on your computer for private backups of cache, userdata, and boot partitions.

## 1. Download the signed release

Choose an immutable [Biscuit Dev release](https://github.com/farooqu/encore-satellite/releases?q=tag%3Abiscuit-)
marked **native A/B**. Start in an empty directory and use the same shell
throughout. Download every asset and use a full clone; the installer checks
the receipt against the release files and Git history.

```sh
umask 077
tag=biscuit-v0.10.0-dev.4 # Replace with the release you're installing.
repo=farooqu/encore-satellite
gh release download "$tag" --repo "$repo" --dir candidate
git clone "https://github.com/$repo.git" encore-satellite
git -C encore-satellite checkout --detach "$tag"
cd encore-satellite
git rev-parse HEAD
```

Check that the printed commit matches the release notes. Then get the receipt
hash from GitHub and export Encore's pinned verification key:

```sh
gh api "repos/$repo/releases/tags/$tag" \
  --jq 'select(.immutable) | .assets[] | select(.name == "receipt.json") | .digest'

PYTHONPATH=release python3 -c 'import sys; from pipeline import BISCUIT_ARM64_ROOT; sys.stdout.buffer.write(BISCUIT_ARM64_ROOT)' \
  | openssl pkey -pubin -inform DER -pubout -out ../biscuit-public.pem
```

Set `RECEIPT_SHA256` to the digest without `sha256:`. If GitHub returns no
digest, get the reviewed hash from the maintainer before continuing.

## 2. Boot into recovery and run preflight

Connect one Dot and put it in fastboot. Use its USB serial for `DOT_SERIAL`
and the independently verified recovery hash for `TWRP_SHA256`. Check that
fastboot reports `BISCUIT` before rebooting into recovery.

```sh
fastboot devices -l
fastboot -s "${DOT_SERIAL:?set the Dot USB serial}" getvar product
```

```sh
fastboot -s "${DOT_SERIAL:?set the Dot USB serial}" reboot recovery
# Wait for this Dot to appear in recovery before continuing.
adb devices -l
adb -s "${DOT_SERIAL:?set the Dot USB serial}" shell 'id; getprop ro.twrp.version'
```

Confirm the same serial and `uid=0`. Leave both stock system partitions
unmounted. The boot backup directory must not already exist.

```sh
install_encore() {
  python3 devices/biscuit/packaging/install.py install-native \
    --serial "${DOT_SERIAL:?set the Dot USB serial}" \
    --candidate ../candidate \
    --receipt-sha256 "${RECEIPT_SHA256:?set the reviewed receipt hash}" \
    --public-key ../biscuit-public.pem \
    --twrp-sha256 "${TWRP_SHA256:?set the independently verified recovery hash}" \
    --backup-dir ../dot-rollback "$@"
}
install_encore
```

## 3. Clear cache and userdata in recovery

Continue only after **Read-only signed native preflight passed**.

1. **Check the mounts.** `/cache` must be ext4 on `mmcblk0p15` and `/data`
   ext4 on `mmcblk0p16`. Check for aliases (such as `/sdcard`) and nested
   mounts. Stop if the layout differs or another filesystem could be affected.
2. **Back up both filesystems.** Keep complete, verified backups on your
   computer, including hidden files and metadata. For raw backups, unmount
   the filesystems and their aliases first. The installer backs up boot
   partitions separately; it doesn't back up cache or userdata.
3. **Clear only `/cache` and `/data`.** Remove all their contents, including
   internal storage and hidden files. Don't use a generic factory reset or
   reformat either partition. Verify both roots are empty except for ext4
   bookkeeping such as `lost+found`, then stay in TWRP.

## 4. Install and reboot

If TWRP uses `/data/local/tmp` as `TMPDIR`, recreate that directory empty now.
Remove it after apply if you created it for the installer.

```sh
install_encore --apply
```

If apply succeeds, verify the boot-partition backups:

```sh
(cd ../dot-rollback && shasum -a 256 -c SHA256SUMS)
```

If either step fails, **stay in TWRP**. Don't retry over a partial install.
If both pass, start Encore with `adb -s "$DOT_SERIAL" reboot`.

## 5. Set up Wi-Fi, Home Assistant, and voice

1. **Wi-Fi:** Press **Action** and use Home Assistant's Improv discovery or an
   [Improv-compatible client](https://www.improv-wifi.com/) to join a trusted
   WPA2-Personal network.
2. **Home Assistant:** Once the Dot is online, press **Action** again. In
   **Settings → Devices & services**, add the discovered Dot within two
   minutes. If it doesn't appear, try the Dot's IP on port `6053`. Home
   Assistant needs zero-PSK provisioning support and generates the key for you.
3. **Voice:** Add the Dot separately in [Voice Incubator](https://github.com/OHF-Voice/voice-incubator)
   via discovery or its IP on port `6055` (LAN only). Unmute the Dot and press
   **Action** to try a voice command.

## Hardware tests

Two unlocked Dots booted the signed [0.1.63 seed](https://github.com/farooqu/encore-satellite/releases/tag/biscuit-0.1.63);
one also paired with Home Assistant and ran voice commands. A previously
enrolled Dot passed the clean wipe and reinstall with the signed
[0.10.0-dev.3 seed](https://github.com/farooqu/encore-satellite/releases/tag/biscuit-v0.10.0-dev.3):
both slots verified, TWRP stayed intact, and Wi-Fi setup started from scratch.
These tests didn't exercise unlocking, OTA, or slot failover.
