package nativeab

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/farooqu/encore-satellite/platforms/linuxupdate"
	"golang.org/x/sys/unix"
)

const Directory = "/store/encore/native-ab-v1"
const Target = "biscuit-native-ab"
const Format = "encore-biscuit-native-ab-v1"

// LayoutID is the SHA256 of Format: the boot/storage protocol, not the kernel.
const LayoutID = "6d3705ef63c857baf9b729e6090865e12b2a5cd4cbcefb64540d97b99283cb7c"

const bootBytes = 16 << 20
const MaxBundle = 2*bootBytes + linuxupdate.ARM64MaxArchive + 1<<20 // ZIP envelope
const slotOffset = 0x165                                            // within MISC sector 1

type Store struct {
	dir, booted string
	open        func(partition int, flags int) (*os.File, error)
}

func Device() Store { return Store{Directory, "/run/encore/slot", openPartition} }

func openPartition(partition int, flags int) (*os.File, error) {
	names := map[int]string{8: "misc", 10: "boot_a", 11: "boot_b"}
	name, ok := names[partition]
	if !ok {
		return nil, errors.New("partition outside native A/B allowlist")
	}
	node := fmt.Sprintf("mmcblk0p%d", partition)
	sys := "/sys/class/block/" + node + "/"
	uevent, err := os.ReadFile(sys + "uevent")
	if err != nil || !strings.Contains(strings.ToLower("\n"+string(uevent)), "\npartname="+name+"\n") {
		return nil, fmt.Errorf("wrong GPT role for %s", node)
	}
	if partition != 8 {
		start, _ := os.ReadFile(sys + "start")
		size, _ := os.ReadFile(sys + "size")
		if strings.TrimSpace(string(start)) != strconv.Itoa(163840+(partition-10)*32768) || strings.TrimSpace(string(size)) != "32768" {
			return nil, fmt.Errorf("wrong boot-store geometry for %s", node)
		}
	}
	dev, _ := os.ReadFile(sys + "dev")
	fd, err := unix.Open("/dev/"+node, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFBLK ||
		strings.TrimSpace(string(dev)) != fmt.Sprintf("%d:%d", unix.Major(st.Rdev), unix.Minor(st.Rdev)) {
		unix.Close(fd)
		return nil, fmt.Errorf("unexpected block device for %s", node)
	}
	return os.NewFile(uintptr(fd), node), nil
}

func (s Store) InstalledHighWater(installed linuxupdate.Manifest) (int64, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, "highest-revision"))
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n < installed.Revision {
		return 0, errors.New("invalid native A/B high-water mark")
	}
	return n, nil
}

func (s Store) running() (int, error) {
	b, err := os.ReadFile(s.booted)
	if err == nil {
		switch strings.TrimSpace(string(b)) {
		case "a":
			return 0, nil
		case "b":
			return 1, nil
		}
	}
	return 0, errors.New("missing native boot slot identity")
}

func controlSector(f *os.File) ([512]byte, error) {
	var sector [512]byte
	var command [32]byte
	if _, err := f.ReadAt(command[:], 0); err != nil || command != [32]byte{} {
		return sector, errors.New("unexpected MISC command; normal boot required")
	}
	if _, err := f.ReadAt(sector[:], 512); err != nil || !bytes.Equal(sector[slotOffset-5:slotOffset], []byte("\x00ABB\x01")) {
		return sector, errors.New("invalid native boot control record")
	}
	return sector, nil
}

func publishSector(f *os.File, before, after [512]byte) error {
	if live, err := controlSector(f); err != nil || live != before {
		return errors.New("boot control changed before activation")
	}
	if _, err := f.WriteAt(after[:], 512); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if live, err := controlSector(f); err != nil || live != after {
		return errors.New("boot control write could not be verified; recover before retrying")
	}
	return nil
}

// Trial reads hardware state for the slot identified by this initramfs. A
// failed image may have no tries left by the time LK hands it off.
func (s Store) Trial() (bool, error) {
	return s.health(false)
}

func (s Store) Confirm() error {
	_, err := s.health(true)
	return err
}

