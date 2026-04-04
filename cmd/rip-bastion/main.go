//go:build rpi

package main

import (
	"log"
	"os"
	"time"

	"github.com/walterschell/rip-bastion/internal/display"
	"github.com/walterschell/rip-bastion/internal/drm"
	"github.com/walterschell/rip-bastion/internal/messages"
	"github.com/walterschell/rip-bastion/internal/spi"
	"github.com/walterschell/rip-bastion/internal/sysinfo"
	"github.com/walterschell/rip-bastion/internal/vpn"
)

func setSPIRuntimeDefaults() {
	if _, ok := os.LookupEnv("RIP_SPI_STREAM_FULL_FRAME"); !ok {
		_ = os.Setenv("RIP_SPI_STREAM_FULL_FRAME", "1")
	}
	if _, ok := os.LookupEnv("RIP_SPI_COLUMN_MAJOR"); !ok {
		_ = os.Setenv("RIP_SPI_COLUMN_MAJOR", "0")
	}
	if _, ok := os.LookupEnv("RIP_SPI_PACK_PIXELS16"); !ok {
		_ = os.Setenv("RIP_SPI_PACK_PIXELS16", "0")
	}
}

func main() {
	setSPIRuntimeDefaults()

	var dev display.Device
	var err error

	dev, err = spi.NewFromEnv()
	if err != nil {
		log.Printf("SPI display init failed (%v), falling back to DRM", err)
		dev, err = drm.NewAuto()
		if err != nil {
			log.Fatalf("Failed to open display (SPI and DRM both failed): %v", err)
		}
	}

	sd := display.NewSystemDisplay(dev)
	defer sd.Close()

	msgStore := messages.NewStore()
	msgStore.Add("System starting...")

	collector := sysinfo.NewCollector(vpn.DefaultProvider(), msgStore)
	defer collector.Stop()

	for {
		snap := collector.Collect()
		sd.Update(snap)
		if err := sd.Render(); err != nil {
			log.Printf("Render error: %v", err)
		}
		time.Sleep(5 * time.Second)
	}
}
