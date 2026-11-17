package android_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/ble"
	androidhost "github.com/farooqu/encore-satellite/platforms/android"
)

type nativeScan struct {
	started, stopped chan int64
	startError       error
}

func (n nativeScan) StartScan(g int64) error { n.started <- g; return n.startError }
func (n nativeScan) StopScan(g int64) error  { n.stopped <- g; return nil }

func TestNativeScanPreservesBytesAndFencesCancelledGenerations(t *testing.T) {
	n := nativeScan{started: make(chan int64, 2), stopped: make(chan int64, 2)}
	s := androidhost.NewBluetoothScanner(n)
	ads := make(chan ble.Advertisement, 8)
	for run := 0; run < 2; run++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		done := make(chan error, 1)
		go func() { done <- s.Scan(ctx, true, func() error { return nil }, func(a ble.Advertisement) { ads <- a }) }()
		var token int64
		select {
		case token = <-n.started:
		case <-ctx.Done():
			t.Fatal("scan did not start")
		}
		data := []byte{2, 1, 6, 3, 0xff, 0x12, 0x9a}
		s.Report(token-1, "C0:FE:19:82:E3:45", 1, -73, data)
		s.Report(token, "C0:FE:19:82:E3:45", 2, -73, data)
		s.Report(token, "C0:FE:19:82:E3:45", 1, -73, make([]byte, 63))
		s.Report(token, "C0:FE:19:82:E3:45", 1, -73, data)
		data[6] = 0
		select {
		case a := <-ads:
			if a.Address != 0xc0fe1982e345 || a.AddressType != 1 || a.RSSI != -73 || !reflect.DeepEqual(a.Data, []byte{2, 1, 6, 3, 0xff, 0x12, 0x9a}) {
				t.Fatalf("corrupted advertisement: %+v", a)
			}
		case <-ctx.Done():
			t.Fatal("missing advertisement")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("cancellation hung")
		}
		if stopped := <-n.stopped; stopped != token {
			t.Fatal("wrong generation stopped")
		}
		s.Report(token, "C0:FE:19:82:E3:45", 1, -73, data)
		select {
		case extra := <-ads:
			t.Fatalf("late/invalid callback delivered: %+v", extra)
		default:
		}
	}
}

func TestNativeStartFailureStillCleansUpRegistration(t *testing.T) {
	denied := errors.New("permission revoked during start")
	n := nativeScan{started: make(chan int64, 1), stopped: make(chan int64, 1), startError: denied}
	s := androidhost.NewBluetoothScanner(n)
	err := s.Scan(context.Background(), true, func() error {
		t.Fatal("reported running after native start failed")
		return nil
	}, func(ble.Advertisement) { t.Fatal("delivered after failed start") })
	if !errors.Is(err, denied) {
		t.Fatalf("lost native failure: %v", err)
	}
	select {
	case token := <-n.stopped:
		if token != <-n.started {
			t.Fatal("cleaned up another generation")
		}
	default:
		t.Fatal("partial native registration was not cleaned up")
	}
}
