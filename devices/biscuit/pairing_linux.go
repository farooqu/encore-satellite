package biscuit

import (
	"context"
	"errors"
	"log"
	"net"
	"os"

	"github.com/farooqu/encore-satellite/internal/esphomepairing"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/mdns"
)

// waitForESPHomeKey returns a validated key only after the bootstrap listener
// has shut down. An expired window requires another physical press.
func waitForESPHomeKey(ctx context.Context, path string, action <-chan struct{}, info esphome.Info, ad mdns.Config, ready func() error, emit func(setupCue)) (esphome.PSK, error) {
	first := true
	for {
		key, err := noiseKey(path)
		if !errors.Is(err, os.ErrNotExist) {
			return key, err
		}
		if err := ctx.Err(); err != nil {
			return key, err
		}
		log.Print("ESPHome setup: press Action to open the two-minute pairing window")
		if err = ready(); err != nil {
			return key, err
		}
		if first {
			emit(haReady)
			first = false
		} else {
			emit(pairingTimeout)
		}
		select {
		case <-ctx.Done():
			return key, ctx.Err()
		case <-action:
		}
		ad.Encrypted, ad.Provisionable = false, true
		err = serveAdvertisedAPI(ad, func(l net.Listener) error {
			emit(pairingOpen)
			return esphomepairing.Serve(ctx, l, info, func(key esphome.PSK) error { return saveNoiseKey(path, key) })
		})
		if err != nil {
			return key, err
		}
	}
}

func serveAdvertisedAPI(ad mdns.Config, serve func(net.Listener) error) error {
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{Port: ad.Port})
	if err != nil {
		return err
	}
	defer l.Close()
	a, err := mdns.Advertise(ad)
	if err != nil {
		return err
	}
	defer a.Close()
	return serve(l)
}
