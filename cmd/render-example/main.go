// render-example generates a PNG screenshot of the rip-bastion display using
// a sample snapshot so that the output can be embedded in documentation.
package main

import (
	"flag"
	"log"

	"github.com/walterschell/rip-bastion/internal/display"
	"github.com/walterschell/rip-bastion/internal/mdns"
	"github.com/walterschell/rip-bastion/internal/network"
	"github.com/walterschell/rip-bastion/internal/sysinfo"
	"github.com/walterschell/rip-bastion/internal/vpn"
)

func main() {
	out := flag.String("o", "docs/example-render.png", "output PNG path")
	flag.Parse()

	dev := display.NewImageDevice(480, 320)
	sd := display.NewSystemDisplay(dev)

	snap := &sysinfo.Snapshot{
		Network: &network.Info{
			InterfaceName: "eth0",
			IP:            "192.168.1.100",
			Netmask:       "255.255.255.0",
			Gateway:       "192.168.1.1",
			DNS:           []string{"1.1.1.1", "8.8.8.8"},
		},
		MDNS: &mdns.Status{
			Running:  true,
			Hostname: "rip-bastion.local",
		},
		VPN: &vpn.Status{
			Name:      "WireGuard",
			Connected: true,
			Interface: "wg0",
			PeerIP:    "10.0.0.1",
		},
		Messages: []string{"System started", "VPN connected"},
		Errors:   make(map[string]string),
	}

	sd.Update(snap)
	if err := sd.Render(); err != nil {
		log.Fatalf("render: %v", err)
	}
	if err := dev.SavePNG(*out); err != nil {
		log.Fatalf("save PNG: %v", err)
	}
	log.Printf("Wrote %s", *out)
}
