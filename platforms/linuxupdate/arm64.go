package linuxupdate

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

const ARM64MaxArchive = 96 << 20

func BiscuitARM64Identity(core string, revision int64) Manifest {
	return Manifest{Schema: 1, Target: "biscuit-arm64", Backend: "incubator", Format: "encore-biscuit-arm64-rootfs-v1",
		Channel: "stable", CoreVersion: core, Revision: revision,
		BaseSHA256: "a8386b120851db6bf30cd6a2a354d605d3c76ea83e62b18c5549079a9b373fbe"}
}

func BiscuitARM64Root() ed25519.PublicKey {
	key, _ := base64.StdEncoding.DecodeString("Ggx1WlubkJjSPNpKoaEDzPZYMzUxbiPunioZcmvxsuc=")
	return key
}

type ARM64Store struct{ Directory, Booted string }

func (s ARM64Store) highest() (int64, error) {
	b, err := text(filepath.Join(s.Directory, "highest-revision"))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(b, 10, 64)
}

func (s ARM64Store) InstalledHighWater(installed Manifest) (int64, error) {
	highest, err := s.highest()
	if err != nil || highest < installed.Revision {
		return 0, errors.New("invalid ARM64 update high-water mark")
	}
	return highest, nil
}

func verifyARM64Archive(path, digest string, size int64) error {
	stat, err := os.Lstat(path)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() < 1 || stat.Size() > ARM64MaxArchive || (size > 0 && stat.Size() != size) {
		return errors.New("invalid ARM64 rootfs file or size")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if actual, err := hash(io.LimitReader(f, ARM64MaxArchive+1)); err != nil || actual != digest {
		return errors.New("ARM64 rootfs digest mismatch")
	}
	return nil
}

// VerifyBoot authenticates a slot before init unpacks it. The factory archive
// is pinned by the locally approved recovery image, not by removable storage.
// Known-good rollback can boot below high-water; staging can never do so.
func (s ARM64Store) VerifyBoot(slot, seedDigest string, seed Manifest, key ed25519.PublicKey) (Manifest, error) {
	if slot != "a" && slot != "b" || !validCore(seed.CoreVersion) || seed.Revision < 1 {
		return Manifest{}, errors.New("invalid ARM64 boot policy")
	}
	highest, err := s.InstalledHighWater(seed)
	if err != nil {
		return Manifest{}, err
	}
	dir := filepath.Join(s.Directory, "slots", slot)
	archive := filepath.Join(dir, "rootfs.tar.gz")
	proof, err := read(filepath.Join(dir, "release.jws"), 32768)
	if errors.Is(err, os.ErrNotExist) {
		return seed, verifyARM64Archive(archive, seedDigest, 0)
	}
	if err != nil {
		return Manifest{}, err
	}
	r, err := VerifyRelease(proof, key, seed, seed.Channel)
	if err != nil || r.Revision <= seed.Revision || r.Revision > highest || !Newer(r.CoreVersion, seed.CoreVersion) {
		return Manifest{}, errors.New("untrusted ARM64 slot release")
	}
	seed.CoreVersion, seed.Revision = r.CoreVersion, r.Revision
	return seed, verifyARM64Archive(archive, r.SHA256, r.Size)
}

// Install writes only the inactive ARM64 slot. The selector is the commit
// point; prior errors can burn a revision, but cannot select a partial archive.
func (s ARM64Store) Install(installed Manifest, key ed25519.PublicKey, proof, downloaded string) error {
	lock := filepath.Join(s.Directory, ".install-lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		return fmt.Errorf("ARM64 store locked: %w", err)
	}
	defer os.Remove(lock)
	r, err := VerifyRelease([]byte(proof), key, installed, installed.Channel)
	if err != nil {
		return err
	}
	highest, err := s.InstalledHighWater(installed)
	if err != nil || r.Revision <= highest || !Newer(r.CoreVersion, installed.CoreVersion) || r.Size > ARM64MaxArchive {
		return errors.New("ARM64 release is not eligible for staging")
	}
	booted, err := text(s.Booted)
	if err != nil || booted != "a" && booted != "b" {
		return errors.New("invalid running ARM64 slot")
	}
	active, err := text(filepath.Join(s.Directory, "active"))
	if err != nil || active != booted {
		return errors.New("ARM64 selector differs from running slot")
	}
	slots := filepath.Join(s.Directory, "slots")
	if state, err := text(filepath.Join(slots, booted+".state")); err != nil || state != "good" {
		return errors.New("running ARM64 slot must pass health before updating")
	}
	next := "b"
	if booted == "b" {
		next = "a"
	}
	dir := filepath.Join(slots, next)
	f, err := os.CreateTemp(dir, ".rootfs-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	src, err := os.Open(downloaded)
	if err != nil {
		f.Close()
		return err
	}
	_, err = io.Copy(f, io.LimitReader(src, r.Size+1))
	src.Close()
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	if err = verifyARM64Archive(f.Name(), r.SHA256, r.Size); err != nil {
		return err
	}
	if err = r.Fresh(); err != nil {
		return err
	}
	state := filepath.Join(slots, next+".state")
	if err = atomicWrite(state, []byte("bad\n")); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, "rootfs.tar.gz")); err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(dir, "release.jws"), []byte(proof)); err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(s.Directory, "highest-revision"), []byte(fmt.Sprintf("%d\n", r.Revision))); err != nil {
		return err
	}
	if err = atomicWrite(state, []byte("update\n")); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.Directory, "active"), []byte(next+"\n"))
}
