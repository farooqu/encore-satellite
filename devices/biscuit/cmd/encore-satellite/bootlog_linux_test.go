package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootLogKeepsLateOutputAcrossBoots(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app")
	logFile := func(boot, file string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, boot, file))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	run := func(boot, input string) string {
		var live bytes.Buffer
		records := make(chan string, 60)
		readServiceLog(strings.NewReader(input), &live, records)
		close(records)
		keepBootLog(records, dir, []byte(boot), time.Millisecond, func() bool { return true })
		return live.String()
	}
	input := strings.Repeat("ordinary output that fills the RAM log\n", 4096) + "late failure before unplug\n"
	if live := run("old boot", input); live != input[:128<<10] {
		t.Fatalf("RAM output did not stop at the usual cap: %d bytes", len(live))
	}
	run("old boot", "more output without reboot\n")
	run("new boot", "subsequent startup\n")
	if id, old := logFile("previous", "boot-id"), logFile("previous", "log"); id != "old boot" ||
		!strings.Contains(old, "late failure before unplug\n") || !strings.Contains(old, "more output without reboot\n") ||
		strings.Contains(old, "subsequent startup") {
		t.Fatalf("wrong previous boot or missing late output: id=%q", id)
	}
	if current := logFile("current", "log"); !strings.Contains(current, "subsequent startup\n") || strings.Contains(current, "late failure") {
		t.Fatalf("boot histories mixed: %q", current)
	}
}

func TestBootLogBoundsHistoryAndKeepsLastNonEmptyBoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wifi")
	if err := beginBootLog(dir, []byte("old boot")); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"oldest", "older", "latest"} {
		if err := appendBootLog(filepath.Join(dir, "current"), marker+strings.Repeat("x", 40<<10)); err != nil {
			t.Fatal(err)
		}
	}
	for _, boot := range []string{"empty boot", "new boot"} {
		if err := beginBootLog(dir, []byte(boot)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "previous"))
	if err != nil || len(entries) != 3 {
		t.Fatalf("unexpected previous boot files: %v, %v", entries, err)
	}
	for file, start := range map[string]string{"log.1": "older", "log": "latest"} {
		b, err := os.ReadFile(filepath.Join(dir, "previous", file))
		if err != nil || len(b) > 64<<10 || !bytes.HasPrefix(b, []byte(start)) {
			t.Fatalf("%s did not retain its bounded segment, len=%d, err=%v", file, len(b), err)
		}
	}
}
