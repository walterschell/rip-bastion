//go:build rpi

package main

import (
	"image/color"
	"log"
	"os"
	"strings"

	"github.com/walterschell/rip-bastion/internal/spi"
)

var (
	bgColor        = color.RGBA{R: 0, G: 0, B: 0, A: 255}
	topBorderColor = color.RGBA{R: 255, G: 0, B: 0, A: 255}
	botBorderColor = color.RGBA{R: 255, G: 0, B: 255, A: 255}
)

func hsvToRGB(h float64) color.RGBA {
	for h < 0 {
		h += 360
	}
	for h >= 360 {
		h -= 360
	}

	c := 1.0
	x := c * (1 - abs(mod(h/60.0, 2)-1))

	var r1, g1, b1 float64
	switch {
	case h < 60:
		r1, g1, b1 = c, x, 0
	case h < 120:
		r1, g1, b1 = x, c, 0
	case h < 180:
		r1, g1, b1 = 0, c, x
	case h < 240:
		r1, g1, b1 = 0, x, c
	case h < 300:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}

	return color.RGBA{
		R: uint8(r1 * 255),
		G: uint8(g1 * 255),
		B: uint8(b1 * 255),
		A: 255,
	}
}

func hslToRGB(h, s, l float64) color.RGBA {
	for h < 0 {
		h += 360
	}
	for h >= 360 {
		h -= 360
	}

	c := (1 - abs(2*l-1)) * s
	x := c * (1 - abs(mod(h/60.0, 2)-1))
	m := l - c/2

	var r1, g1, b1 float64
	switch {
	case h < 60:
		r1, g1, b1 = c, x, 0
	case h < 120:
		r1, g1, b1 = x, c, 0
	case h < 180:
		r1, g1, b1 = 0, c, x
	case h < 240:
		r1, g1, b1 = 0, x, c
	case h < 300:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}

	return color.RGBA{
		R: uint8((r1 + m) * 255),
		G: uint8((g1 + m) * 255),
		B: uint8((b1 + m) * 255),
		A: 255,
	}
}

