// arm64-verify runs from the immutable ARM64 recovery ramdisk before extraction.
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/farooqu/encore-satellite/devices/biscuit/internal/nativeab"
	"github.com/farooqu/encore-satellite/platforms/linuxupdate"
)

func main() {
	if len(os.Args) == 2 {
		device := nativeab.Device()
		var err error
		switch os.Args[1] {
		case "native-trial":
			var trial bool
			if trial, err = device.Trial(); err == nil {
				fmt.Println(trial)
			}
		case "native-watchdog":
			err = nativeab.GuardTrial()
		case "native-confirm":
			if err = device.Confirm(); err == nil {
				err = os.WriteFile("/run/encore/native-confirmed", nil, 0600)
			}
		default:
			os.Exit(2)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 6 && len(os.Args) != 7 {
		os.Exit(2)
	}
	revision, err := strconv.ParseInt(os.Args[5], 10, 64)
	if err != nil {
		os.Exit(2)
	}
	store := linuxupdate.ARM64Store{Directory: os.Args[1]}
	seed := linuxupdate.BiscuitARM64Identity(os.Args[4], revision)
	if len(os.Args) == 7 {
		if value, err := hex.DecodeString(os.Args[6]); err != nil || len(value) != 32 {
			os.Exit(2)
		}
		seed.BaseSHA256 = os.Args[6]
	}
	m, err := store.VerifyBoot(os.Args[2], os.Args[3], seed, linuxupdate.BiscuitARM64Root())
	if err == nil {
		// A runtime from OTA can also run under pre-channel recovery. Only
		// advertise Dev selection when this boot's verifier supports it.
		err = json.NewEncoder(os.Stdout).Encode(struct {
			linuxupdate.Manifest
			UpdateChannels bool `json:"update_channels"`
		}{m, true})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
