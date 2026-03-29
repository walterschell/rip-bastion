// Package display defines a generic drawing device interface and the
// SystemDisplay, which owns all current system state and renders it onto a
// Device using only the device's primitive drawing methods.
package display

import (
	"fmt"
	"image/color"
	"strings"
	"sync"

	"github.com/walterschell/rip-bastion/internal/sysinfo"
)

// Device is a generic drawing surface.  Rendering backends (DRM/KMS, Fyne
// canvas, etc.) implement this interface so that SystemDisplay can operate on
// any physical or virtual output without knowing its internals.
type Device interface {
	// Dimensions of the drawable area in pixels.
	Width() int
	Height() int

	// TextLineHeight returns the vertical distance (in pixels) between
	// successive text baselines, including leading.  SystemDisplay uses this
	// to position every line on the screen.
	TextLineHeight() int

	// Clear fills the entire surface with the given colour.
	Clear(c color.Color)

	// DrawText draws a UTF-8 string with its baseline at pixel (x, y).
	DrawText(x, y int, c color.Color, text string)

	// DrawHLine draws a filled horizontal line from column x0 to x1 at row y.
	DrawHLine(x0, x1, y int, c color.Color)

	// DrawRect draws a filled axis-aligned rectangle.
	DrawRect(x, y, w, h int, c color.Color)

	// DrawCircle draws a filled circle centred at (cx, cy) with radius r.
	DrawCircle(cx, cy, r int, c color.Color)

	// Flush presents the rendered frame to the physical output.
	Flush() error

	// Close releases all resources held by the device.
	Close() error
}

// Dashboard colour palette.
var (
	ColorBackground = color.RGBA{R: 0, G: 0, B: 0, A: 255}
	ColorTitle      = color.RGBA{R: 0, G: 212, B: 255, A: 255}
	ColorSectionHdr = color.RGBA{R: 136, G: 136, B: 136, A: 255}
	ColorText       = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	ColorOK         = color.RGBA{R: 0, G: 255, B: 136, A: 255}
	ColorError      = color.RGBA{R: 255, G: 68, B: 68, A: 255}
	ColorDivider    = color.RGBA{R: 64, G: 64, B: 128, A: 255}
	ColorMessage    = color.RGBA{R: 170, G: 255, B: 170, A: 255}
)

// Layout constants for the status indicator (circle + label) used for mDNS
// and VPN rows.
const (
	iconPadding    = 4  // left-edge gap before the circle
	iconTextGap    = 4  // gap between the circle's right edge and the text label
	mdnsHostOffset = 90 // additional x offset to the hostname text
	vpnDetailsOff  = 100 // additional x offset to VPN interface/peer text
)

// SystemDisplay owns the current system state and renders it to a Device.
// Update and Render may be called from different goroutines.
type SystemDisplay struct {
	dev Device

	mu   sync.RWMutex
	snap *sysinfo.Snapshot
}

// NewSystemDisplay wraps dev in a SystemDisplay ready for use.
func NewSystemDisplay(dev Device) *SystemDisplay {
	return &SystemDisplay{dev: dev}
}

// Update replaces the stored snapshot.  Safe to call concurrently with Render.
func (sd *SystemDisplay) Update(snap *sysinfo.Snapshot) {
	sd.mu.Lock()
	sd.snap = snap
	sd.mu.Unlock()
}

