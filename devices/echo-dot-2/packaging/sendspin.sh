#!/bin/sh
# Standalone companion build; package.py integration is deliberately separate.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
exec python3 "$root/platforms/linuxsendspin/build.py" "$@"
