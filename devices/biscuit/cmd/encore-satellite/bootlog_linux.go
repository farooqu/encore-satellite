package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Four sources, two segments per source, two boots: at most 1 MiB of logs.
const bootLogSegment = 64 << 10

func captureBootLog(ctx context.Context, source string) {
	switch source {
	case "app", "wifi", "dhcp", "system":
	default:
		return
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || len(bytes.TrimSpace(boot)) != 36 {
		return // The shell drains service output if capture cannot start.
	}
	records := make(chan string, 60) // At most one segment of bounded records per batch.
	done := make(chan struct{})
	go func() {
		defer close(done)
		keepBootLog(records, filepath.Join("/data/encore/diagnostics", source), bytes.TrimSpace(boot), 5*time.Second, func() bool {
			_, err := os.Stat("/run/encore/base-ready")
			return err == nil
		})
	}()
	if source == "system" {
		readSystemLog(ctx, records)
	} else {
		readServiceLog(os.Stdin, os.Stdout, records)
	}
	close(records)
	<-done
}

func queueBootLog(records chan string, line string) {
	record := time.Now().UTC().Format(time.RFC3339) + " " + line + "\n"
	select {
	case records <- record:
		return
	default:
	}
	// A stuck disk must not slow the pipe drainer. Keep the newest evidence.
	select {
	case <-records:
	default:
	}
	records <- record // One producer; the disk worker only removes records.
}

func readServiceLog(in io.Reader, live io.Writer, records chan string) {
	r := bufio.NewReaderSize(in, 1024)
	remaining, continued := 128<<10, false
	for {
		line, err := r.ReadSlice('\n')
		if n := min(remaining, len(line)); n > 0 {
			_, _ = live.Write(line[:n]) // Preserve the existing bounded RAM log.
			remaining -= n
		}
		if !continued && len(line) != 0 {
			text := string(bytes.TrimRight(line, "\r\n"))
			if err == bufio.ErrBufferFull {
				text += " [truncated]"
			}
			queueBootLog(records, text)
		}
		continued = err == bufio.ErrBufferFull
		if err != nil && !continued {
			return
		}
	}
}

func keepBootLog(records <-chan string, dir string, boot []byte, interval time.Duration, ready func() bool) {
	t := time.NewTicker(interval)
	defer t.Stop()
	initialized := false
	var batch strings.Builder
	for range t.C {
		batch.Reset()
		closed := false
	read:
		for range cap(records) {
			select {
			case record, ok := <-records:
				if !ok {
					closed = true
					break read
				}
				batch.WriteString(record)
			default:
				break read
			}
		}
		if ready() {
			if !initialized {
				initialized = beginBootLog(dir, boot) == nil
			}
			if initialized && batch.Len() != 0 {
				_ = appendBootLog(filepath.Join(dir, "current"), batch.String())
			}
		}
		if closed {
			return
		}
	}
}

func beginBootLog(dir string, boot []byte) error {
	current := filepath.Join(dir, "current")
	previous := filepath.Join(dir, "previous")
	id := filepath.Join(current, "boot-id")
	if old, err := os.ReadFile(id); err == nil && bytes.Equal(old, boot) {
		return nil
	}
	if _, err := os.Lstat(current); err == nil {
		// Don't evict useful evidence for an interrupted, empty initialization.
		_, active := os.Stat(filepath.Join(current, "log"))
		_, rotated := os.Stat(filepath.Join(current, "log.1"))
		if active == nil || rotated == nil {
			if err = os.RemoveAll(previous); err != nil {
				return err
			}
			if err = os.Rename(current, previous); err != nil {
				return err
			}
		} else if err = os.RemoveAll(current); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(current, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(id, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(boot); err != nil {
		return err
	}
	return f.Sync()
}

func appendBootLog(dir, batch string) error {
	path := filepath.Join(dir, "log")
	info, err := os.Stat(path)
	newFile := errors.Is(err, os.ErrNotExist)
	if err != nil && !newFile {
		return err
	}
	if !newFile && info.Size()+int64(len(batch)) > bootLogSegment {
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
		newFile = true
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.WriteString(batch); err != nil {
		return err
	}
	if err := f.Sync(); err != nil || !newFile {
		return err
	}
	for _, name := range []string{dir, filepath.Dir(dir)} {
		d, err := os.Open(name)
		if err != nil {
			return err
		}
		err = d.Sync()
		d.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func readSystemLog(ctx context.Context, records chan string) {
	fd, err := unix.Open("/dev/kmsg", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == nil {
		defer unix.Close(fd)
	}
	var buf [8192]byte
	seen := make(map[string]string)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if fd >= 0 {
			for range 256 {
				n, err := unix.Read(fd, buf[:])
				if err == unix.EPIPE { // Kernel ring wrapped; continue from the oldest surviving message.
					continue
				}
				if err != nil || n == 0 {
					break
				}
				line, _, _ := bytes.Cut(buf[:n], []byte{'\n'})
				pri, _, _ := bytes.Cut(line, []byte{','})
				if p, err := strconv.Atoi(string(pri)); err == nil && p >= 0 && p <= 4 {
					queueBootLog(records, "kernel "+string(line[:min(len(line), 1000)]))
				}
			}
		}
		for _, service := range [...]string{"base", "radio", "wifi", "dhcp", "bluetooth", "network", "app", "enrollment", "health"} {
			if raw, err := os.ReadFile(filepath.Join("/run/encore/services", service, "exit-status")); err == nil {
				if status, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && (status != 0 || service == "app" || service == "wifi" || service == "dhcp") {
					message := fmt.Sprintf("%s exited status=%d", service, status)
					if message != seen[service] {
						seen[service] = message
						queueBootLog(records, message)
					}
				}
			}
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}
