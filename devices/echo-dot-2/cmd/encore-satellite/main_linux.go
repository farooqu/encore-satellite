package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	dot "github.com/farooqu/encore-satellite/devices/echo-dot-2"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// This service must not load voice config, clear voice readiness, or enter
	// provisioning: it survives voice runtime restarts during a Wi-Fi change.
	if len(os.Args) == 2 && os.Args[1] == "--network-manager" {
		if err := dot.RunNetworkManager(ctx); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Clear a stale marker even when config validation fails before Run.
	_ = os.Remove(dot.ReadyPath)
	c, err := dot.LoadConfig(dot.ConfigPath)
	if err != nil {
		log.Fatal(err)
	}
	if err = dot.PrepareNetwork(ctx, c); err != nil {
		log.Fatal(err)
	}
	if err = dot.Run(ctx, c); err != nil {
		log.Fatal(err)
	}
}