func mod(a, b float64) float64 {
	return a - float64(int(a/b))*b
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func gradientColor(y, h int) color.RGBA {
	if h <= 1 {
		return topBorderColor
	}
	t := float64(y) / float64(h-1)
	return hsvToRGB(300.0 * t)
}

func drawBorder(d interface {
	Width() int
	Height() int
	SetPixel(x, y int, c color.Color)
	Clear(c color.Color)
	DrawRect(x, y, w, h int, c color.Color)
	DrawHLine(x0, x1, y int, c color.Color)
	Flush() error
}) error {
	w := d.Width()
	h := d.Height()

	// Top and bottom edges.
	d.DrawHLine(0, w-1, 0, topBorderColor)
	d.DrawHLine(0, w-1, h-1, gradientColor(h-1, h))

	// Left and right edges (1px wide) with full vertical gradient.
	for y := 0; y < h; y++ {
		c := gradientColor(y, h)
		d.SetPixel(0, y, c)
		d.SetPixel(w-1, y, c)
	}

	// Inset calibration matrix with a thick black buffer between it and the border.
	gap := 8
	innerX0 := gap + 1
	innerY0 := gap + 1
	innerX1 := w - gap - 2
	innerY1 := h - gap - 2
	innerW := innerX1 - innerX0 + 1
	innerH := innerY1 - innerY0 + 1
	if innerW > 0 && innerH > 0 {
		stripH := innerH / 7
		if stripH < 12 {
			stripH = 12
		}
		stripGap := 4
		tileY1 := innerY1 - stripH - stripGap
		if tileY1 < innerY0 {
			tileY1 = innerY1
			stripH = 0
			stripGap = 0
		}
		tileHAvail := tileY1 - innerY0 + 1

		const cols = 6
		const rows = 4
		const gutter = 2
		tileW := (innerW - gutter*(cols-1)) / cols
		tileH := (tileHAvail - gutter*(rows-1)) / rows
		if tileW > 0 && tileH > 0 {
			for row := 0; row < rows; row++ {
				lightness := 0.25 + float64(row)*(0.5/float64(rows-1))
				for col := 0; col < cols; col++ {
					hue := 360.0 * float64(col) / float64(cols)
					tileColor := hslToRGB(hue, 1.0, lightness)
					xStart := innerX0 + col*(tileW+gutter)
					yStart := innerY0 + row*(tileH+gutter)
					for y := 0; y < tileH; y++ {
						for x := 0; x < tileW; x++ {
							d.SetPixel(xStart+x, yStart+y, tileColor)
						}
					}
				}
			}

			// Add a grayscale strip in a dedicated footer below the tile grid.
			grayY0 := tileY1 + stripGap + 1
			if stripH > 0 && grayY0 <= innerY1 {
				for x := innerX0; x <= innerX1; x++ {
					var v uint8
					if innerW <= 1 {
						v = 255
					} else {
						v = uint8((255 * (x - innerX0)) / (innerW - 1))
					}
					c := color.RGBA{R: v, G: v, B: v, A: 255}
					for y := grayY0; y <= innerY1; y++ {
						d.SetPixel(x, y, c)
					}
				}
			}
		}
	}

	return d.Flush()
}

func drawQuadrants(d interface {
	Width() int
	Height() int
	Clear(c color.Color)
	DrawRect(x, y, w, h int, c color.Color)
	Flush() error
}) error {
	w := d.Width()
	h := d.Height()
	gap := 8
	midX := w / 2
	midY := h / 2

	d.Clear(bgColor)

	leftW := midX - gap/2
	rightX := midX + (gap+1)/2
	rightW := w - rightX
	topH := midY - gap/2
	bottomY := midY + (gap+1)/2
	bottomH := h - bottomY

	if leftW > 0 && topH > 0 {
		d.DrawRect(0, 0, leftW, topH, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	}
	if rightW > 0 && topH > 0 {
		d.DrawRect(rightX, 0, rightW, topH, color.RGBA{R: 0, G: 255, B: 0, A: 255})
	}
	if leftW > 0 && bottomH > 0 {
		d.DrawRect(0, bottomY, leftW, bottomH, color.RGBA{R: 0, G: 0, B: 255, A: 255})
	}
	if rightW > 0 && bottomH > 0 {
		d.DrawRect(rightX, bottomY, rightW, bottomH, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	}

	return d.Flush()
}

func main() {
	if _, ok := os.LookupEnv("RIP_SPI_BATCH_ROWS"); !ok {
		_ = os.Setenv("RIP_SPI_BATCH_ROWS", "1")
	}
	if _, ok := os.LookupEnv("RIP_SPI_STREAM_FULL_FRAME"); !ok {
		_ = os.Setenv("RIP_SPI_STREAM_FULL_FRAME", "1")
	}
	if _, ok := os.LookupEnv("RIP_SPI_COLUMN_MAJOR"); !ok {
		_ = os.Setenv("RIP_SPI_COLUMN_MAJOR", "0")
	}
	if _, ok := os.LookupEnv("RIP_SPI_PACK_PIXELS16"); !ok {
		_ = os.Setenv("RIP_SPI_PACK_PIXELS16", "0")
	}
	if _, ok := os.LookupEnv("RIP_SPI_SKIP_INIT_FLUSH"); !ok {
		_ = os.Setenv("RIP_SPI_SKIP_INIT_FLUSH", "1")
	}

	dev, err := spi.NewFromEnv()
	if err != nil {
		log.Fatalf("SPI display init failed: %v", err)
	}
	defer dev.Close()

	dev.Clear(bgColor)
	pattern := strings.ToLower(strings.TrimSpace(os.Getenv("RIP_DEBUG_PATTERN")))
	if pattern == "" {
		pattern = "border"
	}

	var errDraw error
	switch pattern {
	case "quadrants":
		errDraw = drawQuadrants(dev)
	case "border":
		errDraw = drawBorder(dev)
	default:
		log.Fatalf("unknown RIP_DEBUG_PATTERN=%q", pattern)
	}
	if errDraw != nil {
		log.Fatalf("debug draw failed: %v", errDraw)
	}
	log.Printf("Pattern %q drawn on %dx%d display", pattern, dev.Width(), dev.Height())
}
