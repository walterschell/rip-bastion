// Package display defines a generic drawing device interface and the
// SystemDisplay, which owns all current system state and renders it onto a
// Device using only the device's primitive drawing methods.
package display

import (
	"fmt"
	"image"
	"image/color"
	"math"
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
// At() always returns color.Transparent.  This is safe for basicfont.Face7x13
// because it is a 1-bit bitmap font: each glyph pixel is either fully opaque
// (mask=0xFF) or fully transparent (mask=0).  Under the draw.Over operator
// those two cases simplify to dst=src and dst=dst respectively — neither reads
// the destination colour — so the stub At() is never consulted.
//
// Note: if this adapter is ever used with an anti-aliased font, At() would
// need to return the actual current pixel colour to produce correct blending.
type deviceCanvas struct {
	dev    Device
	bounds image.Rectangle
}

func (dc *deviceCanvas) Bounds() image.Rectangle     { return dc.bounds }
func (dc *deviceCanvas) ColorModel() color.Model     { return color.RGBAModel }
func (dc *deviceCanvas) At(x, y int) color.Color     { return color.Transparent }
func (dc *deviceCanvas) Set(x, y int, c color.Color) { dc.dev.SetPixel(x, y, c) }

// drawText renders text onto dev with its baseline at pixel (x, y) using
// colour c and the shared basicfont.Face7x13.  Font rendering is centralised
// here so that Device implementations never need to deal with font metrics,
// glyph masks, or the golang.org/x/image/font package.

func drawText(dev Device, x, y int, c color.Color, text string) (image.Rectangle, error) {
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
	bounds26_6, _ := dr.BoundString(text)
	var err error = nil
	bounds := image.Rectangle{
		Min: image.Point{X: bounds26_6.Min.X.Round(), Y: bounds26_6.Min.Y.Round()},
		Max: image.Point{X: bounds26_6.Max.X.Round(), Y: bounds26_6.Max.Y.Round()},
	}
	if !bounds.In(canvas.Bounds()) {
		err = fmt.Errorf("text bounds %v exceed canvas %v", bounds, canvas.Bounds())
	}
	dr.DrawString(text)
	return bounds, err
}

// drawSectionHeader renders a section title and draws a divider bar that
// starts just after the rendered title bounds.
func drawSectionHeader(dev Device, x, y int, title string) {
	bounds, _ := drawText(dev, x, y, ColorSectionHdr, title)

	lineStart := bounds.Max.X + 6
	lineEnd := dev.Width() - 1
	if lineStart > lineEnd {
		return
	}

	lineY := bounds.Min.Y + bounds.Dy()/2
	if lineY < 0 {
		lineY = 0
	}
	if lineY >= dev.Height() {
		lineY = dev.Height() - 1
	}

	dev.DrawHLine(lineStart, lineEnd, lineY, ColorDivider)
}

func sectionHeaderDividerY(baseline int, title string) int {
	bounds := textBounds(title)
	return baseline + bounds.Min.Y + bounds.Dy()/2
}

// drawSectionHeaderWithStatus renders a status circle immediately before the
// section title, then draws the title and divider.
func drawSectionHeaderWithStatus(dev Device, x, y int, title string, ok bool) {
	cr := TextLineHeight/2 - 2
	cx := x + cr
	cy := y - cr
	dev.DrawCircle(cx, cy, cr, statusColour(ok))

	titleX := cx + cr + iconTextGap
	drawSectionHeader(dev, titleX, y, title)
}

// Dashboard colour palette.
var (
	ColorBackground = color.RGBA{R: 0, G: 0, B: 0, A: 255}
	ColorTitle      = color.RGBA{R: 0, G: 212, B: 255, A: 255}
	ColorSectionHdr = color.RGBA{R: 136, G: 136, B: 136, A: 255}
	ColorText       = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	ColorRX         = color.RGBA{R: 255, G: 196, B: 64, A: 255}
	ColorTX         = color.RGBA{R: 64, G: 220, B: 255, A: 255}
	ColorOK         = color.RGBA{R: 0, G: 255, B: 0, A: 255}
	ColorError      = color.RGBA{R: 255, G: 0, B: 0, A: 255}
	ColorDivider    = color.RGBA{R: 64, G: 64, B: 128, A: 255}
	ColorMessage    = color.RGBA{R: 170, G: 255, B: 170, A: 255}
	ColorWarningBg  = color.RGBA{R: 255, G: 221, B: 0, A: 255}
	ColorWarningInk = color.RGBA{R: 0, G: 0, B: 0, A: 255}
	ColorWarningTxt = color.RGBA{R: 255, G: 0, B: 0, A: 255}
)

// Layout constants for the status indicator (circle + label) used for mDNS
// and VPN rows.
const (
	iconPadding = 4 // left-edge gap before the circle
	iconTextGap = 4 // gap between the circle's right edge and the text label
)

type statusTableEntry struct {
	label     string
	ok        bool
	textColor color.Color
}

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
	compact := h <= 320
	contentBottom := h - 4

	d.Clear(ColorBackground)

	y := lh - 2
	y += 2

	graphW := w / 3
	if graphW < 110 {
		graphW = 110
	}
	if graphW > 170 {
		graphW = 170
	}
	graphX := w - graphW - iconPadding
	leftX := iconPadding

	canDrawLine := func(baseline int) bool {
		return baseline <= contentBottom
	}

	sectionFits := func(topY int) bool {
		return topY+lh <= contentBottom
	}

	lowestRenderedY := -1
	markRendered := func(bottomY int) {
		if bottomY > lowestRenderedY {
			lowestRenderedY = bottomY
		}
	}
	alignSectionTop := func(topY int, title string) int {
		if lowestRenderedY < 0 {
			return topY
		}
		minDividerY := lowestRenderedY + 2
		dividerY := sectionHeaderDividerY(topY, title)
		if dividerY < minDividerY {
			topY += minDividerY - dividerY
		}
		return topY
	}

	// ── Network ──────────────────────────────────────────────────────────
	if snap.Network != nil && sectionFits(y) {
		y = alignSectionTop(y, "Network")
		drawSectionHeaderWithStatus(d, leftX, y, "Network", true)
		markRendered(sectionHeaderDividerY(y, "Network"))
		y += lh
		graphTop := y - 11
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorText, fmt.Sprintf("Interface           : %s", snap.Network.InterfaceName))
			markRendered(y)
		}
		y += lh
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorText, fmt.Sprintf("IP/CIDR             : %s", networkCIDR(snap.Network.IP, snap.Network.Netmask, snap.Network.CIDR)))
			markRendered(y)
		}
		y += lh
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorText, fmt.Sprintf("Gateway             : %s", emptyDash(snap.Network.Gateway)))
			markRendered(y)
		}
		y += lh
		if !compact {
			if canDrawLine(y) {
				drawText(d, leftX, y, ColorText, fmt.Sprintf("Detected External IP: %s", emptyDash(snap.Network.ExternalIP)))
				markRendered(y)
			}
			y += lh
			if canDrawLine(y) {
				drawText(d, leftX, y, ColorText, fmt.Sprintf("DNS                 : %s", emptyDash(strings.Join(snap.Network.DNS, ", "))))
				markRendered(y)
			}
			y += lh
			graphH := lh*5 - 4
			maxGraphH := contentBottom - graphTop
			if maxGraphH < graphH {
				graphH = maxGraphH
			}
			if graphH >= 24 {
				drawBandwidthGraph(d, graphX, graphTop, graphW, graphH, snap.NetworkRXBandwidthHistory, snap.NetworkTXBandwidthHistory, snap.NetworkRXKBps, snap.NetworkTXKBps)
				markRendered(graphTop + graphH - 1)
			}
		} else {
			graphH := lh*3 + 8
			maxGraphH := contentBottom - graphTop
			if maxGraphH < graphH {
				graphH = maxGraphH
			}
			if graphH >= 24 {
				drawBandwidthGraph(d, graphX, graphTop, graphW, graphH, snap.NetworkRXBandwidthHistory, snap.NetworkTXBandwidthHistory, snap.NetworkRXKBps, snap.NetworkTXKBps)
				markRendered(graphTop + graphH - 1)
			}
		}
	} else if errMsg, ok := snap.Errors["network"]; ok && sectionFits(y) {
		y = alignSectionTop(y, "Network")
		drawSectionHeaderWithStatus(d, leftX, y, "Network", false)
		markRendered(sectionHeaderDividerY(y, "Network"))
		y += lh
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorError, "Error: "+errMsg)
			markRendered(y)
		}
		y += lh
	}
	y += 2

	// ── VPN ──────────────────────────────────────────────────────────────
	if snap.VPN != nil && sectionFits(y) {
		vpnTitle := fmt.Sprintf("VPN (%s)", snap.VPN.Name)
		y = alignSectionTop(y, vpnTitle)
		drawSectionHeaderWithStatus(d, leftX, y, vpnTitle, snap.VPN.Connected)
		markRendered(sectionHeaderDividerY(y, vpnTitle))
		y += lh
		graphTop := y - 11
		ifaceLine := fmt.Sprintf("Iface: %s", emptyDash(snap.VPN.Interface))
		if snap.VPN.LocalCIDR != "" {
			ifaceLine += "  " + snap.VPN.LocalCIDR
		}
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorText, ifaceLine)
			markRendered(y)
		}
		y += lh
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorText, fmt.Sprintf("Peer / Node          : %s", emptyDash(snap.VPN.PeerIP)))
			markRendered(y)
		}
		y += lh
		graphH := lh*3 + 8
		if compact {
			graphH = lh*2 + 8
		}
		maxGraphH := contentBottom - graphTop
		if maxGraphH < graphH {
			graphH = maxGraphH
		}
		if graphH >= 24 {
			drawBandwidthGraph(d, graphX, graphTop, graphW, graphH, snap.VPNRXBandwidthHistory, snap.VPNTXBandwidthHistory, snap.VPNRXKBps, snap.VPNTXKBps)
			markRendered(graphTop + graphH - 1)
		}
	} else if errMsg, ok := snap.Errors["vpn"]; ok && sectionFits(y) {
		y = alignSectionTop(y, "VPN")
		drawSectionHeaderWithStatus(d, leftX, y, "VPN", false)
		markRendered(sectionHeaderDividerY(y, "VPN"))
		y += lh
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorError, "Error: "+errMsg)
			markRendered(y)
		}
		y += lh
	}
	y += 2

	// ── Services ─────────────────────────────────────────────────────────
	if sectionFits(y) {
		y = alignSectionTop(y, "Services")
		drawSectionHeader(d, leftX, y, "Services")
		markRendered(sectionHeaderDividerY(y, "Services"))
		y += lh

		mdnsRunning := snap.MDNS != nil && snap.MDNS.Running
		mdnsHost := "-"
		if snap.MDNS != nil && snap.MDNS.Hostname != "" {
			mdnsHost = snap.MDNS.Hostname
		}
		sshRunning := snap.SSH != nil && snap.SSH.Running
		entries := []statusTableEntry{
			{label: fmt.Sprintf("mDNS: %s", mdnsHost), ok: mdnsRunning, textColor: ColorText},
			{label: "SSH", ok: sshRunning, textColor: ColorText},
		}
		seenLabels := make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			seenLabels[entry.label] = struct{}{}
		}
		for _, proxySite := range snap.ProxySites {
			label := fmt.Sprintf("mDNS: %s", proxySite)
			if _, exists := seenLabels[label]; exists {
				continue
			}
			seenLabels[label] = struct{}{}
			entries = append(entries, statusTableEntry{label: label, ok: true, textColor: ColorText})
		}

		sep := " | "
		sepW := textBounds(sep).Dx()
		maxX := w - iconPadding
		x := leftX
		rowHasItems := false

		for _, entry := range entries {
			if !canDrawLine(y) {
				break
			}

			entryW := statusEntryWidth(lh, entry.label)
			neededW := entryW
			if rowHasItems {
				neededW += sepW
			}

			if rowHasItems && x+neededW > maxX {
				y += lh
				if !canDrawLine(y) {
					break
				}
				x = leftX
				rowHasItems = false
			}

			if rowHasItems {
				drawText(d, x, y, ColorSectionHdr, sep)
				x += sepW
			}

			textX := drawStatusIndicatorAt(d, x, y, lh, entry.ok)
			drawText(d, textX, y, entry.textColor, entry.label)
			markRendered(y)
			x = textX + textBounds(entry.label).Dx()
			rowHasItems = true
		}
		y += lh

		if errMsg, ok := snap.Errors["mdns"]; ok {
			if canDrawLine(y) {
				drawText(d, leftX, y, ColorError, "mDNS error: "+errMsg)
				markRendered(y)
			}
			y += lh
		}
		if errMsg, ok := snap.Errors["ssh"]; ok {
			if canDrawLine(y) {
				drawText(d, leftX, y, ColorError, "SSH error : "+errMsg)
				markRendered(y)
			}
			y += lh
		}
		if errMsg, ok := snap.Errors["proxy"]; ok {
			if canDrawLine(y) {
				drawText(d, leftX, y, ColorError, "Proxy error: "+errMsg)
				markRendered(y)
			}
			y += lh
		}

		y += 2
	}

	// ── Messages (consume remaining space) ───────────────────────────────
	if sectionFits(y) {
		y = alignSectionTop(y, "Messages")
		drawSectionHeader(d, leftX, y, "Messages")
		markRendered(sectionHeaderDividerY(y, "Messages"))
		y += lh
		for _, msg := range snap.Messages {
			if !canDrawLine(y) {
				break
			}
			drawText(d, leftX, y, ColorMessage, "- "+msg)
			markRendered(y)
			y += lh
		}
	}

	return d.Flush()
}

