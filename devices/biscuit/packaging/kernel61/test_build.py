"""The experimental payload cannot be mistaken for a release kernel."""
import struct
import sys
from pathlib import Path
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from kernel61.build import validate_payload
from kernel61.arm64_probe import image_header, native_elf
from package import arm_elf, validate_kernel


class PayloadTest(unittest.TestCase):
    def test_experimental_framing_does_not_relax_release_validator(self):
        kernel = bytearray(97)
        struct.pack_into("<I", kernel, 36, 0x016F2818)
        dtb = struct.pack(">II", 0xD00DFEED, 43) + bytes(32) + b"abc"
        payload = kernel + dtb.ljust(65536, b"\0")
        validate_payload(kernel, dtb, payload)
        with self.assertRaisesRegex(ValueError, "two complete appended device trees"):
            validate_kernel(payload)
        for k, d, p in ((kernel, dtb, payload[:-1]),
                        (kernel, dtb, payload + dtb),
                        (kernel, dtb[:-1], payload),
                        (b"ANDROID!" + kernel, dtb, payload),
                        (kernel, dtb, kernel + dtb)):
            with self.subTest(lengths=(len(k), len(d), len(p))):
                with self.assertRaises(ValueError):
                    validate_payload(k, d, p)

    def test_arm64_probe_requires_native_bounded_4k_image(self):
        image = bytearray(97)
        image[56:60] = b"ARM\x64"
        struct.pack_into("<3Q", image, 8, 0x80000, 0x300000, 10)
        self.assertEqual(image_header(image), {
            "text_offset": 0x80000, "image_size": 0x300000,
            "flags": 10, "alignment": 0x200000})
        for size, flags in ((0, 10), (96, 10), (0x300000, 11), (0x300000, 12)):
            invalid = bytearray(image)
            struct.pack_into("<2Q", invalid, 16, size, flags)
            with self.subTest(size=size, flags=flags), self.assertRaises(ValueError):
                image_header(invalid)
        for invalid in (image[:63], b"\x1f\x8b" + image, bytes(97)):
            with self.assertRaises(ValueError):
                image_header(invalid)
        # The public release validator must not accept an ARM64 probe payload.
        with self.assertRaises(ValueError):
            validate_kernel(image)

    def test_native_helpers_do_not_relax_arm32_release_elf_contract(self):
        elf = bytearray(120)
        elf[:6] = b"\x7fELF\x02\x01"
        struct.pack_into("<H", elf, 18, 183)
        struct.pack_into("<Q", elf, 32, 64)
        struct.pack_into("<HH", elf, 54, 56, 1)
        struct.pack_into("<I", elf, 64, 1)  # PT_LOAD, no dynamic interpreter.
        native_elf(elf, static=True)
        with self.assertRaises(ValueError):
            arm_elf(elf, static=True)
        struct.pack_into("<I", elf, 64, 3)  # PT_INTERP must be rejected.
        with self.assertRaises(ValueError):
            native_elf(elf, static=True)
        native_elf(elf)
        for data in (elf[:63], elf[:100]):
            with self.assertRaises(ValueError):
                native_elf(data, static=True)


if __name__ == "__main__":
    unittest.main()
