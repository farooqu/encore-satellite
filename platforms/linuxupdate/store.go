package linuxupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Store operates only on application slots. Recovery and Misc are read-only
// gates, never flash targets. Paths are supplied by the Linux device package.
type Store struct{ Directory, Booted, Recovery, Misc string }

func read(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e == nil && int64(len(b)) > limit {
		e = errors.New("oversized update input")
	}
	return b, e
}
func text(path string) (string, error) {
	b, e := read(path, 4096)
	return strings.TrimSpace(string(b)), e
}
func hash(r io.Reader) (string, error) {
	h := sha256.New()
	_, e := io.Copy(h, r)
	return hex.EncodeToString(h.Sum(nil)), e
}
func (s Store) Inspect() (Manifest, int64, error) {
	var m Manifest
	format, e := text(filepath.Join(s.Directory, "format"))
	if e != nil {
		return m, 0, e
	}
	a, e := text(filepath.Join(s.Directory, "active"))
	if e != nil || (a != "a" && a != "b") {
		return m, 0, errors.New("invalid active slot")
	}
	b, e := text(s.Booted)
	if e != nil || a != b {
		return m, 0, errors.New("active slot differs from running slot; reboot/recovery required")
	}
	state, e := text(filepath.Join(s.Directory, "slots", a+".state"))
	if e != nil || state != "good" {
		return m, 0, errors.New("running slot has not passed health checks")
	}
	data, e := read(filepath.Join(s.Directory, "slots", a, "etc/encore-package.json"), 32768)
	if e != nil {
		return m, 0, e
	}
	if e = json.Unmarshal(data, &m); e != nil {
		return m, 0, e
	}
	if m.Schema != 1 || m.Format != format || !Channel(m.Channel) || !validCore(m.CoreVersion) || m.Revision < 1 || !digest.MatchString(m.BaseSHA256) {
		return m, 0, errors.New("invalid installed provenance")
	}
	v, e := text(filepath.Join(s.Directory, "highest-revision"))
	if e != nil {
		return m, 0, e
	}
	highest, e := strconv.ParseInt(v, 10, 64)
	if e != nil || highest < m.Revision {
		return m, 0, errors.New("invalid highest-attempted revision")
	}
	for _, name := range []string{"linux", "twrp"} {
		b, e := read(filepath.Join(s.Directory, name+".img"), 16<<20)
		if e != nil {
			return m, 0, e
		}
		want, e := text(filepath.Join(s.Directory, name+".img.sha256"))
		fields := strings.Fields(want)
		got, _ := hash(bytes.NewReader(b))
		if e != nil || len(b) == 0 || len(fields) == 0 || got != fields[0] {
			return m, 0, fmt.Errorf("saved %s recovery image is damaged", name)
		}
		if name == "linux" {
			f, e := os.Open(s.Recovery)
			if e != nil {
				return m, 0, e
			}
			live, e := hash(io.LimitReader(f, int64(len(b))))
			f.Close()
			if e != nil || live != got {
				return m, 0, errors.New("live recovery differs from saved Linux image")
			}
		}
	}
	f, e := os.Open(s.Misc)
	if e != nil {
		return m, 0, e
	}
	command := make([]byte, 32)
	_, e = io.ReadFull(f, command)
	f.Close()
	if e != nil || !bytes.Equal(command, append([]byte("boot-recovery"), make([]byte, 19)...)) {
		return m, 0, errors.New("MISC does not select recovery; physical recovery repair required")
	}
	return m, highest, nil
}

// Staged owns the same lock as the USB installer until Close. Its zero value
// cannot activate. A stale lock after process death fails closed for recovery.
type Staged struct {
	store          Store
	release        Release
	installed      Manifest
	active, target string
	closed         bool
}

func (s *Staged) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return os.Remove(filepath.Join(s.store.Directory, ".install-lock"))
}

func matches(r Release, m Manifest) bool {
	return m.Schema == 1 && r.Target == m.Target && r.Backend == m.Backend && r.Format == m.Format && r.CoreVersion == m.CoreVersion && r.Revision == m.Revision && r.BaseSHA256 == m.BaseSHA256 && Channel(m.Channel)
}