func (s Store) health(confirm bool) (bool, error) {
	active, err := s.running()
	if err != nil {
		return false, err
	}
	flags := os.O_RDONLY
	if confirm {
		flags = os.O_RDWR
	}
	f, err := s.open(8, flags)
	if err != nil {
		return false, err
	}
	defer f.Close()
	b, err := controlSector(f)
	if err != nil {
		return false, err
	}
	current, peer := b[slotOffset+active], b[slotOffset+1-active]
	if current&128 != 0 && current&15 != 0 {
		if peer&0xf0 != 0 && peer&15 > current&15 {
			return false, errors.New("different native slot selected; reboot required")
		}
		return false, nil
	}
	if peer != 0x8e || (current != 0x1f && current != 0x0f) {
		return false, errors.New("running boot is not the armed native trial")
	}
	if !confirm {
		return true, nil
	}
	after := b
	after[slotOffset+active] = 0x8f
	return true, publishSector(f, b, after)
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func stageFile(directory, name string, src io.Reader, size int64) error {
	f, err := os.CreateTemp(directory, ".native-ab-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	n, err := io.Copy(f, io.LimitReader(src, size+1))
	if err == nil && n != size {
		err = errors.New("native OTA entry length changed")
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(directory, name)); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func bootPrefix(f *os.File, n int64) (string, error) {
	h := sha256.New()
	if _, err := io.CopyN(h, io.NewSectionReader(f, 0, n), n); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Install treats boot control as the sole activation record. It disables the
// inactive slot before replacing either member, so a partial old fallback
// cannot be selected while its boot/runtime pair is being overwritten.
func (s Store) Install(m linuxupdate.Manifest, key ed25519.PublicKey, channel, proof, downloaded string) error {
	lock, err := os.OpenFile(filepath.Join(s.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	r, err := linuxupdate.VerifyRelease([]byte(proof), key, m, channel)
	if err != nil || m.Target != Target || m.Format != Format || m.BaseSHA256 != LayoutID {
		return errors.New("invalid native A/B release proof")
	}
	highest, err := s.InstalledHighWater(m)
	if err != nil || r.Revision <= highest || r.Size > MaxBundle || !linuxupdate.Newer(r.CoreVersion, m.CoreVersion) {
		return errors.New("native A/B release not newer than installed or attempted")
	}
	if err := r.Fresh(); err != nil {
		return err
	}
	bundle, err := os.Open(downloaded)
	if err != nil {
		return err
	}
	defer bundle.Close()
	hash, err := bootPrefix(bundle, r.Size)
	st, statErr := bundle.Stat()
	if err != nil || statErr != nil || !st.Mode().IsRegular() || st.Size() != r.Size || hash != r.SHA256 {
		return errors.New("native A/B bundle no longer matches signed metadata")
	}
	z, err := zip.NewReader(bundle, r.Size)
	if err != nil || len(z.File) != 3 {
		return errors.New("expected exactly two boot images and one runtime")
	}
	files := map[string]*zip.File{}
	for _, asset := range z.File {
		max := uint64(bootBytes)
		switch asset.Name {
		case "boot_a.img", "boot_b.img":
		case "rootfs.tar.gz":
			max = linuxupdate.ARM64MaxArchive
		default:
			return errors.New("unexpected native A/B bundle member")
		}
		if files[asset.Name] != nil || !asset.Mode().IsRegular() || asset.UncompressedSize64 == 0 || asset.UncompressedSize64 > max {
			return errors.New("invalid native A/B bundle member")
		}
		files[asset.Name] = asset
	}
	active, err := s.running()
	if err != nil {
		return err
	}
	if trial, err := s.Trial(); err != nil || trial {
		return errors.New("running native slot is not confirmed")
	}
	next := 1 - active
	letter := string("ab"[next])
	asset := files["boot_"+letter+".img"]
	img, err := asset.Open()
	if err != nil {
		return err
	}
	image, err := io.ReadAll(io.LimitReader(img, bootBytes+1))
	img.Close()
	if err != nil || uint64(len(image)) != asset.UncompressedSize64 || len(image) < 2048 || !bytes.HasPrefix(image, []byte("ANDROID!")) {
		return errors.New("invalid native boot image")
	}
	misc, err := s.open(8, os.O_RDWR)
	if err != nil {
		return err
	}
	defer misc.Close()
	before, err := controlSector(misc)
	if err != nil {
		return err
	}
	if before[slotOffset+active]&128 == 0 || before[slotOffset+active]&15 == 0 ||
		(before[slotOffset+next]&0xf0 != 0 && before[slotOffset+next]&15 > before[slotOffset+active]&15) {
		return errors.New("MISC no longer selects the confirmed running slot")
	}
	disabled := before
	disabled[slotOffset+next] = 0
	if disabled != before {
		if err := publishSector(misc, before, disabled); err != nil {
			return err
		}
	}
	root := files["rootfs.tar.gz"]
	src, err := root.Open()
	if err != nil {
		return err
	}
	directory := filepath.Join(s.dir, "slots", letter)
	rootHash := sha256.New()
	err = stageFile(directory, "rootfs.tar.gz", io.TeeReader(src, rootHash), int64(root.UncompressedSize64))
	src.Close()
	if err != nil {
		return err
	}
	staged, err := os.Open(filepath.Join(directory, "rootfs.tar.gz"))
	if err != nil {
		return err
	}
	actual, err := bootPrefix(staged, int64(root.UncompressedSize64))
	staged.Close()
	if err != nil || actual != hex.EncodeToString(rootHash.Sum(nil)) {
		return errors.New("inactive runtime readback failed")
	}
	boot, err := s.open(10+next, os.O_RDWR)
	if err != nil {
		return err
	}
	defer boot.Close()
	if _, err = boot.WriteAt(image, 0); err != nil {
		return err
	}
	if err = boot.Sync(); err != nil {
		return err
	}
	if got, err := bootPrefix(boot, int64(len(image))); err != nil || got != fmt.Sprintf("%x", sha256.Sum256(image)) {
		return errors.New("inactive boot store readback failed")
	}
	if err := r.Fresh(); err != nil {
		return err
	}
	revision := fmt.Sprintf("%d\n", r.Revision)
	if err := stageFile(s.dir, "highest-revision", strings.NewReader(revision), int64(len(revision))); err != nil {
		return err
	}
	armed := disabled
	armed[slotOffset+active], armed[slotOffset+next] = 0x8e, 0x2f
	return publishSector(misc, disabled, armed)
}
