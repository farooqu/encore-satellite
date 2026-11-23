# Install Encore on Echo Dot (2nd gen)

Use the signed Alpine AArch64 development build for a fresh install on an
unlocked Biscuit. The installer replaces both boot slots and writes Encore to
both runtime slots. It leaves recovery and the stock system partitions intact.
To keep an existing setup, see [updates and package details](packaging/README.md).

## Before you start

- Use an Echo Dot (2nd gen) that's already unlocked and has verified Biscuit
  TWRP with root ADB. Encore doesn't unlock the Dot or ship TWRP. See the
  [upstream recovery project and stock firmware requirements](packaging/README.md#private-persistent-arm64-starter).
  Keep both Fire OS system partitions intact. The installer checks the radio
  firmware on both. We tested the file set from Fire OS 6.5.7.4.
- Install [Android platform-tools](https://developer.android.com/tools/releases/platform-tools),
  Git, the GitHub CLI (`gh`), Python 3, and OpenSSL with Ed25519 `pkeyutl`
  support. On macOS, put a recent OpenSSL on `PATH` if the system `openssl`
  doesn't support that operation.
- Get the **SHA-256 of the full TWRP recovery partition** from a trusted
  independent source. A bare TWRP image or a hash from the partition you're
  checking won't verify the recovery. Stop if you lack the expected hash or
  a way back into recovery.
- Connect one Dot at a time. Keep its backups off the device. If the installer
  finds an existing Encore install, stop. Don't format `/cache` or `/data`.

## Download and verify

Pick an immutable `biscuit-<version>` [development release](https://github.com/farooqu/encore-satellite/releases?q=tag%3Abiscuit-)
whose notes say **native A/B**. The installer flashes the `-native-seed.zip`
and checks every asset in the receipt, including the `-native-ota.zip`. Download
the whole release. Use a full Git checkout so the verifier can check the release
identity against history. A source tarball or shallow clone won't work.

Run the commands in the same shell, starting in a new working directory.
Replace the example release tag with the release you reviewed. Verify that its
release notes' source commit matches `git rev-parse HEAD` after checkout.

```sh
umask 077
tag=biscuit-0.1.63
repo=farooqu/encore-satellite
gh release download "$tag" --repo "$repo" --dir candidate
git clone "https://github.com/$repo.git" encore-satellite
git -C encore-satellite checkout --detach "$tag"
cd encore-satellite
git rev-parse HEAD
```

Get the expected SHA-256 of `receipt.json` from the immutable release. The
installer compares this value with the file you downloaded:

```sh
gh api "repos/$repo/releases/tags/$tag" \
  --jq 'select(.immutable) | .assets[] | select(.name == "receipt.json") | .digest'
```

Set `RECEIPT_SHA256` to the hex value after `sha256:`. If the command returns
no digest, get the reviewed hash from the maintainer before you proceed.
Export Encore's pinned Ed25519 public key from the reviewed checkout to verify
the receipt signature:

```sh
PYTHONPATH=release python3 -c 'import sys; from pipeline import BISCUIT_ARM64_ROOT; sys.stdout.buffer.write(BISCUIT_ARM64_ROOT)' \
  | openssl pkey -pubin -inform DER -pubout -out ../biscuit-public.pem
```

## Read-only preflight, then install

With the Dot in fastboot, check the product is `BISCUIT` and use the device's
actual USB serial. Set `DOT_SERIAL`, `RECEIPT_SHA256`, and `TWRP_SHA256` in this
shell before running the commands below.

```sh
fastboot devices -l
fastboot -s "$DOT_SERIAL" getvar product
fastboot -s "$DOT_SERIAL" reboot recovery
# Wait for this Dot to appear in recovery; it can take more than 15 seconds.
adb devices -l
adb -s "$DOT_SERIAL" shell 'id; getprop ro.twrp.version'
```

Expect the same device in recovery and root (`uid=0`). Leave both stock system
partitions unmounted. Use a backup directory that doesn't already exist:

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

Wait for **Read-only signed native preflight passed** before you run
`install_encore --apply`. That command writes both slots. After it succeeds,
verify the backups from the checkout:

```sh
(cd ../dot-rollback && shasum -a 256 -c SHA256SUMS)
```

The installer backs up boot A, boot B, recovery, and MISC before writing. It
selects A after read-back checks and leaves the Dot in TWRP. If apply or a
backup/read-back check fails, **stay in recovery** and inspect the backups.
Don't reboot or retry apply over a partial install. After the checks pass,
start Encore with `adb -s "$DOT_SERIAL" reboot`. Use the USB serial console
during normal boot. Keep a way back into recovery.

## Wi-Fi, Home Assistant, then voice

1. **Wi-Fi.** On first boot, press **Action** to authorize Improv BLE. Use
   [an Improv-compatible client](https://www.improv-wifi.com/) or Home Assistant's
   Improv discovery to join a trusted WPA2-Personal LAN. Wait for the Dot to
   get an IPv4 address and for Encore to finish starting.
2. **Home Assistant.** Press and release **Action again** to open the two-minute
   ESPHome pairing window. In Home Assistant, use **Settings → Devices & services**
   to add the discovered Dot, or add ESPHome by Dot IP on TCP `6053` during the
   window. Home Assistant must support zero-PSK provisioning; it generates the
   permanent key. If discovery is missing, confirm the IP and check whether the
   port opens after pressing Action (`nc -vz DOT_IP 6053`, replacing `DOT_IP`
   with the address). If the window expires, press Action again. If pairing
   saved but controls don't appear, reload the ESPHome integration.
3. **Voice Incubator.** Add the Dot in [Voice Incubator](https://github.com/OHF-Voice/voice-incubator)
   using discovery or the Dot's IP and TCP `6055`. This is a different connection
   from ESPHome. Keep it on a trusted LAN; don't port-forward it. Unmute using
   the physical control and press **Action** to try a command. Then assign a
   wake word in Incubator and try another command. Check that the Dot plays a reply.

## Hardware tests

We installed the signed
[0.1.63 seed release](https://github.com/farooqu/encore-satellite/releases/tag/biscuit-0.1.63)
on two unlocked Dots. Both passed slot read-backs and booted to Wi-Fi setup.
We enrolled one Dot in Home Assistant and ran voice commands. We left the
second at Wi-Fi setup. We didn't test unlocking, OTA, or slot failover.
