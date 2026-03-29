package sysinfo

import (
	"context"
	"sync"

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
	Errors   map[string]string // per-subsystem error strings
}

// Collector gathers system information.  Network and mDNS are polled on each
// Collect call; VPN status is received reactively via the provider's Subscribe
// channel and cached so that Collect never blocks on the VPN.
type Collector struct {
	vpnProvider vpn.Provider
	msgStore    *messages.Store

	vpnMu  sync.RWMutex
	vpnLast *vpn.Status
	vpnErr  string

	cancel context.CancelFunc
}

// NewCollector creates a Collector backed by vp and ms.  It immediately
// subscribes to VPN push notifications in a background goroutine so that the
// first Collect call already has a valid cached VPN status.
func NewCollector(vp vpn.Provider, ms *messages.Store) *Collector {
	// Seed the cache with a synchronous read so the VPN status is never nil
	// before the first push arrives.
	initial, err := vp.Status()

	ctx, cancel := context.WithCancel(context.Background())
	c := &Collector{
		vpnProvider: vp,
		msgStore:    ms,
		cancel:      cancel,
	}
	if err != nil {
		c.vpnErr = err.Error()
	} else {
		c.vpnLast = initial
	}

	go c.watchVPN(ctx, vp.Subscribe(ctx))
	return c
}

// watchVPN drains the VPN status channel and keeps the cache up to date.
func (c *Collector) watchVPN(ctx context.Context, ch <-chan *vpn.Status) {
	for {
		select {
		case status, ok := <-ch:
			if !ok {
				return
			}
			c.vpnMu.Lock()
			c.vpnLast = status
			c.vpnErr = ""
			c.vpnMu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

// Stop cancels the background VPN subscription goroutine.  Call this when
// the Collector is no longer needed.
func (c *Collector) Stop() {
	c.cancel()
}

// Collect builds a fresh Snapshot.  Network and mDNS are queried
// synchronously; VPN status comes from the push-updated cache.
// Per-subsystem errors are captured in Snapshot.Errors rather than returned.
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

	c.vpnMu.RLock()
	snap.VPN = c.vpnLast
	if c.vpnErr != "" {
		snap.Errors["vpn"] = c.vpnErr
	}
	c.vpnMu.RUnlock()

	snap.Messages = c.msgStore.All()
	return snap
}