// RenderNeedsRebootNotice overwrites the display with a high-visibility
// reboot-required warning suitable for system shutdown.
func (sd *SystemDisplay) RenderNeedsRebootNotice() error {
	d := sd.dev
	w := d.Width()
	h := d.Height()
	if w <= 0 || h <= 0 {
		return nil
	}

	full := image.Rect(0, 0, w, h)
	border := warningBorderThickness(w, h)
	hashSpacing := border
	if hashSpacing < 10 {
		hashSpacing = 10
	}
	hashWidth := hashSpacing / 3
	if hashWidth < 3 {
		hashWidth = 3
	}

	fillRectWithDiagonalHashes(d, full, ColorWarningBg, ColorWarningInk, hashSpacing, hashWidth)

	inner := full.Inset(border)
	if inner.Dx() <= 0 || inner.Dy() <= 0 {
		return d.Flush()
	}
	d.DrawRect(inner.Min.X, inner.Min.Y, inner.Dx(), inner.Dy(), ColorBackground)

	padding := border / 2
	if padding < 8 {
		padding = 8
	}
	textArea := inner.Inset(padding)
	if textArea.Dx() <= 0 || textArea.Dy() <= 0 {
		return d.Flush()
	}

	lines, scale := chooseNeedsRebootLayout(textArea.Dx(), textArea.Dy())
	if scale < 1 {
		scale = 1
	}

	lineAdvance := basicfont.Face7x13.Metrics().Height.Ceil() * scale
	lineGap := 4 * scale
	totalHeight := len(lines) * lineAdvance
	if len(lines) > 1 {
		totalHeight += (len(lines) - 1) * lineGap
	}

	y := textArea.Min.Y + (textArea.Dy()-totalHeight)/2
	for idx, line := range lines {
		lineBounds := textBounds(line)
		lineWidth := lineBounds.Dx() * scale
		x := textArea.Min.X + (textArea.Dx()-lineWidth)/2
		_, _ = drawTextScaled(d, x, y, ColorWarningTxt, line, scale)
		y += lineAdvance
		if idx < len(lines)-1 {
			y += lineGap
		}
	}

	return d.Flush()
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func textBounds(text string) image.Rectangle {
	dr := &font.Drawer{Face: basicfont.Face7x13}
	bounds26_6, _ := dr.BoundString(text)
	return image.Rectangle{
		Min: image.Point{X: bounds26_6.Min.X.Round(), Y: bounds26_6.Min.Y.Round()},
		Max: image.Point{X: bounds26_6.Max.X.Round(), Y: bounds26_6.Max.Y.Round()},
	}
}

// drawTextScaled renders text at an integer pixel scale using the same drawing
// path as drawText.  It renders into a temporary ImageDevice at 1× first (so
// the proven NRGBA + font.Drawer path is used unchanged), then scales every
// set pixel up to scale×scale blocks on the target device.
func drawTextScaled(dev Device, x, y int, c color.Color, text string, scale int) (image.Rectangle, error) {
	if scale < 1 {
		scale = 1
	}
	bounds := textBounds(text)
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return image.Rect(x, y, x, y), nil
	}

	// Render at 1× into an off-screen ImageDevice so we can read back pixels.
	tmp := NewImageDevice(bounds.Dx(), bounds.Dy())
	// drawText places the baseline at (x, y); shift so baseline sits inside
	// the tmp buffer: baseline row = -bounds.Min.Y (the ascender offset).
	drawText(tmp, -bounds.Min.X, -bounds.Min.Y, c, text)

	// Scale every coloured pixel up onto the real device.
	for py := 0; py < bounds.Dy(); py++ {
		for px := 0; px < bounds.Dx(); px++ {
			col := tmp.img.NRGBAAt(px, py)
			if col.A == 0 {
				continue
			}
			dev.DrawRect(x+px*scale, y+py*scale, scale, scale, col)
		}
	}

	scaledBounds := image.Rect(x, y, x+bounds.Dx()*scale, y+bounds.Dy()*scale)
	canvasBounds := image.Rect(0, 0, dev.Width(), dev.Height())
	if !scaledBounds.In(canvasBounds) {
		return scaledBounds, fmt.Errorf("text bounds %v exceed canvas %v", scaledBounds, canvasBounds)
	}
	return scaledBounds, nil
}

