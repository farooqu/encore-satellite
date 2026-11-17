"""Verify the optional TSL2540 patch against an extracted, pinned Amazon tree.

Only a private copy is patched. No download, device access or recipe integration.
"""

import argparse
import hashlib
from pathlib import Path
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[3]
PINNED = {
    "drivers/misc/ams_tsl2540.c": "7fb81e524e4a1b30862c93bd1eb216ace515e5a834262d662fad969683e301a0",
    "drivers/misc/ams_tsl2540_als.c": "250d8c237cf9d32813c82df30256d762278206b7d2a372561370422176511ca6",
    "include/linux/i2c/ams_tsl2540.h": "dc02dd8a48654840db1aa2c4f9639b139be1d128d5672ef5c8416b0c828deeb1",
    "drivers/misc/ams_tsl2540_als.h": "3d7f74de5b2d8dc39324b8f1153c6d264ee94f3cdc254f220e294205fcac7838",
    "drivers/misc/ams_i2c.h": "134c6a970759f3c31f2ef846a39a9b5aec863e5067da9324130a5186c7cbc347",
}


def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path, help="unmodified kernel/mediatek/mt8163/3.18_hl")
    parser.add_argument("--m32", action="store_true", help="also require 32-bit host execution (gcc-multilib)")
    args = parser.parse_args()
    scratch = ROOT / ".amp/in/orb-backup"
    scratch.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="tsl2540-check-", dir=scratch) as tmp:
        work = Path(tmp)
        tree = work / "src"
        for name, digest in PINNED.items():
            data = (args.source / name).read_bytes()
            if hashlib.sha256(data).hexdigest() != digest:
                raise ValueError("not the pinned unmodified Amazon source: " + name)
            target = tree / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        shim = work / "shim/linux"
        shim.mkdir(parents=True)
        for name in ("types", "mutex", "workqueue", "wait", "input", "i2c", "slab", "math64", "ktime",
                     "kernel", "delay", "string", "unistd", "interrupt", "platform_device", "pm",
                     "module", "device", "uaccess", "kthread", "freezer", "of", "of_platform", "of_device"):
            (shim / (name + ".h")).write_text('#include "kernel-shim.h"\n')

        def compile_and_run(name, *defines, m32=False, lifecycle=False, arguments=()):
            binary = work / name
            run("cc", "-std=gnu11", "-g", "-O1", "-pthread", "-fsanitize=undefined",
                "-fno-sanitize-recover=all", "-DCONFIG_AMZN_AMS_ALS",
                "-DCONFIG_AMS_ADJUST_WITH_BASELINE", *defines,
                *(["-DCONFIG_OF", "-include", str(HERE / "lifecycle-shim.h")] if lifecycle else []),
                *(["-m32", "-DHOST32"] if m32 else []),
                "-I" + str(HERE), "-I" + str(shim.parent), "-I" + str(tree / "include"),
                "-I" + str(tree / "drivers/misc"),
                *([str(HERE / "lifecycle-test.c"), str(tree / "drivers/misc/ams_tsl2540_als.c")]
                  if lifecycle else [str(HERE / "measurement-test.c")]),
                "-o", str(binary))
            return subprocess.run([str(binary), *arguments], text=True, capture_output=True, timeout=30)

        def apply_patch(name):
            patch = str(HERE / name)
            run("patch", "-d", str(tree), "-p1", "--fuzz=0", "--dry-run", "-i", patch)
            run("patch", "-d", str(tree), "-p1", "--fuzz=0", "-i", patch)

        def expect_failure(result, message):
            if result.returncode != 1 or message not in result.stderr:
                raise RuntimeError("unexpected regression result: " + result.stdout + result.stderr)
            print("BASELINE REPRODUCED:", result.stderr.strip())

        baseline = compile_and_run("baseline", "-DBASELINE")
        expect_failure(baseline, "FAIL: failed bus read must not return cached lux")
        apply_patch("0001-measurement-abi.patch")
        expect_failure(compile_and_run("old-flush", arguments=("flush-regression",)),
                       "FAIL: gain tail flush must propagate its bus error")
        expect_failure(compile_and_run("old-lifecycle", lifecycle=True),
                       "FAIL: published sysfs groups survive driver teardown")
        apply_patch("0002-teardown-and-write-errors.patch")
        for m32 in ([False, True] if args.m32 else [False]):
            result = compile_and_run("patched32" if m32 else "patched", m32=m32)
            print(("32-bit: " if m32 else "Native: ") + result.stdout, end="")
            if result.returncode:
                raise RuntimeError("patched harness failed: " + result.stderr)
            for flags in ((), ("-DTSL2540_ENABLE_INPUT",), ("-DTSL2540_ENABLE_INTERRUPT",),
                          ("-DTSL2540_ENABLE_INPUT", "-DTSL2540_ENABLE_INTERRUPT")):
                result = compile_and_run("lifecycle", *flags, m32=m32, lifecycle=True)
                print(("32-bit " if m32 else "Native ") + str(flags) + ": " + result.stdout, end="")
                if result.returncode:
                    raise RuntimeError("lifecycle harness failed: " + result.stderr)
        print("Source/harness evidence only; Dot lux remains deferred pending exact-image hardware qualification.")


if __name__ == "__main__":
    main()
