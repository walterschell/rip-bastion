//go:build rpi

package main

import (
	"log"
	"time"

	"github.com/walterschell/rip-bastion/internal/display"
	"github.com/walterschell/rip-bastion/internal/drm"
	"github.com/walterschell/rip-bastion/internal/messages"
	"github.com/walterschell/rip-bastion/internal/sysinfo"
	"github.com/walterschell/rip-bastion/internal/vpn"
)

func main() {
	dev, err := drm.New(0)
	if err != nil {
		log.Fatalf("Failed to open DRM display: %v", err)
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