func warningBorderThickness(w, h int) int {
	minDim := w
	if h < minDim {
		minDim = h
	}
	border := minDim / 10
	if border < 12 {
		border = 12
	}
	if border > 40 {
		border = 40
	}
	if border*2 >= minDim {
		border = minDim / 6
		if border < 2 {
			border = 2
		}
	}
	return border
}

func chooseNeedsRebootLayout(availW, availH int) ([]string, int) {
	candidates := [][]string{
		{"NEEDS REBOOT"},
		{"NEEDS", "REBOOT"},
	}

	baseLineHeight := basicfont.Face7x13.Metrics().Height.Ceil()
	const baseGap = 4

	bestLines := candidates[0]
	bestScale := 1
	bestArea := -1

	for _, lines := range candidates {
		maxWidth := 0
		totalHeight := len(lines) * baseLineHeight
		if len(lines) > 1 {
			totalHeight += (len(lines) - 1) * baseGap
		}
		for _, line := range lines {
			lineWidth := textBounds(line).Dx()
			if lineWidth > maxWidth {
				maxWidth = lineWidth
			}
		}
		if maxWidth <= 0 || totalHeight <= 0 {
			continue
		}

		scaleW := availW / maxWidth
		scaleH := availH / totalHeight
		scale := scaleW
		if scaleH < scale {
			scale = scaleH
		}
		if scale < 1 {
			scale = 1
		}

		area := maxWidth * totalHeight * scale * scale
		if scale > bestScale || (scale == bestScale && area > bestArea) {
			bestLines = lines
			bestScale = scale
			bestArea = area
		}
	}

	return bestLines, bestScale
}

