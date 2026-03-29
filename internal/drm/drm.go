//go:build rpi

// Package drm renders the system status dashboard to a DRM/KMS framebuffer.
// It uses github.com/NeowayLabs/drm for mode-setting and dumb-buffer
// management, and golang.org/x/sys/unix for memory-mapping the framebuffer.
// No CGo is required.
package drm

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"strings"

	neoDRM "github.com/NeowayLabs/drm"
	"github.com/NeowayLabs/drm/mode"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"golang.org/x/sys/unix"

	"github.com/walterschell/rip-bastion/internal/sysinfo"
)

// Display wraps a DRM/KMS dumb framebuffer for a single connected output.
type Display struct {
	file      *os.File
	modeset   *mode.SimpleModeset
	mset      mode.Modeset
	savedCRTC *mode.Crtc

	fbID   uint32
	handle uint32
	pitch  uint32
	size   uint64
	data   []byte

	width  uint16
	height uint16
}

// New opens DRM card n (0 = /dev/dri/card0), selects the first connected
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
		width:   mset.Width,
		height:  mset.Height,
	}

	// Save the current CRTC so we can restore it on Close.
	d.savedCRTC, err = mode.GetCrtc(file, mset.Crtc)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("drm: save CRTC: %w", err)
	}

	if err := d.allocFramebuffer(); err != nil {
		file.Close()
		return nil, err
	}

	// Activate the CRTC with our new framebuffer.
	if err := mode.SetCrtc(file, mset.Crtc, d.fbID, 0, 0, &mset.Conn, 1, &mset.Mode); err != nil {
		d.freeFramebuffer()
		file.Close()
		return nil, fmt.Errorf("drm: SetCrtc: %w", err)
	}

	return d, nil
}

// allocFramebuffer creates a 32-bpp dumb buffer, registers it as a
// framebuffer, and memory-maps it.
func (d *Display) allocFramebuffer() error {
	fb, err := mode.CreateFB(d.file, d.width, d.height, 32)
	if err != nil {
		return fmt.Errorf("drm: CreateFB: %w", err)
	}

	fbID, err := mode.AddFB(d.file, d.width, d.height, 24, 32, fb.Pitch, fb.Handle)
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

// Render draws the system snapshot onto the framebuffer.
func (d *Display) Render(snap *sysinfo.Snapshot) error {
	img := image.NewNRGBA(image.Rect(0, 0, int(d.width), int(d.height)))

	// Black background.
	for y := 0; y < int(d.height); y++ {
		for x := 0; x < int(d.width); x++ {
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
		}
	}

	var (
		white  = image.NewUniform(color.NRGBA{255, 255, 255, 255})
		green  = image.NewUniform(color.NRGBA{0, 255, 136, 255})
		red    = image.NewUniform(color.NRGBA{255, 68, 68, 255})
		cyan   = image.NewUniform(color.NRGBA{0, 212, 255, 255})
		gray   = image.NewUniform(color.NRGBA{136, 136, 136, 255})
		msgClr = image.NewUniform(color.NRGBA{170, 255, 170, 255})
	)

	face := basicfont.Face7x13
	lineH := 16
	y := 14

	drawText := func(x, yy int, src *image.Uniform, text string) {
		dr := &font.Drawer{
			Dst:  img,
			Src:  src,
			Face: face,
			Dot:  fixed.Point26_6{X: fixed.I(x), Y: fixed.I(yy)},
		}
		dr.DrawString(text)
	}

	drawHRule := func(yy int) {
		c := color.NRGBA{64, 64, 128, 255}
		for x := 0; x < int(d.width); x++ {
			img.SetNRGBA(x, yy, c)
		}
	}

	// Title row.
	drawText(4, y, cyan, "rip-bastion")
	y += lineH
	drawHRule(y)
	y += 4

	// Network.
	if snap.Network != nil {
		drawText(4, y, gray, "\u2500\u2500 Network \u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500")
		y += lineH
		drawText(4, y, white, fmt.Sprintf("Interface : %s", snap.Network.InterfaceName))
		y += lineH
		drawText(4, y, white, fmt.Sprintf("IP/Mask   : %s / %s", snap.Network.IP, snap.Network.Netmask))
		y += lineH
		drawText(4, y, white, fmt.Sprintf("Gateway   : %s", snap.Network.Gateway))
		y += lineH
		drawText(4, y, white, fmt.Sprintf("DNS       : %s", strings.Join(snap.Network.DNS, ", ")))
		y += lineH
	} else if errMsg, ok := snap.Errors["network"]; ok {
		drawText(4, y, gray, "\u2500\u2500 Network \u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500")
		y += lineH
		drawText(4, y, red, "Error: "+errMsg)
		y += lineH
	}
	drawHRule(y)
	y += 4

	// mDNS.
	if snap.MDNS != nil {
		drawText(4, y, gray, "\u2500\u2500 mDNS \u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500")
		y += lineH
		statusSrc, statusText := red, "\u25cb Stopped"
		if snap.MDNS.Running {
			statusSrc, statusText = green, "\u25cf Running"
		}
		drawText(4, y, statusSrc, statusText)
		drawText(100, y, white, snap.MDNS.Hostname)
		y += lineH
	}
	drawHRule(y)
	y += 4

	// VPN.
	if snap.VPN != nil {
		drawText(4, y, gray, fmt.Sprintf("\u2500\u2500 VPN (%s) \u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500", snap.VPN.Name))
		y += lineH
		statusSrc, statusText := red, "\u25cb Disconnected"
		if snap.VPN.Connected {
			statusSrc, statusText = green, "\u25cf Connected"
		}
		drawText(4, y, statusSrc, statusText)
		if snap.VPN.Connected {
			drawText(120, y, white, fmt.Sprintf("%s  %s", snap.VPN.Interface, snap.VPN.PeerIP))
		}
		y += lineH
	}

	// Messages area pinned to the bottom 80 pixels.
	msgAreaTop := int(d.height) - 80
	if y < msgAreaTop {
		drawHRule(msgAreaTop - 2)
		drawText(4, msgAreaTop+2, gray, "\u2500\u2500 Messages \u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500")
		msgY := msgAreaTop + 2 + lineH
		for _, msg := range snap.Messages {
			if msgY > int(d.height)-4 {
				break
			}
			drawText(4, msgY, msgClr, "\u25b8 "+msg)
			msgY += lineH
		}
	}

	// Blit the NRGBA image to the 32-bpp (XRGB8888) framebuffer.
	for row := 0; row < int(d.height); row++ {
		for col := 0; col < int(d.width); col++ {
			c := img.NRGBAAt(col, row)
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

// Close restores the saved CRTC and releases all DRM resources.
func (d *Display) Close() error {
	if d.savedCRTC != nil {
		_ = d.modeset.SetCrtc(&d.mset, d.savedCRTC)
	}
	d.freeFramebuffer()
	return d.file.Close()
}
