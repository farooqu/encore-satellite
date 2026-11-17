#!/usr/bin/env python3
"""Builder regressions; uses disposable caches, no downloads or device access."""
import fcntl
import io
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock

import build


class Scratch(unittest.TestCase):
    def setUp(self):
        scratch = build.ROOT / ".amp/in/orb-backup"
        scratch.mkdir(parents=True, exist_ok=True)
        self.tmp = tempfile.TemporaryDirectory(prefix="sendspin-builder-test-", dir=scratch)
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)


class CacheLock(Scratch):
    def test_entire_build_is_locked_and_failure_releases_persistent_lock(self):
        cache = self.root / "cache"
        cache.mkdir()
        alias = self.root / "alias"
        alias.symlink_to(cache, target_is_directory=True)
        lock = cache / ".build.lock"

        def work(args, resolved_cache, out):
            self.assertEqual(resolved_cache, cache)
            # An independent open (as in a second invocation) cannot acquire
            # the lock while any of build_locked's work is executing.
            with lock.open("a") as other:
                with self.assertRaises(BlockingIOError):
                    fcntl.flock(other, fcntl.LOCK_EX | fcntl.LOCK_NB)
            if fail:
                raise RuntimeError("build failed")

        for fail in (False, True):
            with self.subTest(failure=fail), mock.patch(
                    "sys.argv", ["build.py", "--cache", str(alias), "--out", str(self.root / "out")]), mock.patch.object(
                    build, "build_locked", side_effect=work) as delegated:
                if fail:
                    with self.assertRaisesRegex(RuntimeError, "build failed"):
                        build.main()
                else:
                    build.main()
                delegated.assert_called_once()
            self.assertTrue(lock.exists(), "never unlink the shared lock inode")
            with lock.open("a") as other:
                fcntl.flock(other, fcntl.LOCK_EX | fcntl.LOCK_NB)


class VerifiedToolchain(Scratch):
    def setUp(self):
        super().setUp()
        self.cache = self.root / "cache"
        self.cache.mkdir()
        self.files = {"zig": b"#!/bin/sh\nprintf 'verified-toolchain\\n'\n",
                      "lib/libc/musl/COPYRIGHT": b"verified runtime notice\n"}
        with tarfile.open(self.cache / "zig.tar.xz", "w:xz") as tar:
            for name, data in self.files.items():
                info = tarfile.TarInfo("zig-test/" + name)
                info.size = len(data)
                info.mode = 0o755 if name == "zig" else 0o644
                tar.addfile(info, io.BytesIO(data))
        self.spec = {"directory": "zig-test", "url": "must-not-download",
                     "sha256": build.sha(self.cache / "zig.tar.xz")}

    def test_tampered_executable_runtime_and_extra_files_are_replaced(self):
        zig = build.prepare_zig(self.cache, self.spec)
        (zig / "zig").write_text("#!/bin/sh\nexit 91\n")
        (zig / "lib/libc/musl/COPYRIGHT").write_text("tampered notice")
        (zig / "lib/libc/musl/injected").write_text("not in archive")
        zig = build.prepare_zig(self.cache, self.spec)
        self.assertEqual(build.output(zig / "zig"), "verified-toolchain")
        self.assertFalse((zig / "lib/libc/musl/injected").exists())
        # Exercise the same source-kit copy helper used by the production build.
        kit = self.root / "source.tar"
        with tarfile.open(kit, "w") as tar:
            build.add_tree(tar, zig / "lib/libc/musl", "toolchain/lib/libc/musl")
        with tarfile.open(kit) as tar:
            self.assertEqual(tar.extractfile("toolchain/lib/libc/musl/COPYRIGHT").read(),
                             self.files["lib/libc/musl/COPYRIGHT"])

    def test_symlinked_extracted_cache_is_not_followed(self):
        external = self.root / "external"
        external.mkdir()
        (external / "zig").write_text("do not touch")
        (self.cache / "zig-test").symlink_to(external, target_is_directory=True)
        zig = build.prepare_zig(self.cache, self.spec)
        self.assertFalse(zig.is_symlink())
        self.assertEqual(build.output(zig / "zig"), "verified-toolchain")
        self.assertEqual((external / "zig").read_text(), "do not touch")

    def test_bad_archive_fails_before_using_existing_tree(self):
        zig = build.prepare_zig(self.cache, self.spec)
        (zig / "zig").write_text("do not execute")
        with (self.cache / "zig.tar.xz").open("ab") as raw:
            raw.write(b"corruption")
        with self.assertRaisesRegex(RuntimeError, "checksum mismatch"):
            build.prepare_zig(self.cache, self.spec)
        self.assertEqual((zig / "zig").read_text(), "do not execute")


@unittest.skipUnless(all(shutil.which(x) for x in ("cmake", "ninja", "gcc", "g++")),
                     "requires CMake, Ninja and GCC C/C++")
class ConfiguredCompilers(Scratch):
    def test_cached_nondefault_compilers_arguments_and_launchers(self):
        source = self.root / "src"
        source.mkdir()
        (source / "CMakeLists.txt").write_text(
            "cmake_minimum_required(VERSION 3.20)\nproject(probe LANGUAGES C CXX)\n"
            "add_library(probe STATIC probe.c probe.cpp)\n")
        (source / "probe.c").write_text("int c_probe(void) { return 0; }\n")
        (source / "probe.cpp").write_text("int cxx_probe() { return 0; }\n")
        compilers = {}
        for language, compiler in (("C", "gcc"), ("CXX", "g++")):
            wrapper = self.root / ("nondefault-" + language)
            wrapper.write_text('#!/bin/sh\nexec ' + shlex.quote(shutil.which(compiler)) + ' "$@"\n')
            wrapper.chmod(0o755)
            compilers[language] = wrapper
        binary = self.root / "build"
        query = binary / ".cmake/api/v1/query/toolchains-v1"
        query.parent.mkdir(parents=True)
        query.touch()
        command = ["cmake", "-S", str(source), "-B", str(binary), "-G", "Ninja",
                   "-DCMAKE_EXPORT_COMPILE_COMMANDS=ON"]
        for language, compiler in compilers.items():
            command += [f"-DCMAKE_{language}_COMPILER={compiler}",
                        f"-DCMAKE_{language}_COMPILER_ARG1=-DARG1_{language}=1",
                        f"-DCMAKE_{language}_COMPILER_LAUNCHER={shutil.which('cmake')};-E;env;PROBE=1"]
        subprocess.run(command, check=True, capture_output=True)
        # The configured cache must win over misleading shell/environment defaults.
        subprocess.run(command[:8], check=True, capture_output=True,
                       env={**os.environ, "CC": "/bin/false", "CXX": "/bin/false"})
        subprocess.run(["cmake", "--build", str(binary), "--parallel", "1"],
                       check=True, capture_output=True)
        records = build.compiler_provenance(binary)
        self.assertEqual(set(records), {"C", "CXX"})
        for language, record in records.items():
            self.assertEqual(record["path"], str(compilers[language]))
            self.assertEqual(record["id"], "GNU")
            self.assertTrue(record["version"])
            command = record["compile_command"]["command"]
            self.assertIn(str(compilers[language]), command)
            self.assertIn(f"-DARG1_{language}=1", command)
            self.assertEqual(record["launcher"], f"{shutil.which('cmake')};-E;env;PROBE=1")
        # Remains JSON serializable for provenance.json.
        json.dumps(records)


if __name__ == "__main__":
    unittest.main()
