# Shared device update controls

Android and Linux use the same user-facing channel policy and ESPHome controls;
they do not share an installer or acquire each other's privileges. This contract
does not turn either planned target into a supported release.

## Persistent desired channel, not installed provenance

Expose one encrypted ESPHome select entity with object ID `update_channel`, name
**Update channel**, options exactly `dev`, `stable`, and config entity category.
Persist a successful explicit selection in device-private configuration before
acknowledging it. Invalid values and persistence failure leave the old selection
intact. Android's existing installs default to `dev`; Linux must initialize from
the recorded installed channel (current development packages are `dev`). Never
derive the desired channel from the app's version suffix on every boot.

Keep the installed package's immutable target/backend/channel/revision provenance
separate from the desired channel. A change from dev to stable, or stable to dev,
changes only which channel is checked. Clear stale availability and pending
authorization from the previous channel and request a check of the new channel.
**Selection never installs, replaces, restarts, downgrades, or falls back**.
In-flight work must recheck selection before offering or activating its result.
After a restart, report the saved desired channel, not the old installed channel.

## Available updates and explicit requests

Expose an ESPHome update entity (`apk_update` on Android, `linux_update` on Linux),
plus **Check for updates** button `check_updates`.
An update check uses only the selected target/backend/channel signed feed.
The device normally checks on startup and every six hours while running,
matching ESPHome's managed HTTP update cadence; Home Assistant does not schedule
these checks. Android may report a restored install attempt in place of its
startup check. A verified offer may carry Markdown release notes in the signed
`release_summary` field. Home Assistant shows them inline and links to the full
changelog for an Encore GitHub release. Older feeds without the field still work;
no extra changelog fetch is required by the device.
Report actual installed version/revision and latest eligible version/revision;
include revision in displayed version strings to identify the exact package.
No eligible upgrade reports current==latest; network/trust/compatibility errors
report unknown/missing state with an actionable reason, not false “up to date”.

ESPHome's update CHECK command and check button discover only. Its UPDATE command
requests the platform's download/installation flow and revalidates metadata,
selection, artifact authenticity, compatibility and monotonic revision. It does
not bypass Android consent or Linux's privileged installer/recovery gates. A
download, staged package, pending user confirmation and successful activation
are distinct states; never report one as the other.

## Eligibility: next newer semantic version, never a downgrade

Umer confirmed that “next” includes an eligible already-published or later
release on the selected channel; there is no switch-time watermark. Compare the
candidate's signed `core_version` against the installed package's immutable
`core_version` using SemVer precedence: it must be **strictly greater**. Numeric
components and prerelease identifiers follow SemVer, not lexical string ordering;
build metadata does not make an equal version newer. Stable has no prerelease.

No semantic-version downgrades are allowed in either direction. Equal/older core
versions at a higher package revision are not eligible for ordinary OTA. This
also means packaging-only revisions of an unchanged core do not appear as a
“next semantic version” update. Android embeds the central inventory core version
in both BuildConfig and APK metadata; Linux records it in its installed package
manifest. Do not use app display suffixes or package revision as a SemVer proxy.

Revision checks are **additional** safety: Android must exceed installed
versionCode. Linux must additionally exceed its highest attempted
target revision, including failed trial boots, and preserve its installed base,
healthy-slot and recovery constraints. Those stricter platform safety gates remain
in place when channels differ. No backend replacement or base migration is an
ordinary channel update. There is no automatic fallback when a feed is absent.

For example: installed dev core `1.3.0-dev.4`, revision 20 → select stable core
`1.2.0`, revision 25: wait, do not downgrade despite the higher revision. Stable
core `1.3.0`, revision 26 is eligible when present. Installed stable core `1.3.0`,
revision 26 → select dev already at core `1.4.0-dev.1`, revision 27: report it
available, but do not install until a separate update request.

Stable entries cannot advertise prerelease shared-core versions. Promotion reuses
the same immutable APK/package bytes and revision, with a separately signed
channel entry; it is not a rebuild. Reserve revisions across channels/variants,
never reset them on a switch. Recovery is a separately reviewed mechanism; a
higher revision alone cannot bypass this ordinary OTA semantic-version floor.

## Platform ownership and delivery state

- Android owns JWS verification, HTTPS discovery/download, APK identity/signer
  checks and locally approved Android installation. See
  [Android signing and updates](../../release/android-updates.md).
- Linux/Dot owns desired-channel configuration and management integration,
  package signatures/feed verification, privileged inactive-slot staging,
  high-water/trial-health/recovery behavior and installation. Its existing USB/ADB
  development installer is **not** a signed OTA implementation. Experimental Linux
  signed OTA source is separately implemented, not included by the Android work;
  consult the Linux target's documentation and verification for its integration
  state. Neither source implementation establishes publication or hardware qualification.
- Neither installer may fetch a new trust key or command from an unauthenticated
  response. Credentials, publication, shared production changes and real hardware
  provisioning require their own explicit approval and verification.