func fillRectWithDiagonalHashes(dev Device, rect image.Rectangle, bg, hash color.Color, spacing, hashWidth int) {
	if spacing < 2 {
		spacing = 2
	}
	if hashWidth < 1 {
		hashWidth = 1
	}
	canvas := image.Rect(0, 0, dev.Width(), dev.Height())
	rect = rect.Intersect(canvas)
	if rect.Empty() {
		return
	}

	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			pattern := (x - rect.Min.X) + (y - rect.Min.Y)
			if pattern%spacing < hashWidth {
				dev.SetPixel(x, y, hash)
				continue
			}
			dev.SetPixel(x, y, bg)
		}
	}
}

func networkCIDR(ip, netmask, cidr string) string {
	if strings.TrimSpace(cidr) != "" {
		return cidr
	}
	ip = strings.TrimSpace(ip)
	netmask = strings.TrimSpace(netmask)
	if ip == "" {
		return "-"
	}
	if netmask == "" {
		return ip
	}
	parts := strings.Split(netmask, ".")
	if len(parts) != 4 {
		return ip
	}
	ones := 0
	for _, p := range parts {
		var v int
		_, err := fmt.Sscanf(p, "%d", &v)
		if err != nil || v < 0 || v > 255 {
			return ip
		}
		for i := 0; i < 8; i++ {
			if (v & (1 << i)) != 0 {
				ones++
			}
		}
	}
	return fmt.Sprintf("%s/%d", ip, ones)
}

