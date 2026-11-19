// arm64-verify runs from the immutable ARM64 recovery ramdisk before extraction.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/farooqu/encore-satellite/platforms/linuxupdate"
)

func main() {
	if len(os.Args) != 6 {
		os.Exit(2)
	}
	revision, err := strconv.ParseInt(os.Args[5], 10, 64)
	if err != nil {
		os.Exit(2)
	}
	store := linuxupdate.ARM64Store{Directory: os.Args[1]}
	m, err := store.VerifyBoot(os.Args[2], os.Args[3], linuxupdate.BiscuitARM64Identity(os.Args[4], revision), linuxupdate.BiscuitARM64Root())
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(m)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