// Render draws the current snapshot to the device then flushes it.
// It is a no-op when Update has never been called.
func (sd *SystemDisplay) Render() error {
	sd.mu.RLock()
	snap := sd.snap
	sd.mu.RUnlock()
	if snap == nil {
		return nil
	}

	d := sd.dev
	w := d.Width()
	h := d.Height()
	lh := d.TextLineHeight()

	d.Clear(ColorBackground)

	y := lh - 2

	// ── Title ────────────────────────────────────────────────────────────
	d.DrawText(iconPadding, y, ColorTitle, "rip-bastion")
	y += lh
	d.DrawHLine(0, w-1, y, ColorDivider)
	y += 4

	// ── Network ──────────────────────────────────────────────────────────
	if snap.Network != nil {
		d.DrawText(iconPadding, y, ColorSectionHdr, "\u2500\u2500 Network")
		y += lh
		d.DrawText(iconPadding, y, ColorText, fmt.Sprintf("Interface : %s", snap.Network.InterfaceName))
		y += lh
		d.DrawText(iconPadding, y, ColorText, fmt.Sprintf("IP/Mask   : %s / %s", snap.Network.IP, snap.Network.Netmask))
		y += lh
		d.DrawText(iconPadding, y, ColorText, fmt.Sprintf("Gateway   : %s", snap.Network.Gateway))
		y += lh
		d.DrawText(iconPadding, y, ColorText, fmt.Sprintf("DNS       : %s", strings.Join(snap.Network.DNS, ", ")))
		y += lh
	} else if errMsg, ok := snap.Errors["network"]; ok {
		d.DrawText(iconPadding, y, ColorSectionHdr, "\u2500\u2500 Network")
		y += lh
		d.DrawText(iconPadding, y, ColorError, "Error: "+errMsg)
		y += lh
	}
	d.DrawHLine(0, w-1, y, ColorDivider)
	y += 4

	// ── mDNS ─────────────────────────────────────────────────────────────
	if snap.MDNS != nil {
		d.DrawText(iconPadding, y, ColorSectionHdr, "\u2500\u2500 mDNS")
		y += lh
		statText := "\u25cb Stopped"
		if snap.MDNS.Running {
			statText = "\u25cf Running"
		}
		textX := sd.drawStatusIndicator(snap.MDNS.Running, y, lh)
		d.DrawText(textX, y, statusColour(snap.MDNS.Running), statText)
		d.DrawText(textX+mdnsHostOffset, y, ColorText, snap.MDNS.Hostname)
		y += lh
	}
	d.DrawHLine(0, w-1, y, ColorDivider)
	y += 4

	// ── VPN ──────────────────────────────────────────────────────────────
	if snap.VPN != nil {
		d.DrawText(iconPadding, y, ColorSectionHdr, fmt.Sprintf("\u2500\u2500 VPN (%s)", snap.VPN.Name))
		y += lh
		statText := "\u25cb Disconnected"
		if snap.VPN.Connected {
			statText = "\u25cf Connected"
		}
		textX := sd.drawStatusIndicator(snap.VPN.Connected, y, lh)
		d.DrawText(textX, y, statusColour(snap.VPN.Connected), statText)
		if snap.VPN.Connected {
			d.DrawText(textX+vpnDetailsOff, y, ColorText, fmt.Sprintf("%s  %s", snap.VPN.Interface, snap.VPN.PeerIP))
		}
		y += lh
	}

	// ── Messages (pinned to the bottom 80 px) ────────────────────────────
	msgTop := h - 80
	if y < msgTop {
		d.DrawHLine(0, w-1, msgTop-2, ColorDivider)
		d.DrawText(iconPadding, msgTop+2, ColorSectionHdr, "\u2500\u2500 Messages")
		msgY := msgTop + 2 + lh
		for _, msg := range snap.Messages {
			if msgY > h-4 {
				break
			}
			d.DrawText(iconPadding, msgY, ColorMessage, "\u25b8 "+msg)
			msgY += lh
		}
	}

	return d.Flush()
}

// drawStatusIndicator draws a filled status circle on the current line at
// the standard left margin, and returns the x position for the text label
// that follows.  ok=true draws in ColorOK; ok=false in ColorError.
func (sd *SystemDisplay) drawStatusIndicator(ok bool, y, lineH int) int {
	cr := lineH/2 - 2
	cx := iconPadding + cr
	cy := y - cr
	sd.dev.DrawCircle(cx, cy, cr, statusColour(ok))
	return cx + cr + iconTextGap
}

// statusColour maps a boolean ok/running/connected flag to its palette entry.
func statusColour(ok bool) color.Color {
	if ok {
		return ColorOK
	}
	return ColorError
}

// Close releases the underlying device.
func (sd *SystemDisplay) Close() error {
	return sd.dev.Close()
}
