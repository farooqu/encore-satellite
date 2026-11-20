# Dependency notices and source

Encore's root MIT license covers Encore-owned material, not these dependencies.
Keep the original notices, permission texts and disclaimers with distributed
copies. Historical acknowledgements are in `ACKNOWLEDGEMENTS.md`; they are not a
binary inventory.

## Retained notice snapshots

- `Go-Dependencies-NOTICE.txt` preserves notices from the Biscuit executable's
  resolved modules, the Go standard library and the Android `golang.org/x/mobile`
  bridge. Exact linked subsets still come from the final binary/AAR build records.
- `go-qrcode-MIT.txt` preserves the license for `github.com/skip2/go-qrcode`,
  used to encode the Android dashboard sign-in QR code.
- `Android-Dependencies-NOTICE.txt` records 134 resolved Maven components and
  preserves supplied AAR/JAR notices, SLF4J's MIT text and Bouncy Castle 1.80's
  `org/bouncycastle/LICENSE.java` permission text. Some resolved components are
  metadata only. This is not a claim that every resolved component is packaged.
- `../devices/biscuit/packaging/Alpine-NOTICE.txt` preserves notices collected
  from the exact Alpine recipe inputs. The matching recipes, patches, build
  configurations and source archives must accompany a binary release separately;
  notices alone do not fulfill corresponding-source obligations.

These snapshots were prepared against source `5300cb6d0b3cefa488fd1efdce639454fe94cf8b`
and its unchanged dependency declarations. Regenerate/review them when dependencies
or toolchains change, and inspect the final package contents. Collection of named
license/notice files is not proof that every per-file attribution is covered.
Neither these files nor source retention clears the embedded audio FPGA firmware.

## OkHttp Public Suffix List preferred source

`okhttp-public-suffix-list.dat` is the unmodified preferred source of the MPL-2.0
Public Suffix List resource supplied by OkHttp 4.12.0. The governing license is
`MPL-2.0.txt`; other OkHttp code remains Apache-2.0. Upstream source:

https://github.com/square/okhttp/blob/parent-4.12.0/okhttp/src/test/resources/okhttp3/internal/publicsuffix/public_suffix_list.dat

Source SHA-256:
`e8b273972eb5a70e888bd3e7d7c5b9b04e600a59d69def9136a74d70ae6fcdd3`.
The independently regenerated, uncompressed resource matches OkHttp's packaged
`okhttp3/internal/publicsuffix/publicsuffixes.gz` exactly (gzip SHA-256
`9af43e9995ec749e7a30eec4e5a87770d7e2027847084a07cca82060eff0c791`).
Generation follows upstream's `PublicSuffixListGenerator`: discard comments and
empty lines; separate exception rules, removing their leading `!`; sort and
deduplicate each set by UTF-8 bytes; append newlines; prefix each of the normal
and exception byte strings with its four-byte big-endian length. Gzip header
differences are immaterial to the verified uncompressed bytes.

The Android Gradle assets directory includes this source alongside the notices
and license, so a recipient does not have to find a matching version elsewhere.
Biscuit packaging also carries the shared license directory. Verify that the
actual final APK/image includes these files before distribution.