// Stage rehashes the signed artifact and validates its contents before replacing
// the inactive slot. The running good slot, kernel and recovery stay untouched.
func (s Store) Stage(ctx context.Context, r Release, bundle string) (_ *Staged, err error) {
	lock := filepath.Join(s.Directory, ".install-lock")
	if err = os.Mkdir(lock, 0700); err != nil {
		return nil, errors.New("installer locked; inspect unfinished update before retrying")
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(lock)
		}
	}()
	i, highest, err := s.Inspect()
	if err != nil {
		return nil, err
	}
	if r.Target != i.Target || r.Backend != i.Backend || r.Format != i.Format || r.BaseSHA256 != i.BaseSHA256 || !Channel(r.Channel) || r.Revision <= highest || !Newer(r.CoreVersion, i.CoreVersion) {
		return nil, errors.New("update no longer eligible")
	}
	if err = r.Fresh(); err != nil {
		return nil, err
	}
	f, err := os.Open(bundle)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	got, err := hash(f)
	if err != nil || st.Size() != r.Size || got != r.SHA256 {
		return nil, errors.New("staged bundle digest mismatch")
	}
	z, err := zip.NewReader(f, st.Size())
	if err != nil {
		return nil, err
	}
	files := map[string]*zip.File{}
	for _, entry := range z.File {
		if files[entry.Name] != nil || entry.UncompressedSize64 > MaxBundle {
			return nil, errors.New("duplicate or oversized bundle member")
		}
		files[entry.Name] = entry
	}
	if len(files) != 5 || files["manifest.json"] == nil {
		return nil, errors.New("invalid bundle members")
	}
	stream, err := files["manifest.json"].Open()
	if err != nil {
		return nil, err
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(stream, 32769))
	stream.Close()
	if err != nil || len(manifestBytes) > 32768 {
		return nil, errors.New("invalid bundle manifest")
	}
	var m Manifest
	if err = json.Unmarshal(manifestBytes, &m); err != nil {
		return nil, err
	}
	if !matches(r, m) {
		return nil, errors.New("signed feed and bundle manifest differ")
	}
	if len(m.Files) != 4 || len(m.Base) != 4 {
		return nil, errors.New("incomplete bundle identity")
	}
	// The builder uses Python's sorted JSON with spaces for this identity.
	keys := []string{"initramfs_sha256", "kernel_inputs_sha256", "kernel_sha256", "userspace_sha256"}
	var parts []string
	for _, k := range keys {
		v := m.Base[k]
		if !digest.MatchString(v) {
			return nil, errors.New("invalid shared base identity")
		}
		parts = append(parts, fmt.Sprintf("%q: %q", k, v))
	}
	baseHash, _ := hash(strings.NewReader("{" + strings.Join(parts, ", ") + "}"))
	if baseHash != i.BaseSHA256 {
		return nil, errors.New("bundle shared base identity mismatch")
	}
	for _, name := range []string{"rootfs.tar.gz", "initramfs.gz", "kernel.zImage-dtb", "kernel.json"} {
		entry := files[name]
		if entry == nil {
			return nil, errors.New("missing bundle payload")
		}
		v, e := entry.Open()
		if e != nil {
			return nil, e
		}
		sum, e := hash(v)
		v.Close()
		if e != nil || !digest.MatchString(m.Files[name]) || sum != m.Files[name] {
			return nil, errors.New("payload digest mismatch")
		}
	}
	if m.Files["initramfs.gz"] != m.Base["initramfs_sha256"] || m.Files["kernel.zImage-dtb"] != m.Base["kernel_sha256"] {
		return nil, errors.New("bundle attempts kernel/initramfs replacement")
	}
	stage, err := os.MkdirTemp(s.Directory, ".ota-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	root, err := files["rootfs.tar.gz"].Open()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = extract(ctx, root, stage); err != nil {
		return nil, err
	}
	if err = os.Chmod(stage, 0755); err != nil {
		return nil, err
	}
	for _, name := range []string{"sbin", "etc", "usr", "usr/local", "usr/local/bin"} {
		info, e := os.Lstat(filepath.Join(stage, name))
		if e != nil || !info.IsDir() {
			return nil, errors.New("unsafe rootfs control directory")
		}
	}
	for _, name := range []string{"usr/local/bin/encore-satellite", "etc/inittab", "etc/encore-base"} {
		info, e := os.Lstat(filepath.Join(stage, name))
		if e != nil || !info.Mode().IsRegular() || (name == "usr/local/bin/encore-satellite" && info.Mode()&0111 == 0) {
			return nil, errors.New("incomplete application rootfs")
		}
	}
	if _, err = os.Lstat(filepath.Join(stage, "sbin/init")); err != nil {
		return nil, errors.New("missing rootfs init")
	}
	base, err := text(filepath.Join(stage, "etc/encore-base"))
	if err != nil || base != i.Format {
		return nil, errors.New("rootfs format mismatch")
	}
	for _, name := range []string{"etc/encore-base", "etc/encore-package.json"} {
		if info, e := os.Lstat(filepath.Join(stage, name)); e == nil && !info.Mode().IsRegular() {
			return nil, errors.New("unsafe rootfs identity path")
		}
	}
	if err = atomicWrite(filepath.Join(stage, "etc/encore-package.json"), manifestBytes); err != nil {
		return nil, err
	}
	if err = syncTree(stage); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	a, err := text(filepath.Join(s.Directory, "active"))
	if err != nil {
		return nil, err
	}
	target := "b"
	if a == "b" {
		target = "a"
	}
	// Reserve before any fallback mutation. Failed staging/trials never free a
	// revision for reuse, including after rollback to the older good slot.
	if err = atomicWrite(filepath.Join(s.Directory, "highest-revision"), []byte(strconv.FormatInt(r.Revision, 10)+"\n")); err != nil {
		return nil, err
	}
	if err = atomicWrite(filepath.Join(s.Directory, "slots", target+".state"), []byte("bad\n")); err != nil {
		return nil, err
	}
	dest := filepath.Join(s.Directory, "slots", target)
	if err = os.RemoveAll(dest); err != nil {
		return nil, err
	}
	if err = os.Rename(stage, dest); err != nil {
		return nil, err
	}
	if err = syncDir(filepath.Dir(dest)); err != nil {
		return nil, err
	}
	keep = true
	return &Staged{store: s, release: r, installed: i, active: a, target: target}, nil
}

