//go:build rpi

// Package drm implements the display.Device interface using a DRM/KMS dumb
// framebuffer.  It uses github.com/NeowayLabs/drm for mode-setting and
// dumb-buffer management, and golang.org/x/sys/unix for memory-mapping.
// No CGo is required.
package drm

import (
	"fmt"
	"image"
	"image/color"
	"os"

	neoDRM "github.com/NeowayLabs/drm"
	"github.com/NeowayLabs/drm/mode"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"golang.org/x/sys/unix"
)

// textLineH is the vertical distance between successive baselines for
// basicfont.Face7x13.
const textLineH = 16

// Display is a DRM/KMS drawing surface that implements display.Device.
// All draw calls operate on an in-memory image.NRGBA; Flush blits it to the
// 32-bpp (XRGB8888) DRM framebuffer.
type Display struct {
	file      *os.File
	modeset   *mode.SimpleModeset
	mset      mode.Modeset
	savedCRTC *mode.Crtc

	// DRM framebuffer state
	fbID   uint32
	handle uint32
	pitch  uint32
	size   uint64
	data   []byte

	// Off-screen draw target
	img *image.NRGBA

	width  int
	height int
}

// New opens DRM card n (0 = /dev/dri/card0), discovers the first connected
// output, allocates a 32-bpp dumb framebuffer, and activates the CRTC.
func New(cardN int) (*Display, error) {
	file, err := neoDRM.OpenCard(cardN)
	if err != nil {
		return nil, fmt.Errorf("drm: open card%d: %w", cardN, err)
	}

	if !neoDRM.HasDumbBuffer(file) {
		file.Close()
		return nil, fmt.Errorf("drm: card%d does not support dumb buffers", cardN)
	}

	ms, err := mode.NewSimpleModeset(file)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("drm: enumerate modes: %w", err)
	}
	if len(ms.Modesets) == 0 {
		file.Close()
		return nil, fmt.Errorf("drm: no connected outputs found")
	}

	mset := ms.Modesets[0]
	d := &Display{
		file:    file,
		modeset: ms,
		mset:    mset,
		width:   int(mset.Width),
		height:  int(mset.Height),
		img:     image.NewNRGBA(image.Rect(0, 0, int(mset.Width), int(mset.Height))),
	}

	d.savedCRTC, err = mode.GetCrtc(file, mset.Crtc)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("drm: save CRTC: %w", err)
	}

	if err := d.allocFramebuffer(); err != nil {
		file.Close()
		return nil, err
	}

	if err := mode.SetCrtc(file, mset.Crtc, d.fbID, 0, 0, &mset.Conn, 1, &mset.Mode); err != nil {
		d.freeFramebuffer()
		file.Close()
		return nil, fmt.Errorf("drm: SetCrtc: %w", err)
	}

	return d, nil
}

func (d *Display) allocFramebuffer() error {
	fb, err := mode.CreateFB(d.file, uint16(d.width), uint16(d.height), 32)
	if err != nil {
		return fmt.Errorf("drm: CreateFB: %w", err)
	}

	fbID, err := mode.AddFB(d.file, uint16(d.width), uint16(d.height), 24, 32, fb.Pitch, fb.Handle)
	if err != nil {
		return fmt.Errorf("drm: AddFB: %w", err)
	}

	offset, err := mode.MapDumb(d.file, fb.Handle)
	if err != nil {
		return fmt.Errorf("drm: MapDumb: %w", err)
	}

	data, err := unix.Mmap(int(d.file.Fd()), int64(offset), int(fb.Size),
		unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("drm: mmap framebuffer: %w", err)
	}

	d.fbID = fbID
	d.handle = fb.Handle
	d.pitch = fb.Pitch
	d.size = fb.Size
	d.data = data
	return nil
}

func (d *Display) freeFramebuffer() {
	if d.data != nil {
		_ = unix.Munmap(d.data)
		d.data = nil
	}
	if d.handle != 0 {
		_ = mode.DestroyDumb(d.file, d.handle)
		d.handle = 0
	}
}

// ── display.Device implementation ─────────────────────────────────────────

// Width returns the framebuffer width in pixels.
func (d *Display) Width() int { return d.width }

// Height returns the framebuffer height in pixels.
func (d *Display) Height() int { return d.height }

// TextLineHeight returns the pixel distance between successive baselines for
// the built-in 7x13 font.
func (d *Display) TextLineHeight() int { return textLineH }

// Clear fills the entire off-screen image with colour c.
func (d *Display) Clear(c color.Color) {
	r32, g32, b32, a32 := c.RGBA()
	nc := color.NRGBA{
		R: uint8(r32 >> 8),
		G: uint8(g32 >> 8),
		B: uint8(b32 >> 8),
		A: uint8(a32 >> 8),
	}
	for y := 0; y < d.height; y++ {
		for x := 0; x < d.width; x++ {
			d.img.SetNRGBA(x, y, nc)
		}
	}
}

// DrawText draws text at pixel (x, y) (y is the baseline) with colour c,
// using the built-in 7x13 bitmap font.
func (d *Display) DrawText(x, y int, c color.Color, text string) {
	dr := &font.Drawer{
		Dst:  d.img,
		Src:  image.NewUniform(c),
		Face: basicfont.Face7x13,
		Dot:  fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)},
	}
	dr.DrawString(text)
}

// DrawHLine draws a horizontal line from column x0 to x1 at row y.
func (d *Display) DrawHLine(x0, x1, y int, c color.Color) {
	if y < 0 || y >= d.height {
		return
	}
	if x0 < 0 {
		x0 = 0
	}
	if x1 >= d.width {
		x1 = d.width - 1
	}
	r32, g32, b32, a32 := c.RGBA()
	nc := color.NRGBA{
		R: uint8(r32 >> 8),
		G: uint8(g32 >> 8),
		B: uint8(b32 >> 8),
		A: uint8(a32 >> 8),
	}
	for x := x0; x <= x1; x++ {
		d.img.SetNRGBA(x, y, nc)
	}
}

// DrawRect draws a filled axis-aligned rectangle.
func (d *Display) DrawRect(x, y, w, h int, c color.Color) {
	r32, g32, b32, a32 := c.RGBA()
	nc := color.NRGBA{
		R: uint8(r32 >> 8),
		G: uint8(g32 >> 8),
		B: uint8(b32 >> 8),
		A: uint8(a32 >> 8),
	}
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
func (d *Display) DrawCircle(cx, cy, r int, c color.Color) {
	r32, g32, b32, a32 := c.RGBA()
	nc := color.NRGBA{
		R: uint8(r32 >> 8),
		G: uint8(g32 >> 8),
		B: uint8(b32 >> 8),
		A: uint8(a32 >> 8),
	}
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

// Flush blits the off-screen NRGBA image to the 32-bpp (XRGB8888) DRM
// framebuffer.
func (d *Display) Flush() error {
	for row := 0; row < d.height; row++ {
		for col := 0; col < d.width; col++ {
			c := d.img.NRGBAAt(col, row)
			off := row*int(d.pitch) + col*4
			if off+3 < len(d.data) {
				d.data[off+0] = c.B
				d.data[off+1] = c.G
				d.data[off+2] = c.R
				d.data[off+3] = 0
			}
		}
	}
	return nil
}

// Close restores the saved CRTC state and releases all DRM resources.
func (d *Display) Close() error {
	if d.savedCRTC != nil {
		_ = d.modeset.SetCrtc(&d.mset, d.savedCRTC)
	}
	d.freeFramebuffer()
	return d.file.Close()
}
