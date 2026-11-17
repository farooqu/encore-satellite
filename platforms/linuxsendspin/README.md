# Official Sendspin companion

Linux-only Go process bridge and native C++20 adapter for the official
[`sendspin-cpp`](https://github.com/Sendspin/sendspin-cpp/tree/a4a9b65fb356657e3a39a2ab70b4aae957bf53e0)
SDK. No lab receiver code, HTTP sink, ALSA ownership, discovery, guest admission,
or plaintext fallback is included. Only player/controller roles are enabled;
the only advertised format is PCM16 little-endian, 48 kHz, stereo.

## Build and redistribution

Required host tools: Linux x86-64 for the pinned ARM cross toolchain, Python 3.12+
(tar extraction filters), Git, CMake 3.20+, Ninja, and `readelf`.
The host build additionally requires a native C/C++20 compiler and development
libc. No Docker daemon, target sysroot, target base changes, or kernel compiler
is needed.

```sh
devices/biscuit/packaging/sendspin.sh --target host --out build/sendspin-host
devices/biscuit/packaging/sendspin.sh --target armv7
```

ARM output defaults to `build/biscuit/sendspin/`:

* `encore-sendspin`: stripped, **static**, ARMv7 Cortex-A7 EABI5 hard-float ELF.
* `provenance.json`: schema 1, target, exact dependency lock, configure command,
  production integration input SHA256s, and artifact SHA256s. `compilers.C` and
  `compilers.CXX` record CMake's configured compiler path, ID/version, a generated
  compile command (including compiler arguments/target flags), and the cached
  launcher setting in CMake-list syntax. `compiler` summarizes the configured
  C++ compiler, not the shell's default `c++`.
* `dependencies.lock.json`: SDK and transitive source Git commit pins plus Zig
  archive URL/SHA256. No floating dependency tags are used by this builder.
* `notices/`: licenses/notices collected from the locked sources and runtimes.
* `source.tar.gz`: dependency sources, adapter/bridge/build sources, and bundled
  musl/libc++/compiler runtime sources for redistribution alongside the binary.
  This is a redistribution kit, not something to unpack into the device rootfs.

`--cache PATH`, `--out PATH`, and `--jobs N` are supported. The default cache is
`.amp/in/orb-backup/sendspin-build-cache`. The builder rejects modified or
untracked dependency trees. CMake receives explicit local dependency paths and
disconnected FetchContent settings. Source archives have normalized timestamps,
owners and gzip headers. Binary paths are prefix-mapped, and ARM debug data is
stripped. Production inputs are hashed before the build and checked again at
completion; a concurrent edit fails the build rather than blessing stale code.
Configured compiler identity/version comes from CMake's toolchains File API,
including when a nondefault compiler is retained in an existing CMake cache.
Host compilers are recorded, not automatically installed/pinned. Each ARM build
extracts the checksum-verified archive afresh before replacing the extracted
Zig cache; modified executables, runtime sources, and extra files in an old
extracted tree are never reused. An exclusive per-cache file lock serializes
the entire build through source-kit copying and provenance, so another builder
cannot replace a toolchain still in use. Symlink aliases of a cache share the
same lock. The persistent `.build.lock` file must not be removed during a build.

The ARM userspace toolchain is checksum-pinned Zig 0.14.1 (Clang 19.1.7,
musl/libc++), explicitly targeting **`arm-linux.3.18-musleabihf`** with
`-mcpu=cortex_a7`. It does not use Zig's default Linux 4.19 target. The companion
checks `clock_gettime(CLOCK_MONOTONIC_RAW)` at startup; Linux has provided this
clock since 2.6.28. `getrandom`, used to provision identity and pairing secrets,
requires Linux 3.17+. QEMU user-mode checks do not replace a real Dot/kernel
test.

Licenses: SDK, micro-flac and micro-ogg-demuxer Apache-2.0; ArduinoJson MIT;
IXWebSocket BSD-3-Clause; noise-c MIT with bundled crypto notices; musl/Zig MIT;
LLVM libc++/libc++abi/libunwind Apache-2.0 with LLVM exception and per-file
notices. The SDK still builds its FLAC decoder dependency although FLAC is not
advertised. Opus, TLS, and zlib are disabled. Complete source/license records
are in the bundle; no third-party source is checked into this platform package.

The source kit can be used without Git by passing its `sources/*` directories as
`SDK_SOURCE` and the four `FETCHCONTENT_SOURCE_DIR_*` CMake variables (see the
recorded configure command), with `FETCHCONTENT_FULLY_DISCONNECTED=ON` and the
included `integration/platforms/linuxsendspin/native` project. Download the
checksum-locked compiler archive from the lock file for an ARM rebuild; set
`ZIG_EXE` and use `native/armv7-musl.cmake`. The kit contains the runtime source,
not a copy of the compiler executable.

## Go API and lifecycle

```go
err := linuxsendspin.Run(ctx, linuxsendspin.Options{
    Name: "Kitchen Echo", URL: "ws://music-server:8928/sendspin",
    StateDir: "/var/lib/encore/sendspin", Executable: "/usr/bin/encore-sendspin",
    Output: musicOutput, Commands: commands,
})
```

`Command` has `Play` and `Stop`; closing `Commands` disables commands, not the
receiver. Controller commands are best-effort SDK requests subject to server
supported-command/active-role checks. Stop/privacy must also be enforced locally
by the mixer. `Run` never retries; the host owns retry/backoff and configuration.

`Output` is exactly:

```go
type Output interface {
    MusicStart(epoch uint64, played func(frames uint32, timestampUS int64), failed ...func())
    MusicWrite(ctx context.Context, epoch uint64, pcm []byte) error
    MusicEnd(epoch uint64)
    MusicVolume(volume uint8, muted bool)
}
```

Start/End/Volume are bounded operations (short mixer locking/nonblocking device
operations are fine). Start and End must fence old queued PCM and callbacks.
`Run` supplies one nonblocking `failed` callback. Invoke it when the output
loses its consumption clock (for example an underrun or uncertain shared-DMA
drop). This terminates that child/stream; the host can reconnect with a fresh
epoch. Never replay uncertain frames or count discarded frames as consumed.
Callbacks retained from an older epoch cannot terminate a newer stream.
Epochs passed to Output monotonically increase across **all Run restarts in the
same Go process**, not just within a child. `MusicWrite` honors context and
returns nil only on whole-block bounded acceptance. It must never return an
error after accepting any part of the block. PCM is immutable for the call;
copy it into the output's bounded queue before returning. The callback reports
**actually consumed music frames**, not writes or voice frames. Its timestamp is
the last consumed music frame's completion time, in **CLOCK_MONOTONIC_RAW
microseconds**, not Unix or Go monotonic time. Discarded frames earn no progress.

`Run` cancellation closes the socket, cancels writes, fences music with End,
kills/reaps the child (2-second reap bound), and joins its workers. It never
closes/drains/kills voice. Shutdown relies on Output honoring its stated
cancellation/bounded-lifecycle contract. The child environment is reduced to
`LANG=C`/`LC_ALL=C`; no host credential environment is forwarded. SDK stdout and
stderr are not forwarded, to prevent future upstream secret logging.

## IPC v1

One inherited **AF_UNIX SOCK_SEQPACKET fd 3**, no filesystem socket or listener.
All packets have a 32-byte little-endian header:

| Offset | Field |
|---|---|
| 0 | `ESPN` magic (4 bytes) |
| 4 | version u16 = 1 |
| 6 | kind u16 |
| 8 | child stream generation u64 |
| 16 | request ID / timestamp u64 |
| 24 | payload byte count u32 |
| 28 | value u32 |

Kinds: 1 Start, 2 End, 3 Volume (`id`=mute 0/1), 4 PCM (`id`=request,
`value`=acceptance deadline milliseconds), 5 Accepted (`value`=bytes, 0 for
ordinary backpressure), 6 Played (`value`=frames, `id`=raw timestamp), 7 Command
(`value`=1 Play / 2 Stop). Maximum accepted packet is 16 KiB PCM + header; native
writes are at most **480 frames / 1920 bytes (10 ms)** each. The SDK callback
accepts into a four-block native queue (at most 40 ms including the block in
flight), waiting no longer than the SDK timeout when full and returning zero
on backpressure. A dedicated IPC worker, independent of SDK main-loop fsync,
has only one Go acceptance request in flight. Go gets a 10 ms acceptance budget;
zero acceptance retains/retries the same native-owned block without dropping
or duplicating PCM. A missing/ambiguous IPC ACK has a separate 250 ms watchdog
and fails closed. Lifecycle events fence and discard the bounded native queue.

Lifecycle sends are nonblocking. All Go sends go through a bounded 32-packet
queue and a deadline-limited writer; the PCM worker has a one-entry queue.
Output callbacks only accumulate bounded per-epoch feedback and wake a worker;
they do not perform socket IO. Stale feedback closures are inert. Old wire
generations are discarded natively; future/invalid generations, malformed or
truncated packets, queue saturation, output faults, and lost critical state fail
closed. Fixed exit codes: 70 state/startup/other failure, 71 socket send failure,
72 acceptance ACK deadline lost, 73 negotiated format mismatch, 74 malformed IPC.
Run errors retain child exit status without forwarding SDK logs.

## Pairing and revocation (maintenance only)

Stop the receiver/supervisor first. These commands take the same nonblocking
exclusive file lock as `run`; they refuse to operate while a companion is active.

```sh
build/sendspin-host/encore-sendspin pairing-token --state /private/sendspin
build/sendspin-host/encore-sendspin revoke-all --state /private/sendspin
```

On the device use `/usr/local/bin/encore-sendspin` with its configured StateDir.
`pairing-token` explicitly emits **only the token and newline to stdout**. Treat
that output as a secret; do not put it in shell arguments, environment, logs,
URLs, source kits or committed configuration. Supply it to the server's pairing
UI. The URL is always `ws://HOST:PORT/sendspin` without userinfo, query or fragment;
Noise authentication is mandatory despite `ws://` transport (no TLS dependency).
Unpaired/guest role admission is explicitly disabled. The adapter exposes no
inbound WebSocket server or dynamic/static pairing-code channel.

`revoke-all` atomically removes all long-term server records and last-played
state **and rotates the Pairing PSK**, revoking previously printed tokens, while
preserving the X25519 private identity/client ID. It does not require network
access or use SDK private internals: only public persistence key/codec layouts.
It intentionally clears output-delay/recency metadata too.

StateDir must be owned by the effective user with mode 0700, not a symlink; its
parent must already exist. State is an owner-only 0600 snapshot with an
accidental-corruption checksum. Updates write/fsync a temporary, rename, then
fsync the directory. First provisioning also fsyncs the parent directory.
An initialized marker prevents a missing snapshot from silently generating a
replacement identity. Invalid snapshots fail before SDK startup. Any rejected
trust/state write terminates the process rather than allowing ignored SDK
persistence failure. The checksum is **not** protection against a malicious
state owner, disk rollback, or deleting the entire directory; protect/back up
the directory as device credentials. Fail-closed fsync behavior assumes the
storage operation eventually returns; Run cancellation still forcibly kills a
stalled child without waiting for SDK shutdown.

## Checks

```sh
python3 platforms/linuxsendspin/test_build.py -v
go test -race ./platforms/linuxsendspin
go vet ./platforms/linuxsendspin
python3 platforms/linuxsendspin/test_native.py --binary build/sendspin-host/encore-sendspin -v
python3 platforms/linuxsendspin/test_native.py --binary build/biscuit/sendspin/encore-sendspin --runner qemu-arm -v
```

Builder tests use disposable local caches without downloads. They cover tampered
Zig executables/runtime sources, injected files, symlinked extracted trees,
checksum rejection, full-build cache locking/release (including cache aliases
and build failures), and real CMake configure/build cycles with nondefault cached
C/C++ wrappers, compiler arguments, and launchers.

Go tests exercise framing/URL rejection, malformed/oversized packets, child
termination/cancellation, blocked writes, ordinary zero-acceptance backpressure,
queue saturation, raw-clock consumption feedback and stale epochs across child
restarts. Native tests use the real SDK for cold/restart identity, secret
rotation/stable ID, corruption/deletion rejection, failed persistence,
maintenance locks, invalid IPC, and parent socket closure. Test-generated
credentials live only in ephemeral `/dev/shm` directories and are removed.
An additional disposable authenticated MA check exercised the current Go mixer
with a clocked virtual PCM device for 20 seconds: 924,232 music frames accepted,
919,081 consumed, 844,469 nonzero rendered frames; it ended on intentional context
deadline rather than IPC failure. This is not physical Dot ALSA/kernel validation.
