package display

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

// ImageDevice is a display.Device backed by an in-memory image.NRGBA.
// Flush is a no-op; call SavePNG to persist the rendered frame to disk.
// This is useful for generating screenshots and documentation renders
// without requiring physical display hardware.
type ImageDevice struct {
	img    *image.NRGBA
	width  int
	height int
}

// NewImageDevice creates an ImageDevice with the given pixel dimensions.
func NewImageDevice(width, height int) *ImageDevice {
	return &ImageDevice{
		img:    image.NewNRGBA(image.Rect(0, 0, width, height)),
		width:  width,
		height: height,
	}
}

// Width returns the image width in pixels.
func (d *ImageDevice) Width() int { return d.width }

// Height returns the image height in pixels.
func (d *ImageDevice) Height() int { return d.height }

// toNRGBA converts any color.Color to the pre-multiplied NRGBA form used
// by the backing image, performing the standard 16→8 bit shift.
func toNRGBA(c color.Color) color.NRGBA {
	r32, g32, b32, a32 := c.RGBA()
	return color.NRGBA{
		R: uint8(r32 >> 8),
		G: uint8(g32 >> 8),
		B: uint8(b32 >> 8),
		A: uint8(a32 >> 8),
	}
}

// SetPixel sets the pixel at (x, y) to colour c.
func (d *ImageDevice) SetPixel(x, y int, c color.Color) {
	if x < 0 || x >= d.width || y < 0 || y >= d.height {
		return
	}
	d.img.SetNRGBA(x, y, toNRGBA(c))
}

// Clear fills the entire image with colour c.
func (d *ImageDevice) Clear(c color.Color) {
	nc := toNRGBA(c)
	for y := 0; y < d.height; y++ {
		for x := 0; x < d.width; x++ {
			d.img.SetNRGBA(x, y, nc)
		}
	}
}

// DrawHLine draws a horizontal line from column x0 to x1 at row y.
func (d *ImageDevice) DrawHLine(x0, x1, y int, c color.Color) {
	if y < 0 || y >= d.height {
		return
	}
	if x0 < 0 {
		x0 = 0
	}
	if x1 >= d.width {
		x1 = d.width - 1
	}
	nc := toNRGBA(c)
	for x := x0; x <= x1; x++ {
		d.img.SetNRGBA(x, y, nc)
	}
}

// DrawRect draws a filled axis-aligned rectangle.
func (d *ImageDevice) DrawRect(x, y, w, h int, c color.Color) {
	nc := toNRGBA(c)
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			px, py := x+dx, y+dy
			if px >= 0 && px < d.width && py >= 0 && py < d.height {
				d.img.SetNRGBA(px, py, nc)
			}
		}
	}
}

// DrawCircle draws a filled circle centred at (cx, cy) with radius r.
func (d *ImageDevice) DrawCircle(cx, cy, r int, c color.Color) {
	nc := toNRGBA(c)
	r2 := r * r
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r2 {
				px, py := cx+dx, cy+dy
				if px >= 0 && px < d.width && py >= 0 && py < d.height {
					d.img.SetNRGBA(px, py, nc)
				}
			}
		}
	}
}

// Flush is a no-op for ImageDevice; it satisfies the Device interface.
// Use SavePNG to persist the rendered frame to disk.
func (d *ImageDevice) Flush() error { return nil }

// Close is a no-op for ImageDevice; it satisfies the Device interface.
func (d *ImageDevice) Close() error { return nil }

// SavePNG encodes the current frame as a PNG file at path.
func (d *ImageDevice) SavePNG(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, d.img)
}
