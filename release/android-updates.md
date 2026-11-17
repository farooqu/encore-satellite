# Android signing and updates — gated foundation

This builds on Android source `ec2167426d43c67afcc40b21cd5c33f2470117d7`.
Incubator only; the engine travels inside the APK. No runtime backend selector,
Assist package, OS replacement, remote shell, or unattended installer is added.
The target remains planned/release-unsupported. Nothing here publishes a feed.

## What the app does

The encrypted ESPHome API exposes **APK update**, **Update channel** (`dev` or
`stable`) and **Check for updates**. Existing installs default to `dev`; the
explicit selection persists alongside pairing/dashboard/privacy state. There is
no channel fallback. Checks run when the service is created, every six hours
while it lives, on channel selection and on ESPHome check requests. A stopped
app is not a scheduled/background updater. Failures report unknown, not up to
date. Version labels show the installed/eligible shared-core SemVer and versionCode.
Eligibility requires a strictly newer `core_version` **and** a higher versionCode;
equal/older semantic versions are never offered, even at higher package revisions.
Build metadata is ignored for precedence; prereleases follow SemVer ordering.
This uses the [shared channel contract](../docs/device-packages/update-contract.md),
including already-published eligible updates. Packaging-only changes with unchanged
core SemVer are not a “next semantic version” OTA update. The installed core is
embedded from `release/targets.json` in BuildConfig and APK manifest metadata;
the downloaded APK's actual core metadata must match its signed feed entry.

An ESPHome install command rechecks the selected channel, downloads to private
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
request. Cancel/interruption does not uninstall the old app or delete its data;
retry via ESPHome. “Verified / approve installation” does **not** mean installed.
Successful replacement can kill the process; reopen/start Encore afterward.
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

`android.yml` rebuilds the Go AAR, assembles the debug APK and runs native tests
and lint on PRs/pushes without signing secrets. Its separate manual **signed
candidate** job runs only on main, after checks, in the `android-signing`
environment. It signs a release APK, verifies the expected certificate, and
retains APK/digest/source as a 30-day Actions artifact. This is a review candidate,
not an immutable public release store. No GitHub release/feed upload is performed.

Before storing any private credential, the repository owner must configure that
environment with required reviewers and main-only deployment branches. Provision
privately in GitHub environment settings, never a chat or tracked file:

- Secrets: `ANDROID_KEYSTORE_BASE64`, `ANDROID_STORE_PASSWORD`,
  `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`.
- Variables: `ANDROID_CERTIFICATE_SHA256` (lowercase hex),
  `ANDROID_UPDATE_BASE_URL` (HTTPS), `ANDROID_UPDATE_PUBLIC_KEY` (base64 raw
  32-byte Ed25519 public key, **not** PEM/SPKI).
- Keep the corresponding Ed25519 metadata private key offline, separate from the
  CI APK signing key. Keep recoverable, access-controlled backups of both keys.

Local release builds use `ENCORE_KEYSTORE` (absolute file path),
`ENCORE_STORE_PASSWORD`, `ENCORE_KEY_ALIAS`, `ENCORE_KEY_PASSWORD`,
`ENCORE_VERSION_CODE`, `ENCORE_VERSION_NAME`, `ENCORE_UPDATE_BASE_URL`, and
`ENCORE_UPDATE_PUBLIC_KEY`. Invoke `build-android :app:assembleRelease` with the
SDK/NDK/Java prerequisites from the device README. Missing inputs fail closed;
release signing never falls back to debug. Debug builds without trust inputs
still work, but report update trust as unprovisioned.

Reserve each target's package revision centrally across all channels/variants;
it is the Android versionCode (3…2100000000), not an algorithm derived from
SemVer. Never reuse a revision for different bytes, even after failed/withdrawn
publication. A durable immutable revision ledger/cohort approval and retention
store are still publication gates, not enforced by the candidate workflow.

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
Umer must approve key custody/backup and protected credential setup, and choose
the immutable HTTPS feed/artifact host. Keep feeds disabled until those inputs are provisioned,
central release/cohort/revision/withdrawal rules and the license audit are met,
and a real device demonstrates higher-version replacement retaining state,
user cancellation, denied install/notification permission, interrupted download,
expired/bad metadata, channel changes, restart and recovery. No physical-device
or production publication result is implied by local tests.