func formatKBps(v float64) string {
	if v < 1024 {
		return fmt.Sprintf("%.1f KB/s", v)
	}
	return fmt.Sprintf("%.2f MB/s", v/1024.0)
}

func formatOverlayRate(v float64) string {
	if v < 10 {
		return fmt.Sprintf("%.1fK", v)
	}
	if v < 1024 {
		return fmt.Sprintf("%.0fK", v)
	}
	mb := v / 1024.0
	if mb < 10 {
		return fmt.Sprintf("%.1fM", mb)
	}
	if mb < 1024 {
		return fmt.Sprintf("%.0fM", mb)
	}
	gb := mb / 1024.0
	return fmt.Sprintf("%.1fG", gb)
}

func drawBandwidthGraph(dev Device, x, y, w, h int, rxSeries, txSeries []float64, rxKBps, txKBps float64) {
	if w <= 2 || h <= 4 {
		return
	}

	const leftGutterW = 28
	const bottomGutterH = 14

	plotX := x + leftGutterW
	plotW := w - leftGutterW
	plotH := h - bottomGutterH
	plotY := y
	if plotW <= 2 || plotH <= 4 {
		return
	}

	dev.DrawRect(x, y, w, h, color.RGBA{R: 10, G: 10, B: 18, A: 255})
	dev.DrawRect(x, y+h-bottomGutterH, w, bottomGutterH, color.RGBA{R: 12, G: 12, B: 20, A: 255})

	visibleCount := len(rxSeries)
	if len(txSeries) > visibleCount {
		visibleCount = len(txSeries)
	}

	if visibleCount == 0 {
		return
	}

	rxVisible := visibleTail(rxSeries, visibleCount)
	txVisible := visibleTail(txSeries, visibleCount)
	maxV := maxVisibleSeriesValue(rxVisible, txVisible)
	if maxV < 0.01 {
		maxV = 0.01
	}

	// Compute blended scale-line rows before drawing columns.
	scaleLines := computeScaleLines(maxV, plotH)

	for col := 0; col < plotW; col++ {
		sampleSlot := (col * visibleCount) / plotW
		if sampleSlot >= visibleCount {
			sampleSlot = visibleCount - 1
		}
		rxV := sampleAtVisibleSlot(rxSeries, visibleCount, sampleSlot)
		txV := sampleAtVisibleSlot(txSeries, visibleCount, sampleSlot)
		rxH := scaledBarHeight(rxV, maxV, plotH)
		txH := scaledBarHeight(txV, maxV, plotH)
		drawGraphColumn(dev, plotX+col, plotY, plotH, rxH, txH, scaleLines)
	}

	drawGraphDurationAxis(dev, plotX, plotY, plotW, plotH)
	drawScaleLabelGutter(dev, x, plotY, leftGutterW, plotH, maxV)
	drawGraphAnnotationGutter(dev, x, y+h-bottomGutterH, w, bottomGutterH, visibleCount, rxKBps, txKBps)
}

