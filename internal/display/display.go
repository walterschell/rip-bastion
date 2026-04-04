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

// Dashboard colour palette.
var (
	ColorBackground = color.RGBA{R: 0, G: 0, B: 0, A: 255}
	ColorTitle      = color.RGBA{R: 0, G: 212, B: 255, A: 255}
	ColorSectionHdr = color.RGBA{R: 136, G: 136, B: 136, A: 255}
	ColorText       = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	ColorRX         = color.RGBA{R: 255, G: 196, B: 64, A: 255}
	ColorTX         = color.RGBA{R: 64, G: 220, B: 255, A: 255}
	ColorOK         = color.RGBA{R: 0, G: 255, B: 136, A: 255}
	ColorError      = color.RGBA{R: 255, G: 68, B: 68, A: 255}
	ColorDivider    = color.RGBA{R: 64, G: 64, B: 128, A: 255}
	ColorMessage    = color.RGBA{R: 170, G: 255, B: 170, A: 255}
)

// Layout constants for the status indicator (circle + label) used for mDNS
// and VPN rows.
const (
	iconPadding = 4 // left-edge gap before the circle
	iconTextGap = 4 // gap between the circle's right edge and the text label
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
	compact := h <= 320
	msgBoxH := 80
	if compact {
		msgBoxH = 64
	}
	msgTop := h - msgBoxH
	contentBottom := msgTop - 4

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

	// ── Network ──────────────────────────────────────────────────────────
	if snap.Network != nil && sectionFits(y) {
		drawSectionHeader(d, leftX, y, "Network")
		y += lh
		graphTop := y - 11
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorText, fmt.Sprintf("Interface           : %s", snap.Network.InterfaceName))
		}
		y += lh
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorText, fmt.Sprintf("IP/CIDR             : %s", networkCIDR(snap.Network.IP, snap.Network.Netmask, snap.Network.CIDR)))
		}
		y += lh
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorText, fmt.Sprintf("Gateway             : %s", emptyDash(snap.Network.Gateway)))
		}
		y += lh
		if !compact {
			if canDrawLine(y) {
				drawText(d, leftX, y, ColorText, fmt.Sprintf("Detected External IP: %s", emptyDash(snap.Network.ExternalIP)))
			}
			y += lh
			if canDrawLine(y) {
				drawText(d, leftX, y, ColorText, fmt.Sprintf("DNS                 : %s", emptyDash(strings.Join(snap.Network.DNS, ", "))))
			}
			y += lh
			graphH := lh*5 - 4
			maxGraphH := contentBottom - graphTop
			if maxGraphH < graphH {
				graphH = maxGraphH
			}
			if graphH >= 24 {
				drawBandwidthGraph(d, graphX, graphTop, graphW, graphH, snap.NetworkRXBandwidthHistory, snap.NetworkTXBandwidthHistory, snap.NetworkRXKBps, snap.NetworkTXKBps)
			}
		} else {
			graphH := lh*3 + 8
			maxGraphH := contentBottom - graphTop
			if maxGraphH < graphH {
				graphH = maxGraphH
			}
			if graphH >= 24 {
				drawBandwidthGraph(d, graphX, graphTop, graphW, graphH, snap.NetworkRXBandwidthHistory, snap.NetworkTXBandwidthHistory, snap.NetworkRXKBps, snap.NetworkTXKBps)
			}
		}
	} else if errMsg, ok := snap.Errors["network"]; ok && sectionFits(y) {
		drawSectionHeader(d, leftX, y, "Network")
		y += lh
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorError, "Error: "+errMsg)
		}
		y += lh
	}
	y += 4

	// ── Services ─────────────────────────────────────────────────────────
	if sectionFits(y) {
		drawSectionHeader(d, leftX, y, "Services")
		y += lh

		mdnsRunning := snap.MDNS != nil && snap.MDNS.Running
		mdnsHost := "-"
		if snap.MDNS != nil && snap.MDNS.Hostname != "" {
			mdnsHost = snap.MDNS.Hostname
		}
		mdnsTextX := sd.drawStatusIndicator(mdnsRunning, y, lh)
		if canDrawLine(y) {
			drawText(d, mdnsTextX, y, ColorText, fmt.Sprintf("mDNS : %s", mdnsHost))
		}
		y += lh

		sshRunning := snap.SSH != nil && snap.SSH.Running
		sshTextX := sd.drawStatusIndicator(sshRunning, y, lh)
		if canDrawLine(y) {
			drawText(d, sshTextX, y, ColorText, "SSH")
		}
		y += lh

		if errMsg, ok := snap.Errors["mdns"]; ok {
			if canDrawLine(y) {
				drawText(d, leftX, y, ColorError, "mDNS error: "+errMsg)
			}
			y += lh
		}
		if errMsg, ok := snap.Errors["ssh"]; ok {
			if canDrawLine(y) {
				drawText(d, leftX, y, ColorError, "SSH error : "+errMsg)
			}
			y += lh
		}

		y += 4
	}

	// ── VPN ──────────────────────────────────────────────────────────────
	if snap.VPN != nil && sectionFits(y) {
		drawSectionHeader(d, leftX, y, fmt.Sprintf("VPN (%s)", snap.VPN.Name))
		y += lh
		graphTop := y - 11
		textX := sd.drawStatusIndicator(snap.VPN.Connected, y, lh)
		ifaceLine := fmt.Sprintf("Iface: %s", emptyDash(snap.VPN.Interface))
		if snap.VPN.LocalCIDR != "" {
			ifaceLine += "  " + snap.VPN.LocalCIDR
		}
		if canDrawLine(y) {
			drawText(d, textX, y, ColorText, ifaceLine)
		}
		y += lh
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorText, fmt.Sprintf("Peer                 : %s", emptyDash(snap.VPN.PeerIP)))
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
		}
	} else if errMsg, ok := snap.Errors["vpn"]; ok && sectionFits(y) {
		drawSectionHeader(d, leftX, y, "VPN")
		y += lh
		if canDrawLine(y) {
			drawText(d, leftX, y, ColorError, "Error: "+errMsg)
		}
		y += lh
	}

	// ── Messages (pinned to the bottom 80 px) ────────────────────────────
	drawSectionHeader(d, leftX, msgTop+2, "Messages")
	msgY := msgTop + 2 + lh
	for _, msg := range snap.Messages {
		if msgY > h-4 {
			break
		}
		drawText(d, leftX, msgY, ColorMessage, "- "+msg)
		msgY += lh
	}

	return d.Flush()
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
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
