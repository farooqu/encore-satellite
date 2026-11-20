# Android signing and updates — gated foundation

This builds on Android source `ec2167426d43c67afcc40b21cd5c33f2470117d7`.
Incubator only; the engine travels inside the APK. No runtime backend selector,
Assist package, OS replacement, remote shell, or unattended installer is added.
The target remains planned/release-unsupported. Android Dev publication needs
the dedicated signing environment and an explicit target-policy change.

## What the app does

In Home Assistant, pair Encore with the encrypted ESPHome integration. It exposes
**APK update**, **Update channel** (`dev` or `stable`), **Check for updates** and
the **APK update status** diagnostic sensor. Fresh and existing installs default to `dev`; the
explicit selection persists alongside pairing/dashboard/privacy state. There is
no channel fallback. Checks run when the satellite starts, every six hours
while it lives, on channel selection and on ESPHome check requests. A stopped
app is not a scheduled/background updater. The update entity shows the installed
and eligible shared-core SemVer; versionCode remains a separate install gate.
Without a verified newer offer, Home Assistant receives the installed version
as both values (it ignores the ESPHome update missing-state flag). Use **APK update
status** to distinguish check failures from “Up to date” and see when local approval is needed.
Eligibility requires a strictly newer `core_version` **and** a higher versionCode;
equal/older semantic versions are never offered, even at higher package revisions.
Build metadata is ignored for precedence; prereleases follow SemVer ordering.
This uses the [shared channel contract](../docs/device-packages/update-contract.md),
including already-published eligible updates. Packaging-only changes with unchanged
core SemVer are not a “next semantic version” OTA update. The installed core is
embedded in BuildConfig and APK manifest metadata at assembly. Main release
builds use the shared source-derived version; local builds default to the version
in `release/targets.json`. The downloaded APK's actual core metadata must match
its signed feed entry.

Choose **Install** on the **APK update** entity. Encore rechecks the selected channel, downloads to private
storage, verifies size/SHA-256, and independently checks the APK package name,
versionCode/versionName and signer against both metadata and the installed app.
Changing channel during a request discards its result. A persisted selection
generation also fences dev→stable→dev, not just different channel strings. Pending installation is
checked again against the current channel/expiry/digest/package/signer on-device.
If another replacement changes the installed core, the old request is rejected;
check/request again against the new semantic-version floor.
Content-addressed files prevent a later download replacing the bytes in an
installer's URI. Only individually granted APK URIs leave private storage.

The app posts a notification; tapping it, or choosing **Settings → Review
downloaded update** in Encore, offers a local
confirmation. If necessary, the user enables **Allow from this source** in Android
Settings, then chooses **Review downloaded update** again. Android's own package installer asks for final
confirmation. No Activity is forced to the foreground by a remote command.
Notification permission is optional: the Settings review action also finds the pending
request. Opening Android's installer does not mark the APK installed. If the user
cancels, the verified request stays retryable on the device while still eligible;
if it expires or is superseded, request the update again through Home Assistant.
Successful replacement can kill the process; reopen Encore and **Start satellite**
to reconnect and report the installed version from the replacement APK.
There is no automatic boot/replacement restart or installation-success claim.

Signed release builds use `com.encore.satellite.incubator` on both channels,
as approved by Umer for the first Incubator dev release. Stable is a feed choice,
not a package suffix or backend choice. Debug builds retain
`com.encore.satellite.incubator.dev`; the signed app is a separate installation
with separate data. Leave existing debug installs and their pairing/dashboard/
music state untouched. No automatic uninstall, data transfer or migration is added.
The FileProvider authority follows each build's application ID, keeping APK update
URIs separate too. Android requires the same signing identity and a higher
versionCode for updates within the chosen app ID. The current debug-signed APK
cannot be replaced in-place by a new production key. Do not adopt the publicly
known Android debug key as a production trust root.

## Build and signing pipeline

`android.yml` rebuilds the Go AAR, assembles the debug APK and runs Android unit
tests and lint on PRs/pushes without signing secrets. It also builds and inspects
the release variant with a disposable key; that APK is never uploaded. The
manual **signed candidate** dispatch uses `android-signing` and retains the
APK/digest/source as a 30-day review artifact. That path does not publish or
select a feed.

