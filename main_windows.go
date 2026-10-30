package main

import (
	"flag"
	"log"

	"github.com/kevmo314/urdp/internal/vdev"
)

var installCamera = flag.Bool("install-camera", false, "install urdp Camera (run once as Administrator)")
var uninstallCamera = flag.Bool("uninstall-camera", false, "remove urdp Camera (run as Administrator)")

func platformCommand() bool {
	if *installCamera && *uninstallCamera {
		log.Fatal("choose either -install-camera or -uninstall-camera")
	}
	if *installCamera {
		if err := vdev.InstallCamera(); err != nil {
			log.Fatal(err)
		}
		log.Print("urdp Camera installed; start urdp normally to make the camera available")
		return true
	}
	if *uninstallCamera {
		if err := vdev.UninstallCamera(); err != nil {
			log.Fatal(err)
		}
		log.Print("urdp Camera uninstalled")
		return true
	}
	return false
}
