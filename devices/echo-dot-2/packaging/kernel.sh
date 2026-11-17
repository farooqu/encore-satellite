#!/bin/bash
# Build and retain an Encore kernel artifact from pinned upstream sources.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
HERE=$ROOT/devices/echo-dot-2/packaging
WORK=${WORK:-$ROOT/.amp/in/orb-backup/dot-kernel}
OUTROOT=${1:-$ROOT/build/echo-dot-2/kernel}
mkdir -p "$WORK" "$OUTROOT"
WORK=$(cd "$WORK" && pwd)
OUTROOT=$(cd "$OUTROOT" && pwd)
ID=$(python3 "$HERE/kernel_artifact.py" identity)
OUT=$OUTROOT/$ID
# The upstream recipe clears its intermediate trees; do not race another build.
exec 9>"$WORK/.build.lock"
flock -n 9 || { echo 'Another kernel build owns this workspace' >&2; exit 1; }
if [ -e "$OUT" ]; then
  python3 "$HERE/kernel_artifact.py" verify --directory "$OUT"
  echo "Reusing immutable kernel artifact: $OUT"
  exit 0
fi
readarray -t PINS < <(python3 - "$HERE/kernel-inputs.json" <<'PY'
import json, sys
p = json.load(open(sys.argv[1]))
for v in (p["recipe"]["repository"], p["recipe"]["commit"],
          p["toolchain"]["repository"], p["toolchain"]["commit"], p["source_date_epoch"]):
    print(v)
PY
)
export SOURCE_DATE_EPOCH=${PINS[4]} KBUILD_BUILD_VERSION=1 KCONFIG_NOTIMESTAMP=1
export KBUILD_BUILD_TIMESTAMP=$(date -u -d "@$SOURCE_DATE_EPOCH")
STAGE=$(mktemp -d "$WORK/artifact.XXXXXX")
trap 'rm -rf "$STAGE"' EXIT

checkout() {
  local path=$1 url=$2 revision=$3
  if [ ! -e "$path" ]; then
    git init -q "$path"
    git -C "$path" remote add origin "$url"
    git -C "$path" fetch --depth=1 origin "$revision"
    git -C "$path" checkout --detach FETCH_HEAD
  fi
  [ "$(git -C "$path" rev-parse HEAD)" = "$revision" ] || {
    echo "Unexpected cached source revision: $path" >&2; exit 1;
  }
  [ -z "$(git -C "$path" status --porcelain)" ] || {
    echo "Cached source is modified; inspect it before rebuilding: $path" >&2; exit 1;
  }
}

checkout "$WORK/recipe" "${PINS[0]}" "${PINS[1]}"
checkout "$WORK/toolchain" "${PINS[2]}" "${PINS[3]}"
# The recipe checks SHA256 for Amazon's source and Linux 3.18.19, applies its
# full patch series with zero fuzz, and checks the resulting config fragment.
WORK="$WORK" TOOLCHAIN="$WORK/toolchain" bash "$WORK/recipe/tools/linux/build-kernel.sh" --board biscuit -o "$STAGE"
python3 "$ROOT/tools/check_ble_kernel.py" "$STAGE/zImage-dtb.config"
PYTHONPATH="$HERE" python3 - "$STAGE/zImage-dtb" "$HERE/kernel-inputs.json" "${DL:-$WORK/dl}" <<'PY'
import hashlib, json, pathlib, sys
from package import validate_kernel
validate_kernel(pathlib.Path(sys.argv[1]).read_bytes())
for name, expected in json.load(open(sys.argv[2]))["source_sha256"].items():
    if hashlib.sha256((pathlib.Path(sys.argv[3])/name).read_bytes()).hexdigest() != expected:
        raise ValueError("source archive differs from Encore's kernel lock: " + name)
PY
# Archive the prepared corresponding source and the otherwise out-of-tree build
# inputs. Never put the vendor firmware or cross-compiler binaries in this kit.
tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner -cf - \
  -C "$WORK" src \
  -C "$WORK/recipe" tools/linux/build-kernel.sh tools/linux/kernel-bt.fragment \
    tools/linux/kernel-patches docs/dumps/biscuit-fireos6-echolocal-boot-3.18.19.config LICENSE NOTICE \
  -C "$WORK/out-bt" .config verity-keys include/generated/trapz_generated_kernel.h \
  -C "$ROOT" devices/echo-dot-2/packaging/kernel.sh devices/echo-dot-2/packaging/kernel-inputs.json \
    devices/echo-dot-2/packaging/kernel_artifact.py devices/echo-dot-2/packaging/package.py \
    devices/echo-dot-2/packaging/KERNEL-SOURCE.md tools/check_ble_kernel.py .github/workflows/dot-kernel.yml \
  | gzip -n > "$STAGE/source.tar.gz"
test "$ID" = "$(python3 "$HERE/kernel_artifact.py" identity)" || {
  echo 'Kernel build inputs changed during compilation; refusing artifact' >&2; exit 1;
}
python3 "$HERE/kernel_artifact.py" record --directory "$STAGE" --work "$WORK"
mv -T "$STAGE" "$OUT"
echo "Built Encore development kernel artifact (not hardware-qualified): $OUT"
