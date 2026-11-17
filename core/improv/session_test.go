package improv_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/farooqu/encore-satellite/core/improv"
)

func TestPhysicalAuthorizationFragmentedWiFiAndRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		auth := make(chan struct{}, 1)
		writes := make(chan []byte, 8)
		type value struct {
			uuid string
			data []byte
		}
		updates := make(chan value, 32)
		done := make(chan error, 1)
		attempts := 0
		go func() {
			done <- improv.Run(ctx, improv.Options{Authorize: auth, Join: func(ctx context.Context, wifi improv.WiFi) (string, error) {
				if wifi.SSID != "asymmetric network" || wifi.Passphrase != "secret passphrase" {
					t.Error("credentials changed in transit")
				}
				attempts++
				if attempts == 1 {
					return "", errors.New("association failed")
				}
				return "http://192.0.2.23/", nil
			}}, writes, func(uuid string, b []byte) { updates <- value{uuid, append([]byte(nil), b...)} })
		}()
		next := func(uuid string, expected []byte) {
			t.Helper()
			for {
				v := <-updates
				if v.uuid == uuid {
					if !bytes.Equal(v.data, expected) {
						t.Fatalf("%s: got %x, want %x", uuid, v.data, expected)
					}
					return
				}
			}
		}
		next(improv.StateUUID, []byte{1})
		frame := improv.WiFiFrame(improv.WiFi{SSID: "asymmetric network", Passphrase: "secret passphrase"})
		writes <- frame
		next(improv.ErrorUUID, []byte{0}) // Initial error clear.
		next(improv.ErrorUUID, []byte{0}) // Accepted frame, but not authorized.
		next(improv.ErrorUUID, []byte{4})
		if attempts != 0 {
			t.Fatal("joined before physical authorization")
		}
		auth <- struct{}{}
		next(improv.StateUUID, []byte{2})
		time.Sleep(time.Minute)
		next(improv.StateUUID, []byte{1})
		writes <- frame
		next(improv.ErrorUUID, []byte{0})
		next(improv.ErrorUUID, []byte{4})
		if attempts != 0 {
			t.Fatal("expired authorization permitted WiFi")
		}
		auth <- struct{}{}
		next(improv.StateUUID, []byte{2})
		writes <- improv.WiFiFrame(improv.WiFi{SSID: strings.Repeat("s", 33)})
		next(improv.ErrorUUID, []byte{0})
		next(improv.ErrorUUID, []byte{1})
		writes <- frame[:1]
		writes <- frame[1:9]
		writes <- frame[9:]
		next(improv.StateUUID, []byte{3})
		next(improv.ErrorUUID, []byte{3})
		next(improv.StateUUID, []byte{2})
		writes <- frame
		next(improv.StateUUID, []byte{3})
		next(improv.StateUUID, []byte{4})
		// Independently encoded wire response: command, length, URL length, URL, sum.
		result := []byte{1, 19, 18, 'h', 't', 't', 'p', ':', '/', '/', '1', '9', '2', '.', '0', '.', '2', '.', '2', '3', '/'}
		var sum byte
		for _, b := range result {
			sum += b
		}
		result = append(result, sum)
		next(improv.RPCResultUUID, result)
		if attempts != 2 {
			t.Fatalf("join attempts=%d", attempts)
		}
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("result window not retained")
		default:
		}
		time.Sleep(10 * time.Second)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestClosedAuthorizationDoesNotAuthorizeAndCallerCanCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		auth := make(chan struct{})
		close(auth)
		writes := make(chan []byte, 1)
		writes <- improv.WiFiFrame(improv.WiFi{SSID: "network"})
		var unauthorized bool
		err := improv.Run(ctx, improv.Options{Authorize: auth, Join: func(context.Context, improv.WiFi) (string, error) {
			t.Error("closed channel authorized WiFi")
			return "", nil
		}}, writes, func(uuid string, b []byte) {
			if uuid == improv.ErrorUUID && bytes.Equal(b, []byte{4}) {
				unauthorized = true
			}
		})
		if !unauthorized || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unauthorized=%v error=%v", unauthorized, err)
		}
	})
}

func TestHealthyUserWaitingIsUnlimitedButHealthFailureStopsOffer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("transport or button lost")
		checks := 0
		start := time.Now()
		err := improv.Run(context.Background(), improv.Options{
			Authorize: make(chan struct{}),
			Join: func(context.Context, improv.WiFi) (string, error) {
				t.Fatal("joined without user credentials")
				return "", nil
			},
			Health: func() error {
				checks++
				if time.Since(start) >= 24*time.Hour {
					return failure
				}
				return nil
			},
		}, make(chan []byte), func(string, []byte) {})
		if !errors.Is(err, failure) || checks < 86400 {
			t.Fatalf("checks=%d err=%v", checks, err)
		}
	})
}

func TestJoinHeartbeatsUntilDeadlineAndWaitsForCleanupBeforeRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		auth := make(chan struct{}, 1)
		writes := make(chan []byte, 1)
		auth <- struct{}{}
		checks, attempts := 0, 0
		cleaned := false
		err := improv.Run(ctx, improv.Options{
			Authorize: auth,
			Health:    func() error { checks++; return nil },
			Join: func(ctx context.Context, _ improv.WiFi) (string, error) {
				attempts++
				<-ctx.Done()
				time.Sleep(2 * time.Second) // Supplicant cleanup must finish first.
				cleaned = true
				return "", ctx.Err()
			},
		}, writes, func(uuid string, b []byte) {
			if uuid == improv.StateUUID && bytes.Equal(b, []byte{byte(improv.Authorized)}) && attempts == 0 {
				writes <- improv.WiFiFrame(improv.WiFi{SSID: "network", Passphrase: "password"})
			}
			if uuid == improv.ErrorUUID && bytes.Equal(b, []byte{byte(improv.ErrUnableToConnect)}) {
				if !cleaned {
					t.Error("retry before candidate cleanup finished")
				}
				cancel()
			}
		})
		if !errors.Is(err, context.Canceled) || checks < 79 || attempts != 1 {
			t.Fatalf("checks=%d attempts=%d err=%v", checks, attempts, err)
		}
	})
}
