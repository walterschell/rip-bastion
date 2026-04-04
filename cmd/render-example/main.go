// render-example generates a PNG screenshot of the rip-bastion display using
// a sample snapshot so that the output can be embedded in documentation.
package main

import (
	"flag"
	"log"

	"github.com/walterschell/rip-bastion/internal/display"
	"github.com/walterschell/rip-bastion/internal/mdns"
	"github.com/walterschell/rip-bastion/internal/network"
	"github.com/walterschell/rip-bastion/internal/ssh"
	"github.com/walterschell/rip-bastion/internal/sysinfo"
	"github.com/walterschell/rip-bastion/internal/vpn"
)

func main() {
	out := flag.String("o", "docs/example-render.png", "output PNG path")
	width := flag.Int("width", 320, "render width in pixels")
	height := flag.Int("height", 480, "render height in pixels")
	flag.Parse()

	dev := display.NewImageDevice(*width, *height)
	sd := display.NewSystemDisplay(dev)

	snap := &sysinfo.Snapshot{
		Network: &network.Info{
			InterfaceName: "eth0",
			IP:            "192.168.1.100",
			Netmask:       "255.255.255.0",
			CIDR:          "192.168.1.100/24",
			Gateway:       "192.168.1.1",
			DNS:           []string{"1.1.1.1", "8.8.8.8"},
			ExternalIP:    "203.0.113.44",
		},
		NetworkRXKBps: 245.2,
		NetworkTXKBps: 52.4,
		NetworkRXBandwidthHistory: []float64{
			20, 28, 22, 34, 48, 62, 78, 56, 74, 68,
			96, 90, 120, 112, 140, 118, 132, 108, 126, 116,
			124, 136, 128, 148, 142, 156, 150, 162, 158, 172,
			166, 184, 176, 192, 188, 204, 198, 214, 228, 245,
		},
		NetworkTXBandwidthHistory: []float64{
			6, 10, 8, 12, 18, 22, 28, 20, 26, 18,
			30, 24, 34, 32, 40, 150, 164, 142, 118, 96,
			52, 48, 44, 40, 36, 34, 30, 28, 24, 22,
			20, 18, 16, 14, 12, 10, 18, 26, 38, 52,
		},
		MDNS: &mdns.Status{
			Running:  true,
			Hostname: "rip-bastion.local",
		},
		SSH: &ssh.Status{Running: true},
		VPN: &vpn.Status{
			Name:      "WireGuard",
			Connected: true,
			Interface: "wg0",
			LocalCIDR: "10.0.0.2/24",
			PeerIP:    "10.0.0.1",
		},
		VPNRXKBps: 41.2,
		VPNTXKBps: 96.8,
		VPNRXBandwidthHistory: []float64{
			10, 12, 18, 16, 22, 28, 36, 30, 34, 32,
			40, 38, 44, 36, 34, 28, 24, 22, 26, 30,
			34, 38, 42, 40, 36, 32, 28, 24, 20, 18,
			16, 18, 20, 24, 28, 32, 36, 38, 40, 41,
		},
		VPNTXBandwidthHistory: []float64{
			3, 4, 6, 5, 8, 10, 14, 12, 10, 9,
			16, 14, 18, 46, 58, 74, 88, 97, 82, 76,
			68, 62, 58, 54, 50, 46, 44, 42, 40, 38,
			36, 42, 48, 56, 64, 72, 80, 88, 92, 97,
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
