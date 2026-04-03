package sysinfo

import (
	"context"
	"sync"
	"time"

	"github.com/walterschell/rip-bastion/internal/mdns"
	"github.com/walterschell/rip-bastion/internal/messages"
	"github.com/walterschell/rip-bastion/internal/network"
	"github.com/walterschell/rip-bastion/internal/ssh"
	"github.com/walterschell/rip-bastion/internal/vpn"
)

// Snapshot holds a point-in-time view of all system status.
type Snapshot struct {
	Network                   *network.Info
	NetworkRXKBps             float64
	NetworkTXKBps             float64
	NetworkRXBandwidthHistory []float64
	NetworkTXBandwidthHistory []float64
	MDNS                      *mdns.Status
	SSH                       *ssh.Status
	VPN                       *vpn.Status
	VPNRXKBps                 float64
	VPNTXKBps                 float64
	VPNRXBandwidthHistory     []float64
	VPNTXBandwidthHistory     []float64
	Messages                  []string
	Errors                    map[string]string // per-subsystem error strings
}

type ifaceBandwidthState struct {
	lastRX  uint64
	lastTX  uint64
	lastAt  time.Time
	rxHistory []float64
	txHistory []float64
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

	bwMu      sync.Mutex
	ifaceBW   map[string]*ifaceBandwidthState
	vpnIfLast string

	cancel context.CancelFunc
}

const maxBandwidthSamples = 40

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
		ifaceBW:     make(map[string]*ifaceBandwidthState),
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
		rx, tx, rxHistory, txHistory, bwErr := c.sampleInterfaceBandwidth(netInfo.InterfaceName)
		if bwErr == nil {
			snap.NetworkRXKBps = rx
			snap.NetworkTXKBps = tx
			snap.NetworkRXBandwidthHistory = rxHistory
			snap.NetworkTXBandwidthHistory = txHistory
		}
	}

	mdnsStatus, err := mdns.Get()
	if err != nil {
		snap.Errors["mdns"] = err.Error()
	} else {
		snap.MDNS = mdnsStatus
	}

	sshStatus, err := ssh.Get()
	if err != nil {
		snap.Errors["ssh"] = err.Error()
	} else {
		snap.SSH = sshStatus
	}

	c.vpnMu.RLock()
	snap.VPN = c.vpnLast
	if c.vpnErr != "" {
		snap.Errors["vpn"] = c.vpnErr
	}
	c.vpnMu.RUnlock()

	if snap.VPN != nil {
		vpnIf := snap.VPN.Interface
		if vpnIf != "" {
			snap.VPN.LocalCIDR = network.InterfaceCIDR(vpnIf)
			c.bwMu.Lock()
			c.vpnIfLast = vpnIf
			c.bwMu.Unlock()
		} else {
			c.bwMu.Lock()
			vpnIf = c.vpnIfLast
			c.bwMu.Unlock()
		}

		if vpnIf != "" {
			rx, tx, rxHistory, txHistory, bwErr := c.sampleInterfaceBandwidth(vpnIf)
			if bwErr == nil {
				snap.VPNRXKBps = rx
				snap.VPNTXKBps = tx
				snap.VPNRXBandwidthHistory = rxHistory
				snap.VPNTXBandwidthHistory = txHistory
			}
		}
	}

	snap.Messages = c.msgStore.All()
	return snap
}

func (c *Collector) sampleInterfaceBandwidth(iface string) (rxKBps, txKBps float64, rxHistory, txHistory []float64, err error) {
	rxNow, txNow, err := network.InterfaceByteCounters(iface)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	now := time.Now()

	c.bwMu.Lock()
	defer c.bwMu.Unlock()

	state, ok := c.ifaceBW[iface]
	if !ok {
		state = &ifaceBandwidthState{lastRX: rxNow, lastTX: txNow, lastAt: now, rxHistory: []float64{0}, txHistory: []float64{0}}
		c.ifaceBW[iface] = state
		return 0, 0, append([]float64(nil), state.rxHistory...), append([]float64(nil), state.txHistory...), nil
	}

	dt := now.Sub(state.lastAt).Seconds()
	if dt > 0 {
		rxDelta := counterDelta(state.lastRX, rxNow)
		txDelta := counterDelta(state.lastTX, txNow)
		rxKBps = float64(rxDelta) / dt / 1024.0
		txKBps = float64(txDelta) / dt / 1024.0
		state.rxHistory = appendRolling(state.rxHistory, rxKBps, maxBandwidthSamples)
		state.txHistory = appendRolling(state.txHistory, txKBps, maxBandwidthSamples)
	}

	state.lastRX = rxNow
	state.lastTX = txNow
	state.lastAt = now

	return rxKBps, txKBps, append([]float64(nil), state.rxHistory...), append([]float64(nil), state.txHistory...), nil
}

func counterDelta(prev, now uint64) uint64 {
	if now >= prev {
		return now - prev
	}
	// Counter reset or wrap; treat current value as fresh delta.
	return now
}

func appendRolling(history []float64, value float64, max int) []float64 {
	history = append(history, value)
	if len(history) <= max {
		return history
	}
	start := len(history) - max
	trimmed := make([]float64, max)
	copy(trimmed, history[start:])
	return trimmed
}