Automatic Dev uses `android-dev-signing` only after successful checks on the
exact verified commit pushed to protected `main` and only when
`automatic_dev_targets` includes `android-generic`. The shared
`release/biscuit_dev.py` selector returns the same core SemVer and first-parent
revision for Android and Biscuit, or no release for that source. For Android the
core is embedded in the APK and is also its versionName; the revision is its
deterministic versionCode. Android requires a code above historical R8 and at
most 2,100,000,000. No new ledger allocation or device qualification is needed
for Dev. Historical reservations remain evidence for older receipts.

Provision the two signing environments for protected `main` only, without
admin bypass. Keep the reviewer on the manual `android-signing` environment.
Configure unattended Dev and credentials only through the signing setup owner:

- Secrets: `ANDROID_KEYSTORE_BASE64`, `ANDROID_STORE_PASSWORD`,
  `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`. Use the same APK signer in both
  environments; do not rotate identities as part of enabling Dev.
- Set `ANDROID_CERTIFICATE_SHA256` (lowercase hex) and
  `ANDROID_METADATA_PUBLIC_KEY_PEM` (Ed25519 public key) to the same pins in
  signing, retention and activation. The candidate job derives the raw key from
  the PEM and verifies the URL and key in the signed APK manifest, which is what
  the installed updater reads.
- Set `ANDROID_UPDATE_BASE_URL` to the intended direct HTTPS feed base, without
  query or fragment. A different embedded base requires a new immutable build.
- The separate `ANDROID_METADATA_KEY_PEM` secret is for automatic Dev only if
  online custody of that Ed25519 key is authorized and provisioned. It must match
  the embedded public root; the APK keystore cannot substitute for it. Leave
  Android out of `automatic_dev_targets` until this is ready. Keep recoverable,
  access-controlled key backups.

Local release builds use `ENCORE_KEYSTORE` (absolute file path),
`ENCORE_STORE_PASSWORD`, `ENCORE_KEY_ALIAS`, `ENCORE_KEY_PASSWORD`,
`ENCORE_VERSION_CODE`, `ENCORE_VERSION_NAME`, `ENCORE_UPDATE_BASE_URL`, and
`ENCORE_UPDATE_PUBLIC_KEY` (base64 raw 32-byte Ed25519 public key, not PEM).
`ENCORE_CORE_VERSION` defaults to the historical local inventory value; CI sets
it explicitly to the shared version for the exact source.
Invoke `build-android :app:assembleRelease` with the
SDK/NDK/Java prerequisites from the device README. Missing inputs fail closed;
release signing never falls back to debug. Debug builds without trust inputs
still work, but report update trust as unprovisioned.

The Dev job performs two clean assemblies, requires byte-identical APKs and
prepares a receipt for the exact source commit with build evidence, Encore
sources, and the dependency source kit. It uploads the flat signed bundle as
`android-dev-signed-<attempt>` and `dev.jws` separately as
`android-dev-signed-feed-<attempt>`. A rerun keeps the same version and cannot
overwrite retained releases or previous attempts' artifacts.
The [shared pipeline](publishing.md) retains those bytes immutably before
advancing Dev. Never reuse a versionCode for different published bytes, even
after a withdrawal. Device qualification tests two successive retained Dev
releases: a fresh install and an OTA of the next release on each target. For
the initial Stable baseline and later promotions, explicitly select the same
qualified immutable APK; do not rebuild or auto-promote.

## Feed contract and trust lifecycle

Discovery is exactly:
`<base>/android-generic/incubator/dev.jws` or `stable.jws`. HTTPS and certificate
validation are mandatory, including redirects. ESPHome cannot supply a URL,
trust root or arbitrary installer command. The wire format is compact JWS
(RFC 7515), Ed25519 EdDSA (RFC 8037), with this exact protected header:

```json
{"alg":"EdDSA","typ":"encore-android-update+jws"}
```

The signed payload is JSON with all these fields:

| Field | Contract |
| --- | --- |
| `target`, `backend`, `channel` | `android-generic`, `incubator`, selected `dev`/`stable` |
| `application_id` | Installed package ID |
| `revision`, `version` | APK versionCode, exact versionName |
| `core_version` | Shared-core SemVer, strictly greater than installed; stable must have no prerelease |
| `source`, `toolchain` | Full 40-character Git commit, exact build/toolchain provenance |
| `min_sdk`, `abis` | Actual APK minimum SDK (≥26), supported Android ABI strings |
| `url`, `size`, `sha256` | Immutable HTTPS APK URL, exact bytes (≤256 MiB), lowercase hex digest |
| `certificate_sha256` | Lowercase hex SHA-256 of installed APK signing certificate |
| `issued`, `expires` | Unix seconds; maximum 7-day validity; future issue tolerance 5 minutes |