// drawGraphDurationAxis draws only the axis line and tick marks at the bottom
// of the plot. Text annotations live in the bottom gutter.
func drawGraphDurationAxis(dev Device, x, y, w, plotH int) {
	if w <= 0 || plotH <= 0 {
		return
	}
	axisY := y + plotH - 1
	axisColor := color.RGBA{R: 48, G: 48, B: 80, A: 255}
	dev.DrawHLine(x, x+w-1, axisY, axisColor)

	tickXs := []int{x, x + w/4, x + w/2, x + (3*w)/4, x + w - 1}
	for _, tickX := range tickXs {
		dev.DrawRect(tickX, axisY-3, 1, 3, axisColor)
	}

	leftTickX := x
	if leftTickX > 0 {
		dev.DrawRect(leftTickX-1, axisY-3, 1, 3, axisColor)
	}
}

func drawGraphAnnotationGutter(dev Device, x, y, w, h, samples int, rxKBps, txKBps float64) {
	if w <= 0 || h <= 0 {
		return
	}
	baseline := y + h - 2
	leftText := fmt.Sprintf("RX %s", formatOverlayRate(rxKBps))
	centerText := formatWindowDuration(samples)
	rightText := fmt.Sprintf("TX %s", formatOverlayRate(txKBps))

	drawText(dev, x+2, baseline, ColorRX, leftText)

	centerW := len(centerText) * 7
	centerX := x + (w-centerW)/2
	if centerX < x+2 {
		centerX = x + 2
	}
	drawText(dev, centerX, baseline, ColorSectionHdr, centerText)

	rightW := len(rightText) * 7
	rightX := x + w - rightW - 2
	if rightX < x+2 {
		rightX = x + 2
	}
	drawText(dev, rightX, baseline, ColorTX, rightText)
}

