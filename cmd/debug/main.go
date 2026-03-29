//go:build !rpi

package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/walterschell/rip-bastion/internal/messages"
	"github.com/walterschell/rip-bastion/internal/sysinfo"
	"github.com/walterschell/rip-bastion/internal/vpn"
	"github.com/walterschell/rip-bastion/internal/webui"
)

type uiLabels struct {
	iface      *widget.Label
	ipNet      *widget.Label
	gateway    *widget.Label
	dns        *widget.Label
	mdnsStatus *widget.Label
	mdnsHost   *widget.Label
	vpnStatus  *widget.Label
	vpnDetails *widget.Label
	messages   *widget.Label
}

func newLabel(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return l
}

func updateUI(labels *uiLabels, snap *sysinfo.Snapshot) {
	if snap.Network != nil {
		labels.iface.SetText(fmt.Sprintf("Interface : %s", snap.Network.InterfaceName))
		labels.ipNet.SetText(fmt.Sprintf("IP/Mask   : %s / %s", snap.Network.IP, snap.Network.Netmask))
		labels.gateway.SetText(fmt.Sprintf("Gateway   : %s", snap.Network.Gateway))
		labels.dns.SetText(fmt.Sprintf("DNS       : %s", strings.Join(snap.Network.DNS, ", ")))
	} else if errMsg, ok := snap.Errors["network"]; ok {
		labels.iface.SetText("Network error: " + errMsg)
		labels.ipNet.SetText("")
		labels.gateway.SetText("")
		labels.dns.SetText("")
	}

	if snap.MDNS != nil {
		if snap.MDNS.Running {
			labels.mdnsStatus.SetText("● Running")
		} else {
			labels.mdnsStatus.SetText("○ Stopped")
		}
		labels.mdnsHost.SetText(snap.MDNS.Hostname)
	}

	if snap.VPN != nil {
		if snap.VPN.Connected {
			labels.vpnStatus.SetText(fmt.Sprintf("● Connected  (%s)", snap.VPN.Name))
			labels.vpnDetails.SetText(fmt.Sprintf("Interface: %s  Peer: %s", snap.VPN.Interface, snap.VPN.PeerIP))
		} else {
			labels.vpnStatus.SetText(fmt.Sprintf("○ Disconnected  (%s)", snap.VPN.Name))
			labels.vpnDetails.SetText("")
		}
	}

	if len(snap.Messages) > 0 {
		var sb strings.Builder
		for _, m := range snap.Messages {
			sb.WriteString("▸ ")
			sb.WriteString(m)
			sb.WriteString("\n")
		}
		labels.messages.SetText(sb.String())
	} else {
		labels.messages.SetText("(no messages)")
	}
}

func main() {
	a := app.New()
	w := a.NewWindow("rip-bastion")
	w.Resize(fyne.NewSize(480, 320))

	labels := &uiLabels{
		iface:      newLabel("Interface : —"),
		ipNet:      newLabel("IP/Mask   : —"),
		gateway:    newLabel("Gateway   : —"),
		dns:        newLabel("DNS       : —"),
		mdnsStatus: newLabel("○ Unknown"),
		mdnsHost:   newLabel("—"),
		vpnStatus:  newLabel("○ Unknown"),
		vpnDetails: newLabel(""),
		messages:   newLabel("(no messages)"),
	}

	title := widget.NewLabelWithStyle("rip-bastion", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})

	content := container.NewVBox(
		title,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("── Network ──────────────────", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		labels.iface,
		labels.ipNet,
		labels.gateway,
		labels.dns,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("── mDNS ─────────────────────", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(labels.mdnsStatus, labels.mdnsHost),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("── VPN ──────────────────────", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		labels.vpnStatus,
		labels.vpnDetails,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("── Messages ─────────────────", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		labels.messages,
	)

	w.SetContent(container.NewScroll(content))

	// Start web UI
	webServer, err := webui.New(":8080")
	if err != nil {
		log.Printf("Failed to create web UI: %v", err)
	} else {
		webServer.Start()
	}

	msgStore := messages.NewStore()
	msgStore.Add("Debug session started")

	collector := sysinfo.NewCollector(vpn.DefaultProvider(), msgStore)

	// Initial data load
	snap := collector.Collect()
	updateUI(labels, snap)
	if webServer != nil {
		webServer.Update(snap)
	}

	// Refresh every 5 seconds
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			snap := collector.Collect()
			updateUI(labels, snap)
			if webServer != nil {
				webServer.Update(snap)
			}
		}
	}()

	w.ShowAndRun()
}
