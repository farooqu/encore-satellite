package main

import (
	"strconv"
	"testing"
	"unsafe"
)

func TestNativeWMTIoctlABI(t *testing.T) {
	// WMT_unlocked_ioctl uses _IOW(0xa0, nr, char*) for pointer
	// commands, but _IOW(0xa0, nr, int) for STP mode and patch count.
	want := map[int][2]uintptr{
		32: {0x4004a004, 0x4004a00f},
		64: {0x4008a004, 0x4008a00f},
	}[strconv.IntSize]
	if setPatchName != want[0] || setPatchInfo != want[1] ||
		setSTPMode != 0x4004a005 || setPatchNum != 0x4004a00e {
		t.Fatalf("WMT ioctl ABI: name=%#x info=%#x mode=%#x count=%#x",
			setPatchName, setPatchInfo, setSTPMode, setPatchNum)
	}
	if unsafe.Sizeof(patchInfo{}) != 264 {
		t.Fatal("WMT patch descriptor must remain 264 bytes on both ABIs")
	}
}
