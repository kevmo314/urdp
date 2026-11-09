package main

import (
	_ "embed"
	"flag"
	"log"

	"github.com/kevmo314/urdp/internal/windrivers"
)

//go:embed THIRD_PARTY_NOTICES
var driverNotices []byte

var installDrivers = flag.Bool("install-drivers", false, "install bundled Windows gamepad and USB/IP drivers (one UAC prompt)")
var uninstallDrivers = flag.Bool("uninstall-drivers", false, "remove drivers installed by urdp; retain pre-existing devices")
var driverLog = flag.String("driver-log", "", "internal elevated driver setup log")

func platformCommand() bool {
	action, err := windrivers.ParseAction(*installDrivers, *uninstallDrivers, *driverLog)
	if err != nil {
		log.Fatal(err)
	}
	if action == windrivers.None {
		return false
	}
	if err := windrivers.Run(action == windrivers.Uninstall, *driverLog, driverNotices); err != nil {
		log.Fatal(err)
	}
	return true
}