func drawScaleLabelGutter(dev Device, gutterX, plotY, gutterW, plotH int, maxV float64) {
	if gutterW <= 0 || plotH <= 0 {
		return
	}

	type scaleLabel struct {
		text string
		row  int
	}

	labels := []scaleLabel{
		{text: formatScaleRate(maxV), row: 0},
		{text: "G", row: scaleRow(1024.0*1024.0, maxV, plotH)},
		{text: "M", row: scaleRow(1024.0, maxV, plotH)},
		{text: "K", row: scaleRow(1.0, maxV, plotH)},
		{text: "B", row: scaleRow(1.0/1024.0, maxV, plotH)},
	}

	lastBottom := -1 << 30
	for idx, label := range labels {
		if idx != 0 && label.row < 0 {
			continue
		}

		baseline := plotY + 9
		if idx != 0 {
			baseline = plotY + label.row + 5
		}

		top := baseline - 13
		bottom := baseline
		if top <= lastBottom {
			continue
		}

		drawText(dev, gutterX+2, baseline, ColorSectionHdr, label.text)
		lastBottom = bottom
	}
}

// computeScaleLines returns a map from plot-row-from-top to darkening factor
// (0 = black, 1 = unchanged) for all gridlines that should be rendered.
// Major gridlines (1 B/s, 1 KB/s, 1 MB/s, 1 GB/s) use factor 0.15 (near-black).
// Sub-interval selection:
//   - If at least one of {lastMajor×10, lastMajor×100} fits below maxV, render those.
//   - Otherwise render {prevMajor×10, prevMajor×100} (between the previous two majors).
func computeScaleLines(maxV float64, plotH int) map[int]float64 {
	lines := make(map[int]float64)
	majors := [4]float64{1.0 / 1024.0, 1.0, 1024.0, 1024.0 * 1024.0}

	var visibleMajors []float64
	for _, mv := range majors {
		if r := scaleRow(mv, maxV, plotH); r >= 0 {
			lines[r] = 0.15
			visibleMajors = append(visibleMajors, mv)
		}
	}

	if len(visibleMajors) == 0 {
		return lines
	}

	lastMajor := visibleMajors[len(visibleMajors)-1]
	sub10 := lastMajor * 10
	sub100 := lastMajor * 100

	if sub10 <= maxV || sub100 <= maxV {
		// Condition 1: sub-intervals after the last full major.
		if sub10 <= maxV {
			if r := scaleRow(sub10, maxV, plotH); r >= 0 {
				lines[r] = 0.55
			}
		}
		if sub100 <= maxV {
			if r := scaleRow(sub100, maxV, plotH); r >= 0 {
				lines[r] = 0.80
			}
		}
	} else {
		// Condition 2: sub-intervals between the last major and the one before it.
		var prevMajor float64
		if len(visibleMajors) >= 2 {
			prevMajor = visibleMajors[len(visibleMajors)-2]
		} else {
			for i, mv := range majors {
				if mv == lastMajor && i > 0 {
					prevMajor = majors[i-1]
					break
				}
			}
		}
		if prevMajor > 0 {
			if r := scaleRow(prevMajor*10, maxV, plotH); r >= 0 {
				lines[r] = 0.55
			}
			if r := scaleRow(prevMajor*100, maxV, plotH); r >= 0 {
				lines[r] = 0.80
			}
		}
	}

	return lines
}

// scaleRow converts a data value to a plot row index from the top.
// Returns -1 if the row falls at or outside the plot boundary.
func scaleRow(value, maxV float64, plotH int) int {
	if value <= 0 || maxV <= 0 || plotH <= 0 || value > maxV {
		return -1
	}
	ratio := math.Log1p(value) / math.Log1p(maxV)
	barH := int(math.Round(ratio * float64(plotH)))
	if barH <= 0 || barH >= plotH {
		return -1
	}
	return plotH - barH
}