// Activate must be called under the host's desired-channel generation fence.
// It only changes application slot selection. The existing initramfs owns trial
// budgets, health commitment and fallback on subsequent boots.
func (s *Staged) Activate() error {
	if s == nil || s.closed || s.target == "" {
		return errors.New("no staged update")
	}
	if err := s.release.Fresh(); err != nil {
		return err
	}
	i, highest, err := s.store.Inspect()
	if err != nil {
		return err
	}
	if i.Target != s.installed.Target || i.Backend != s.installed.Backend || i.CoreVersion != s.installed.CoreVersion || i.Revision != s.installed.Revision || i.BaseSHA256 != s.installed.BaseSHA256 || highest != s.release.Revision {
		return errors.New("installed identity or high-water changed during staging")
	}
	a, err := text(filepath.Join(s.store.Directory, "active"))
	if err != nil || a != s.active {
		return errors.New("active slot changed")
	}
	if err = atomicWrite(filepath.Join(s.store.Directory, "slots", s.target+".state"), []byte("trial 2\n")); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.store.Directory, "active"), []byte(s.target+"\n"))
}

func extract(ctx context.Context, src io.Reader, dest string) error {
	g, e := gzip.NewReader(src)
	if e != nil {
		return e
	}
	defer g.Close()
	t := tar.NewReader(g)
	seen := map[string]bool{}
	links := map[string]bool{}
	var total int64
	for count := 0; ; count++ {
		if e = ctx.Err(); e != nil {
			return e
		}
		h, e := t.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		n := h.Name
		// Python tarfile emits a trailing slash for directories. Normalize
		// exactly that convention before checking aliases and symlink parents.
		if h.Typeflag == tar.TypeDir {
			n = strings.TrimSuffix(n, "/")
		}
		if n == "" || n == "." || path.Clean(n) != n || path.IsAbs(n) || strings.Contains(n, "\\") || n == ".." || strings.HasPrefix(n, "../") || seen[n] || count > 100000 {
			return errors.New("unsafe or duplicate rootfs path")
		}
		seen[n] = true
		for p := path.Dir(n); p != "."; p = path.Dir(p) {
			if links[p] {
				return errors.New("rootfs path below symlink")
			}
		}
		p := filepath.Join(dest, filepath.FromSlash(n))
		if e = os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			return e
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if e = os.MkdirAll(p, os.FileMode(h.Mode)&0777); e != nil {
				return e
			}
			if e = os.Chmod(p, os.FileMode(h.Mode)&0777); e != nil {
				return e
			}
		case tar.TypeSymlink:
			links[n] = true
			if e = os.Symlink(h.Linkname, p); e != nil {
				return e
			}
		case tar.TypeReg:
			total += h.Size
			if h.Size < 0 || total > 512<<20 {
				return errors.New("oversized rootfs")
			}
			f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0777)
			if e != nil {
				return e
			}
			_, e = io.Copy(f, t)
			if e == nil {
				e = f.Chmod(os.FileMode(h.Mode) & 0777)
			}
			if e == nil {
				e = f.Sync()
			}
			ce := f.Close()
			if e != nil {
				return e
			}
			if ce != nil {
				return ce
			}
		default:
			return errors.New("rootfs hardlinks and special files forbidden")
		}
	}
	// Python tarfile pads to a 10 KiB record. Accept bounded zero padding only,
	// consuming the gzip trailer to check CRC rather than stopping at tar EOF.
	padding, e := io.ReadAll(io.LimitReader(g, 10241))
	if e != nil {
		return e
	}
	if len(padding) > 10240 || len(bytes.Trim(padding, "\x00")) != 0 {
		return errors.New("trailing rootfs data")
	}
	return nil
}
func syncTree(root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return syncDir(p)
		}
		return nil
	})
}
