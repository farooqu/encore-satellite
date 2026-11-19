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

func (d *biscuitControls) serveAPI(ctx context.Context, path string, action <-chan struct{}, info esphome.Info, ad mdns.Config) error {
	for {
		key, err := noiseKey(path)
		if err == nil {
			ad.Encrypted, ad.Provisionable = true, false
			return serveAdvertisedAPI(ad, func(l net.Listener) error { return d.Serve(ctx, l, info, key) })
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		log.Print("ESPHome setup: press Action to open the two-minute pairing window")
		select {
		case <-ctx.Done():
			return nil
		case <-action:
		}
		ad.Encrypted, ad.Provisionable = false, true
		err = serveAdvertisedAPI(ad, func(l net.Listener) error {
			return esphomepairing.Serve(ctx, l, info, func(key esphome.PSK) error { return saveNoiseKey(path, key) })
		})
		if err != nil || ctx.Err() != nil {
			return err
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
