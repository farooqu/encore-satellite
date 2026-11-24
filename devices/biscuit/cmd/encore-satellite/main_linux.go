package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/farooqu/encore-satellite/devices/biscuit"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) == 3 && os.Args[1] == "--boot-log" {
		captureBootLog(ctx, os.Args[2])
		return
	}
	// This service must not load voice config, clear voice readiness, or enter
	// provisioning: it survives voice runtime restarts during a Wi-Fi change.
	if len(os.Args) == 2 && os.Args[1] == "--network-manager" {
		if err := biscuit.RunNetworkManager(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Clear a stale marker even when config validation fails before Run.
	_ = os.Remove(biscuit.ReadyPath)
	c, err := biscuit.LoadConfig(biscuit.ConfigPath)
	if err != nil {
		log.Fatal(err)
	}
	if err = biscuit.PrepareNetwork(ctx, c); err != nil {
		log.Fatal(err)
	}
	if len(os.Args) == 2 && os.Args[1] == "--provision-only" {
		return
	}
	probeReference := len(os.Args) == 2 && os.Args[1] == "--probe-aec-reference"
	if err = biscuit.Run(ctx, c, probeReference); err != nil {
		log.Fatal(err)
	}
}
