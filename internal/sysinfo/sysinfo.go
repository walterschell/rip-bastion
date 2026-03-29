package sysinfo

import (
	"github.com/walterschell/rip-bastion/internal/mdns"
	"github.com/walterschell/rip-bastion/internal/messages"
	"github.com/walterschell/rip-bastion/internal/network"
	"github.com/walterschell/rip-bastion/internal/vpn"
)

// Snapshot holds a point-in-time view of all system status.
type Snapshot struct {
	Network  *network.Info
	MDNS     *mdns.Status
	VPN      *vpn.Status
	Messages []string
	Errors   map[string]string // per-subsystem errors
}

// Collector gathers system information.
type Collector struct {
	vpnProvider vpn.Provider
	msgStore    *messages.Store
}

// NewCollector creates a new Collector with the given VPN provider and message store.
func NewCollector(vp vpn.Provider, ms *messages.Store) *Collector {
	return &Collector{
		vpnProvider: vp,
		msgStore:    ms,
	}
}

// Collect gathers a fresh Snapshot. Errors are captured per-subsystem rather than returned.
func (c *Collector) Collect() *Snapshot {
	snap := &Snapshot{
		Errors: make(map[string]string),
	}

	netInfo, err := network.Get()
	if err != nil {
		snap.Errors["network"] = err.Error()
	} else {
		snap.Network = netInfo
	}

	mdnsStatus, err := mdns.Get()
	if err != nil {
		snap.Errors["mdns"] = err.Error()
	} else {
		snap.MDNS = mdnsStatus
	}

	vpnStatus, err := c.vpnProvider.Status()
	if err != nil {
		snap.Errors["vpn"] = err.Error()
	} else {
		snap.VPN = vpnStatus
	}

	snap.Messages = c.msgStore.All()
	return snap
}
