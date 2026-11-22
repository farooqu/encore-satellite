package mobile

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
)

func TestAndroidOffersPrimaryMonitorOnlyAfterVisibleIndicator(t *testing.T) {
	dir := t.TempDir()
	b, err := json.Marshal(settings{MAC: "02:11:22:ab:cd:ef", Key: (esphome.PSK{7, 42}).String()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "encore.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir, silentOutput{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	c, err := net.DialTimeout("tcp", "127.0.0.1:6055", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	read := func(kind byte) []byte {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		var header [3]byte
		if _, err := io.ReadFull(c, header[:]); err != nil {
			t.Fatal(err)
		}
		if header[0] != kind {
			t.Fatalf("wire kind = %#x, want %#x", header[0], kind)
		}
		body := make([]byte, binary.BigEndian.Uint16(header[1:]))
		if _, err := io.ReadFull(c, body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	var hello map[string]any
	if err := json.Unmarshal(read(1), &hello); err != nil {
		t.Fatal(err)
	}
	if hello["listen"] != true || hello["wake_format"] != "mixednet" || hello["stop_word"] != true ||
		hello["monitor"] != true || hello["monitor_result"] != true {
		t.Fatalf("missing local wake/stop, listen or monitor capability: %v", hello)
	}
	for _, flag := range []string{"aec", "monitor_aec", "intercom"} {
		if hello[flag] != false {
			t.Fatalf("unqualified %s advertised: %v", flag, hello)
		}
	}
	if hello["aec_lead_samples"] != nil {
		t.Fatalf("unavailable routes advertised: %v", hello)
	}
	request := func(body string, id int, accepted bool) {
		t.Helper()
		packet := binary.BigEndian.AppendUint16([]byte{0x14}, uint16(len(body)))
		_ = c.SetWriteDeadline(time.Now().Add(time.Second))
		if _, err := c.Write(append(packet, body...)); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(read(0x09), &result); err != nil || result["type"] != "monitor_result" ||
			result["request_id"] != float64(id) || result["accepted"] != accepted {
			t.Fatalf("monitor result = %v, err = %v", result, err)
		}
	}
	capture := func(pcm []byte) {
		t.Helper()
		if err := r.Capture(pcm); err != nil {
			t.Fatal(err)
		}
	}
	request(`{"active":true,"intercom":true,"request_id":41}`, 41, false)
	capture([]byte{9, 0, 22, 0}) // Never appears in the monitor's live stream.
	request(`{"active":true,"request_id":42}`, 42, true)
	id := r.engine.Snapshot().MonitorID
	if id == 0 {
		t.Fatal("no recording indicator request")
	}
	r.SetMonitorVisible(int64(id+1), true)
	for range 7 { // More than one report interval without a confirmed indicator.
		capture(make([]byte, 640))
	}
	_ = c.SetReadDeadline(time.Now().Add(40 * time.Millisecond))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("unexpected frame before recording indicator: %v", err)
	}
	r.SetMonitorVisible(int64(id), true)
	for i := range 6 {
		pcm := make([]byte, 640)
		pcm[0] = byte(43 + i)
		capture(pcm)
		if got := read(0x0A); !slices.Equal(got, pcm) {
			t.Fatal("monitor did not send exactly the current primary PCM")
		}
		time.Sleep(10 * time.Millisecond) // Let the bounded model queue consume the block.
	}
	read(0x0B)
	r.SetMonitorVisible(int64(id), false)
	r.SetMonitorVisible(int64(id), true) // Delayed draw callback cannot revive a revoked request.
	capture([]byte{57, 0})
	if _, err := c.Read(make([]byte, 1)); err != io.EOF || r.engine.Snapshot().MonitorID != 0 {
		t.Fatalf("indicator loss did not revoke the stream: %v", err)
	}
}
