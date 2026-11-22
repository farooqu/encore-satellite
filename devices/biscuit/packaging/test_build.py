"""The experimental payload cannot be mistaken for a release kernel."""
import struct
import unittest

from arm64_probe import image_header, native_elf
from package import arm_elf, validate_kernel


class PayloadTest(unittest.TestCase):
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
