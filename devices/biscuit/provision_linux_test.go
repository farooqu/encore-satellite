package biscuit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestWaitForImprovLinkDrain(t *testing.T) {
	for _, name := range []string{"drained", "already empty", "canceled", "timed out", "unavailable"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			adapter := filepath.Join(root, "hci0")
			link := filepath.Join(adapter, "hci0:42")
			if name != "unavailable" {
				if err := os.MkdirAll(filepath.Join(adapter, "power"), 0o700); err != nil {
					t.Fatal(err)
				}
				if name != "already empty" {
					if err := os.Mkdir(link, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				finished := make(chan error, 1)
				go func() { finished <- waitForImprovLinkDrain(ctx, adapter) }()
				synctest.Wait()
				var want error
				switch name {
				case "already empty", "unavailable":
					if name == "unavailable" {
						want = os.ErrNotExist
					}
					select {
					case got := <-finished:
						if !errors.Is(got, want) {
							t.Fatalf("drain returned %v, want %v", got, want)
						}
					default:
						t.Fatal("drain waited despite no link to observe")
					}
					return
				}
				select {
				case err := <-finished:
					t.Fatalf("drain returned while a link was still present: %v", err)
				default:
				}
				switch name {
				case "drained":
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
				case "canceled":
					cancel()
					want = context.Canceled
				case "timed out":
					want = context.DeadlineExceeded
				}
				if got := <-finished; !errors.Is(got, want) {
					t.Fatalf("drain returned %v, want %v", got, want)
				}
			})
		})
	}
}
