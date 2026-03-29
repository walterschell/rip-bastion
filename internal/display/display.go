// Package display defines a generic drawing device interface and the
// SystemDisplay, which owns all current system state and renders it onto a
// Device using only the device's primitive drawing methods.
package display

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/walterschell/rip-bastion/internal/sysinfo"
)

// TextLineHeight is the vertical distance (in pixels) between successive text
// baselines when using the default font (basicfont.Face7x13), including
// leading.  All Device backends share this value because font metrics live
// here, not in the hardware layer.
const TextLineHeight = 16

// Device is a generic drawing surface.  Rendering backends (DRM/KMS, Fyne
// canvas, etc.) implement this interface so that SystemDisplay can operate on
// any physical or virtual output without knowing its internals.
//
// Font rendering is intentionally absent from this interface: it is handled
// once by the display package (see drawText) so that backends only need to
// implement pixel-level and geometry primitives.
type Device interface {
	// Dimensions of the drawable area in pixels.
	Width() int
	Height() int

	// SetPixel sets a single pixel at (x, y) to colour c.  This is the only
	// primitive required for font rendering; all text is rasterised by the
	// display package and delivered to the device one pixel at a time.
	SetPixel(x, y int, c color.Color)

	// Clear fills the entire surface with the given colour.
	Clear(c color.Color)

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

// deviceCanvas adapts a Device into a draw.Image so that the
// golang.org/x/image/font renderer can draw directly onto any Device backend
// without the backend knowing anything about fonts.
//
// At() always returns color.Transparent.  This is correct for binary bitmap
// fonts (basicfont.Face7x13) where every glyph pixel is either fully opaque
// or fully transparent, so the Over compositing operator never needs to read
// the destination.
type deviceCanvas struct {
	dev    Device
	bounds image.Rectangle
}

func (dc *deviceCanvas) Bounds() image.Rectangle              { return dc.bounds }
func (dc *deviceCanvas) ColorModel() color.Model              { return color.RGBAModel }
func (dc *deviceCanvas) At(x, y int) color.Color              { return color.Transparent }
func (dc *deviceCanvas) Set(x, y int, c color.Color)          { dc.dev.SetPixel(x, y, c) }

// drawText renders text onto dev with its baseline at pixel (x, y) using
// colour c and the shared basicfont.Face7x13.  Font rendering is centralised
// here so that Device implementations never need to deal with font metrics,
// glyph masks, or the golang.org/x/image/font package.
func drawText(dev Device, x, y int, c color.Color, text string) {
	canvas := &deviceCanvas{
		dev:    dev,
		bounds: image.Rect(0, 0, dev.Width(), dev.Height()),
	}
	dr := &font.Drawer{
		Dst:  canvas,
		Src:  image.NewUniform(c),
		Face: basicfont.Face7x13,
		Dot:  fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)},
	}
	dr.DrawString(text)
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
	lh := TextLineHeight

	d.Clear(ColorBackground)

	y := lh - 2

	// ── Title ────────────────────────────────────────────────────────────
	drawText(d, iconPadding, y, ColorTitle, "rip-bastion")
	y += lh
	d.DrawHLine(0, w-1, y, ColorDivider)
	y += 4

	// ── Network ──────────────────────────────────────────────────────────
	if snap.Network != nil {
		drawText(d, iconPadding, y, ColorSectionHdr, "\u2500\u2500 Network")
		y += lh
		drawText(d, iconPadding, y, ColorText, fmt.Sprintf("Interface : %s", snap.Network.InterfaceName))
		y += lh
		drawText(d, iconPadding, y, ColorText, fmt.Sprintf("IP/Mask   : %s / %s", snap.Network.IP, snap.Network.Netmask))
		y += lh
		drawText(d, iconPadding, y, ColorText, fmt.Sprintf("Gateway   : %s", snap.Network.Gateway))
		y += lh
		drawText(d, iconPadding, y, ColorText, fmt.Sprintf("DNS       : %s", strings.Join(snap.Network.DNS, ", ")))
		y += lh
	} else if errMsg, ok := snap.Errors["network"]; ok {
		drawText(d, iconPadding, y, ColorSectionHdr, "\u2500\u2500 Network")
		y += lh
		drawText(d, iconPadding, y, ColorError, "Error: "+errMsg)
		y += lh
	}
	d.DrawHLine(0, w-1, y, ColorDivider)
	y += 4

	// ── mDNS ─────────────────────────────────────────────────────────────
	if snap.MDNS != nil {
		drawText(d, iconPadding, y, ColorSectionHdr, "\u2500\u2500 mDNS")
		y += lh
		statText := "\u25cb Stopped"
		if snap.MDNS.Running {
			statText = "\u25cf Running"
		}
		textX := sd.drawStatusIndicator(snap.MDNS.Running, y, lh)
		drawText(d, textX, y, statusColour(snap.MDNS.Running), statText)
		drawText(d, textX+mdnsHostOffset, y, ColorText, snap.MDNS.Hostname)
		y += lh
	}
	d.DrawHLine(0, w-1, y, ColorDivider)
	y += 4

	// ── VPN ──────────────────────────────────────────────────────────────
	if snap.VPN != nil {
		drawText(d, iconPadding, y, ColorSectionHdr, fmt.Sprintf("\u2500\u2500 VPN (%s)", snap.VPN.Name))
		y += lh
		statText := "\u25cb Disconnected"
		if snap.VPN.Connected {
			statText = "\u25cf Connected"
		}
		textX := sd.drawStatusIndicator(snap.VPN.Connected, y, lh)
		drawText(d, textX, y, statusColour(snap.VPN.Connected), statText)
		if snap.VPN.Connected {
			drawText(d, textX+vpnDetailsOff, y, ColorText, fmt.Sprintf("%s  %s", snap.VPN.Interface, snap.VPN.PeerIP))
		}
		y += lh
	}

	// ── Messages (pinned to the bottom 80 px) ────────────────────────────
	msgTop := h - 80
	if y < msgTop {
		d.DrawHLine(0, w-1, msgTop-2, ColorDivider)
		drawText(d, iconPadding, msgTop+2, ColorSectionHdr, "\u2500\u2500 Messages")
		msgY := msgTop + 2 + lh
		for _, msg := range snap.Messages {
			if msgY > h-4 {
				break
			}
			drawText(d, iconPadding, msgY, ColorMessage, "\u25b8 "+msg)
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