func scaledBarHeight(value, maxValue float64, plotH int) int {
	if value <= 0 || plotH <= 0 {
		return 0
	}
	ratio := math.Log1p(value) / math.Log1p(maxValue)
	barH := int(math.Round(ratio * float64(plotH)))
	if barH < 1 {
		barH = 1
	}
	if barH > plotH {
		barH = plotH
	}
	return barH
}

func maxVisibleSeriesValue(rxVisible, txVisible []float64) float64 {
	maxValue := 0.0
	for _, v := range rxVisible {
		if v > maxValue {
			maxValue = v
		}
	}
	for _, v := range txVisible {
		if v > maxValue {
			maxValue = v
		}
	}
	return maxValue
}

func visibleTail(series []float64, visibleCount int) []float64 {
	if visibleCount <= 0 || len(series) == 0 {
		return nil
	}
	start := len(series) - visibleCount
	if start < 0 {
		start = 0
	}
	return series[start:]
}

func sampleAtVisibleSlot(series []float64, visibleCount, slot int) float64 {
	if len(series) == 0 || visibleCount <= 0 || slot < 0 || slot >= visibleCount {
		return 0
	}
	start := len(series) - visibleCount
	if start < 0 {
		start = 0
	}
	idx := start + slot
	if idx < 0 || idx >= len(series) {
		return 0
	}
	return series[idx]
}

func drawGraphColumn(dev Device, x, y, plotH, rxH, txH int, lines map[int]float64) {
	if plotH <= 0 {
		return
	}
	bg := color.RGBA{10, 10, 18, 255}
	blended := blendRGBA(ColorRX, ColorTX)
	for row := 0; row < plotH; row++ {
		fromBottom := plotH - row
		hasRX := rxH >= fromBottom
		hasTX := txH >= fromBottom

		var c color.RGBA
		switch {
		case hasRX && hasTX:
			c = blended
		case hasRX:
			c = ColorRX
		case hasTX:
			c = ColorTX
		default:
			c = bg
		}

		if factor, ok := lines[row]; ok {
			c = darkenRGBA(c, factor)
		}
		dev.SetPixel(x, y+row, c)
	}
}

func darkenRGBA(c color.RGBA, factor float64) color.RGBA {
	return color.RGBA{
		R: uint8(float64(c.R) * factor),
		G: uint8(float64(c.G) * factor),
		B: uint8(float64(c.B) * factor),
		A: 255,
	}
}

func blendRGBA(a, b color.RGBA) color.RGBA {
	return color.RGBA{
		R: uint8((uint16(a.R) + uint16(b.R)) / 2),
		G: uint8((uint16(a.G) + uint16(b.G)) / 2),
		B: uint8((uint16(a.B) + uint16(b.B)) / 2),
		A: 255,
	}
}

func formatWindowDuration(samples int) string {
	seconds := samples * 5
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds = seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes = minutes % 60
	return fmt.Sprintf("%dh%02dm", hours, minutes)
}

func formatScaleRate(v float64) string {
	if v <= 0 {
		return "0"
	}
	bytesPerSec := v * 1024.0
	if bytesPerSec < 1024 {
		return fmt.Sprintf("%.0fB", bytesPerSec)
	}
	if v < 1024 {
		return fmt.Sprintf("%.0fK", v)
	}
	mb := v / 1024.0
	if mb < 1024 {
		return fmt.Sprintf("%.1fM", mb)
	}
	gb := mb / 1024.0
	return fmt.Sprintf("%.1fG", gb)
}

// drawStatusIndicator draws a filled status circle on the current line at
// the standard left margin, and returns the x position for the text label
// that follows.  ok=true draws in ColorOK; ok=false in ColorError.
func (sd *SystemDisplay) drawStatusIndicator(ok bool, y, lineH int) int {
	return drawStatusIndicatorAt(sd.dev, iconPadding, y, lineH, ok)
}

func drawStatusIndicatorAt(dev Device, x, y, lineH int, ok bool) int {
	cr := lineH/2 - 2
	cx := x + cr
	cy := y - cr
	dev.DrawCircle(cx, cy, cr, statusColour(ok))
	return cx + cr + iconTextGap
}

func statusEntryWidth(lineH int, label string) int {
	cr := lineH/2 - 2
	return cr*2 + iconTextGap + textBounds(label).Dx()
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
