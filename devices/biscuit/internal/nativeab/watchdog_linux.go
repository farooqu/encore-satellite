package nativeab

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// GuardTrial runs from the initramfs before mounting or extracting the new
// runtime. Keep the /run/encore descriptor across switch_root so confirmation
// is visible from the old root even after its mounts have moved away.
func GuardTrial() error {
	if trial, err := Device().Trial(); err != nil || !trial {
		return errors.New("watchdog requires an unconfirmed native slot")
	}
	state, err := os.Open("/run/encore")
	if err != nil {
		return err
	}
	defer state.Close()
	const class = "/sys/class/watchdog/watchdog0"
	parent, _ := filepath.EvalSymlinks(class + "/device")
	driver, _ := filepath.EvalSymlinks(class + "/device/driver")
	number, err := os.ReadFile(class + "/dev")
	if err != nil || filepath.Base(parent) != "10007000.toprgu" || driver != "/sys/bus/platform/drivers/mtk-wdt" {
		return errors.New("expected the built-in Biscuit MediaTek watchdog")
	}
	fd, err := unix.Open("/dev/watchdog0", unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFCHR ||
		strings.TrimSpace(string(number)) != fmt.Sprintf("%d:%d", unix.Major(st.Rdev), unix.Minor(st.Rdev)) {
		unix.Close(fd)
		return errors.New("watchdog device identity changed")
	}
	park := func() {
		// Keep the descriptor open without keepalives. Exiting would let the
		// Linux watchdog core take over servicing an unexpected close.
		for {
			_ = unix.Pause()
		}
	}
	const setTimeout = 0xc0045706 // _IOWR('W', 6, int) on arm64
	pretimeout, preErr := unix.IoctlGetInt(fd, unix.WDIOC_GETPRETIMEOUT)
	setErr := unix.IoctlSetPointerInt(fd, setTimeout, 25)
	timeout, getErr := unix.IoctlGetInt(fd, unix.WDIOC_GETTIMEOUT)
	if preErr != nil || setErr != nil || getErr != nil || pretimeout != 0 || timeout != 25 {
		park()
	}
	mark, err := unix.Openat(int(state.Fd()), "native-watchdog-armed", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, 0600)
	if err != nil {
		park()
	}
	unix.Close(mark)
	deadline := time.Now().Add(230 * time.Second)
	for time.Now().Before(deadline) {
		var confirmed unix.Stat_t
		if unix.Fstatat(int(state.Fd()), "native-confirmed", &confirmed, unix.AT_SYMLINK_NOFOLLOW) == nil && confirmed.Mode&unix.S_IFMT == unix.S_IFREG {
			if err := unix.IoctlSetPointerInt(fd, unix.WDIOC_SETOPTIONS, 1); err == nil { // WDIOS_DISABLECARD
				return unix.Close(fd)
			}
			park() // an already-confirmed slot can boot again with the guard off
		}
		if _, err := unix.Write(fd, []byte{0}); err != nil {
			park()
		}
		time.Sleep(5 * time.Second)
	}
	park()
	return nil
}
