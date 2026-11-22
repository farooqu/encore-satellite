package mobile

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
)

func TestAndroidOffersLocalModelsButNoOffCallMicRoutes(t *testing.T) {
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
	_ = c.SetDeadline(time.Now().Add(time.Second))
	read := func(kind byte) []byte {
		t.Helper()
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
	if hello["listen"] != true || hello["wake_format"] != "mixednet" || hello["stop_word"] != true {
		t.Fatalf("missing local wake/stop or listen capability: %v", hello)
	}
	for _, flag := range []string{"aec", "monitor", "monitor_aec", "intercom"} {
		if hello[flag] != false {
			t.Fatalf("unqualified %s advertised: %v", flag, hello)
		}
	}
	if hello["monitor_result"] != nil || hello["aec_lead_samples"] != nil {
		t.Fatalf("unavailable routes advertised: %v", hello)
	}
	request := []byte(`{"active":true,"intercom":true,"request_id":41}`)
	packet := binary.BigEndian.AppendUint16([]byte{0x14}, uint16(len(request)))
	if _, err := c.Write(append(packet, request...)); err != nil {
		t.Fatal(err)
	}
	// A rejected model header is a wire barrier behind the monitor request.
	if _, err := c.Write([]byte{0x20, 0, 0}); err != nil {
		t.Fatal(err)
	}
	read(0x07)
	if err := r.Capture([]byte{9, 0, 22, 0}); err != nil {
		t.Fatal(err)
	}
	if err := r.Talk(); err != nil {
		t.Fatalf("unsupported remote monitor blocked local talk: %v", err)
	}
	read(0x02) // A monitor result, audio or pre-roll sent off-call would precede CALL_START.
	if err := r.Capture([]byte{43, 0}); err != nil {
		t.Fatal(err)
	}
	if pcm := read(0x03); string(pcm) != string([]byte{43, 0}) {
		t.Fatalf("live call did not receive exactly its primary PCM: %v", pcm)
	}
}