Inspect the APK with `apksigner verify --verbose --print-certs` and `aapt dump
badging`, bind its actual version/compatibility/digest/certificate/provenance in a
JSON file, then sign offline with `python3 release/android-feed.py metadata.json
--key /private/path/metadata-ed25519.pem --output dev.jws`. The output must not
already exist. Uploading it is a separately authorized publication, not part of
this command. Retain the signed payload, artifact and build evidence permanently.
Refreshing expiry may re-sign the same revision/digest with a later issue time.
Promotion to stable signs a separate stable entry referencing **the same APK
bytes/revision** (only if the core is not prerelease); do not rebuild it.

The APK pins one metadata root. Per-channel private checkpoints reject older
observed revisions/issue times and replacing bytes at an observed revision;
Android's installed versionCode is the cross-channel downgrade floor. Expired
metadata is rejected even for pending installs. This bounds freeze/replay but
depends on a reasonable device clock and retained app data, not trusted hardware
time. No online key fetch, automatic key rotation or remote revocation exists.
Plan an APK update signed by the existing APK key to replace/revoke the metadata
root; for a suspected compromise stop publication and use a verified manual APK
recovery. APK signer rotation is deliberately rejected by exact signer matching
until a separately tested signing-lineage policy exists. Losing/compromising the
APK signing key requires a reviewed recovery/migration; metadata cannot bypass
Android signing. Recovery is separate and explicitly reviewed; even a higher
package revision cannot authorize an older/equal core through ordinary OTA.

## Device constraints, not hardware qualification

- **Tab A7 Lite:** Samsung publishes Android 11 through 14 manuals for SM-T220
  ([support](https://www.samsung.com/sec/support/model/SM-T220NZAFKOO)). Read the
  actual unit's OS/ABI and installed signer before provisioning. On stock Android
  8+ this app needs per-source install permission and user approval. Enterprise
  policy/Samsung Auto Blocker, if present/enabled, may block sideloading. Do not
  disable shared device protections remotely.
- **Portal 2:** Meta now documents ADB support for Portal 1st/2nd gen: Settings →
  Debug → ADB Enabled, USB-C, then allow the computer on-device
  ([setup](https://developers.meta.com/vr/documentation/android-apps/portal-setup/)).
  Its older AOSP has no GMS; Meta recommends minSdk 28/targetSdk 29 and documents
  a DEFAULT launcher category
  ([app requirements](https://developers.meta.com/vr/documentation/android-apps/portal-create-app/)).
  This generic package targets 35: do not claim Portal compatibility from minSdk
  alone. Verify OS/ABI, installer/settings availability, launcher, foreground
  service, WebView and microphones on the exact unit. If its firmware lacks a
  usable package installer, verified ADB installation remains a manual recovery
  path; this implementation does not relay ADB commands from ESPHome.
- ADB installation is a privileged host action; it does not grant the installed
  app that privilege. Normal APK signing and `REQUEST_INSTALL_PACKAGES` do not
  grant `INSTALL_PACKAGES` or device-owner status.
- Android offers conditional unattended update paths on newer OS versions and
  managed device-owner/affiliated-profile-owner installations
  ([PackageInstaller](https://developer.android.com/reference/android/content/pm/PackageInstaller),
  [user-action rules](https://developer.android.com/reference/android/content/pm/PackageInstaller.SessionParams#setRequireUserAction(int))).
  They require separate provisioning/permissions/qualification. This app requests
  neither device administration nor unattended-update permission. Never promise
  silent installs on either device from these foundations.

## Gates before activating this for users

The separate signed-app identity is decided; debug installations remain untouched.
Before arming Dev, validate the protected signing inputs, matching public pins,
direct HTTPS host, release retention and dependency-source/license review. Dev
is experimental and does not require device qualification. For Stable, confirm
that each target demonstrates higher-version replacement retaining state,
user cancellation, denied install/notification permission, interrupted download,
expired/bad metadata, channel changes, restart and recovery. A fresh install of the
same retained APK also needs qualification. Check the actual package name, signer
and highest deployed versionCode before attempting replacement. Older builds with
an empty saved channel may need **Update channel → dev** selected once in Home
Assistant to write it explicitly before they can receive their first OTA; the newer
updater accepts either older representation of the default. No physical-device or
production publication result is implied by local tests. Keep 0.2.0 on hold until
Android self-update and Biscuit's separate persistent image and OTA are qualified.
