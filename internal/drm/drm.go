//go:build rpi

package drm

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"unsafe"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"golang.org/x/sys/unix"

	"github.com/walterschell/rip-bastion/internal/sysinfo"
)

// Display dimensions
const (
	dispWidth  = 480
	dispHeight = 320
	dispBPP    = 16
)

// Color constants (RGB565)
const (
	colorBlack  uint16 = 0x0000
	colorWhite  uint16 = 0xFFFF
	colorGreen  uint16 = 0x07E0
	colorRed    uint16 = 0xF800
	colorYellow uint16 = 0xFFE0
	colorGray   uint16 = 0x7BEF
	colorCyan   uint16 = 0x07FF
)

// DRM ioctl magic number
const drmIOCTLBase = 'd'

// _IOWR builds an ioctl number for read/write operations.
func _IOWR(t, nr, size uintptr) uintptr {
	return (3 << 30) | (size << 16) | (t << 8) | nr
}

// _IOW builds an ioctl number for write operations.
func _IOW(t, nr, size uintptr) uintptr {
	return (1 << 30) | (size << 16) | (t << 8) | nr
}

// DRM ioctl numbers
var (
	ioctlModeGetResources = _IOWR(drmIOCTLBase, 0xA0, unsafe.Sizeof(drmModeCardRes{}))
	ioctlModeGetConnector = _IOWR(drmIOCTLBase, 0xA7, unsafe.Sizeof(drmModeGetConnector{}))
	ioctlModeGetEncoder   = _IOWR(drmIOCTLBase, 0xA6, unsafe.Sizeof(drmModeGetEncoder{}))
	ioctlModeGetCRTC      = _IOWR(drmIOCTLBase, 0xA1, unsafe.Sizeof(drmModeCRTC{}))
	ioctlModeSetCRTC      = _IOWR(drmIOCTLBase, 0xA2, unsafe.Sizeof(drmModeCRTC{}))
	ioctlModeAddFB        = _IOWR(drmIOCTLBase, 0xAE, unsafe.Sizeof(drmModeAddFB{}))
	ioctlModeCreateDumb   = _IOWR(drmIOCTLBase, 0xB2, unsafe.Sizeof(drmModeCreateDumb{}))
	ioctlModeMapDumb      = _IOWR(drmIOCTLBase, 0xB3, unsafe.Sizeof(drmModeMapDumb{}))
	ioctlModeDestroyDumb  = _IOW(drmIOCTLBase, 0xB4, unsafe.Sizeof(drmModeDestroyDumb{}))
)

// DRM ioctl structs (matching kernel drm.h / drm_mode.h)

type drmModeCardRes struct {
	FbIDPtr         uint64
	CrtcIDPtr       uint64
	ConnectorIDPtr  uint64
	EncoderIDPtr    uint64
	CountFBs        uint32
	CountCRTCs      uint32
	CountConnectors uint32
	CountEncoders   uint32
	MinWidth        uint32
	MaxWidth        uint32
	MinHeight       uint32
	MaxHeight       uint32
}

type drmModeModeInfo struct {
	Clock      uint32
	Hdisplay   uint16
	HsyncStart uint16
	HsyncEnd   uint16
	Htotal     uint16
	Hskew      uint16
	Vdisplay   uint16
	VsyncStart uint16
	VsyncEnd   uint16
	Vtotal     uint16
	Vscan      uint16
	Vrefresh   uint32
	Flags      uint32
	Type       uint32
	Name       [32]byte
}

type drmModeGetConnector struct {
	EncodersPtr    uint64
	ModesPtr       uint64
	PropsPtr       uint64
	PropValuesPtr  uint64
	CountModes     uint32
	CountProps     uint32
	CountEncoders  uint32
	EncoderID      uint32
	ConnectorID    uint32
	ConnectorType  uint32
	ConnectorTypeID uint32
	Connection     uint32
	MmWidth        uint32
	MmHeight       uint32
	Subpixel       uint32
	Pad            uint32
}

type drmModeGetEncoder struct {
	EncoderID    uint32
	EncoderType  uint32
	CrtcID       uint32
	PossibleCRTCs uint32
	PossibleClones uint32
}

type drmModeCRTC struct {
	SetConnectorsPtr uint64
	CountConnectors  uint32
	CrtcID           uint32
	FbID             uint32
	X                uint32
	Y                uint32
	GammaSize        uint32
	ModeValid        uint32
	Mode             drmModeModeInfo
}

type drmModeAddFB struct {
	Width  uint32
	Height uint32
	Pitch  uint32
	Bpp    uint32
	Depth  uint32
	Handle uint32
	FbID   uint32
}

type drmModeCreateDumb struct {
	Height uint32
	Width  uint32
	Bpp    uint32
	Flags  uint32
	Handle uint32
	Pitch  uint32
	Size   uint64
}

type drmModeMapDumb struct {
	Handle uint32
	Pad    uint32
	Offset uint64
}

type drmModeDestroyDumb struct {
	Handle uint32
}

// Display holds DRM display state.
type Display struct {
	fd          int
	width       uint32
	height      uint32
	fb          []byte
	pitch       uint32
	fbID        uint32
	dumbHandle  uint32
	crtcID      uint32
	connID      uint32
	savedCRTC   *drmModeCRTC
}

// New opens the DRM device and sets up the framebuffer.
func New(device string) (*Display, error) {
	fd, err := unix.Open(device, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", device, err)
	}

	d := &Display{fd: fd, width: dispWidth, height: dispHeight}

	if err := d.setup(); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return d, nil
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

func (d *Display) setup() error {
	// Get card resources
	var res drmModeCardRes
	if err := ioctl(d.fd, ioctlModeGetResources, unsafe.Pointer(&res)); err != nil {
		return fmt.Errorf("DRM_IOCTL_MODE_GETRESOURCES: %w", err)
	}
	if res.CountConnectors == 0 {
		return fmt.Errorf("no DRM connectors found")
	}

	// Allocate connector ID array and re-fetch
	connIDs := make([]uint32, res.CountConnectors)
	crtcIDs := make([]uint32, res.CountCRTCs)
	res.ConnectorIDPtr = uint64(uintptr(unsafe.Pointer(&connIDs[0])))
	if res.CountCRTCs > 0 {
		res.CrtcIDPtr = uint64(uintptr(unsafe.Pointer(&crtcIDs[0])))
	}
	if err := ioctl(d.fd, ioctlModeGetResources, unsafe.Pointer(&res)); err != nil {
		return fmt.Errorf("DRM_IOCTL_MODE_GETRESOURCES (2): %w", err)
	}

	// Find a connected connector with modes
	var connID uint32
	var modeInfo drmModeModeInfo
	var encoderID uint32
	found := false

	for _, cid := range connIDs {
		conn := drmModeGetConnector{ConnectorID: cid}
		if err := ioctl(d.fd, ioctlModeGetConnector, unsafe.Pointer(&conn)); err != nil {
			continue
		}
		// Connection state: 1 = connected
		if conn.Connection != 1 || conn.CountModes == 0 {
			continue
		}
		// Get modes
		modes := make([]drmModeModeInfo, conn.CountModes)
		conn2 := drmModeGetConnector{
			ConnectorID: cid,
			ModesPtr:    uint64(uintptr(unsafe.Pointer(&modes[0]))),
		}
		if conn.CountEncoders > 0 {
			encoders := make([]uint32, conn.CountEncoders)
			conn2.EncodersPtr = uint64(uintptr(unsafe.Pointer(&encoders[0])))
			conn2.CountEncoders = conn.CountEncoders
		}
		conn2.CountModes = conn.CountModes
		conn2.CountProps = conn.CountProps
		if conn.CountProps > 0 {
			props := make([]uint32, conn.CountProps)
			propVals := make([]uint64, conn.CountProps)
			conn2.PropsPtr = uint64(uintptr(unsafe.Pointer(&props[0])))
			conn2.PropValuesPtr = uint64(uintptr(unsafe.Pointer(&propVals[0])))
		}
		if err := ioctl(d.fd, ioctlModeGetConnector, unsafe.Pointer(&conn2)); err != nil {
			continue
		}
		modeInfo = modes[0]
		// Use actual display dimensions from mode
		d.width = uint32(modeInfo.Hdisplay)
		d.height = uint32(modeInfo.Vdisplay)
		connID = cid
		encoderID = conn2.EncoderID
		found = true
		break
	}
	if !found {
		return fmt.Errorf("no connected DRM connector with modes found")
	}
	d.connID = connID

	// Get encoder to find CRTC
	enc := drmModeGetEncoder{EncoderID: encoderID}
	if err := ioctl(d.fd, ioctlModeGetEncoder, unsafe.Pointer(&enc)); err != nil {
		return fmt.Errorf("DRM_IOCTL_MODE_GETENCODER: %w", err)
	}
	crtcID := enc.CrtcID
	if crtcID == 0 && res.CountCRTCs > 0 {
		crtcID = crtcIDs[0]
	}
	d.crtcID = crtcID

	// Save original CRTC state for restoration on close
	savedCRTC := &drmModeCRTC{CrtcID: crtcID}
	_ = ioctl(d.fd, ioctlModeGetCRTC, unsafe.Pointer(savedCRTC))
	d.savedCRTC = savedCRTC

	// Create dumb buffer
	dumb := drmModeCreateDumb{
		Width:  d.width,
		Height: d.height,
		Bpp:    dispBPP,
	}
	if err := ioctl(d.fd, ioctlModeCreateDumb, unsafe.Pointer(&dumb)); err != nil {
		return fmt.Errorf("DRM_IOCTL_MODE_CREATE_DUMB: %w", err)
	}
	d.dumbHandle = dumb.Handle
	d.pitch = dumb.Pitch

	// Add framebuffer
	addFB := drmModeAddFB{
		Width:  d.width,
		Height: d.height,
		Pitch:  dumb.Pitch,
		Bpp:    dispBPP,
		Depth:  16,
		Handle: dumb.Handle,
	}
	if err := ioctl(d.fd, ioctlModeAddFB, unsafe.Pointer(&addFB)); err != nil {
		return fmt.Errorf("DRM_IOCTL_MODE_ADDFB: %w", err)
	}
	d.fbID = addFB.FbID

	// Map dumb buffer
	mapDumb := drmModeMapDumb{Handle: dumb.Handle}
	if err := ioctl(d.fd, ioctlModeMapDumb, unsafe.Pointer(&mapDumb)); err != nil {
		return fmt.Errorf("DRM_IOCTL_MODE_MAP_DUMB: %w", err)
	}

	size := int(dumb.Size)
	fb, err := unix.Mmap(d.fd, int64(mapDumb.Offset), size,
		unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("mmap framebuffer: %w", err)
	}
	d.fb = fb

	// Set CRTC
	setCRTC := drmModeCRTC{
		CrtcID:           crtcID,
		FbID:             addFB.FbID,
		ModeValid:        1,
		Mode:             modeInfo,
		SetConnectorsPtr: uint64(uintptr(unsafe.Pointer(&connID))),
		CountConnectors:  1,
	}
	if err := ioctl(d.fd, ioctlModeSetCRTC, unsafe.Pointer(&setCRTC)); err != nil {
		return fmt.Errorf("DRM_IOCTL_MODE_SETCRTC: %w", err)
	}

	return nil
}

// Render renders the system snapshot to the DRM framebuffer.
func (d *Display) Render(snap *sysinfo.Snapshot) error {
	img := image.NewNRGBA(image.Rect(0, 0, int(d.width), int(d.height)))

	// Fill background black
	for y := 0; y < int(d.height); y++ {
		for x := 0; x < int(d.width); x++ {
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
		}
	}

	white := image.NewUniform(color.NRGBA{255, 255, 255, 255})
	green := image.NewUniform(color.NRGBA{0, 255, 136, 255})
	red := image.NewUniform(color.NRGBA{255, 68, 68, 255})
	cyan := image.NewUniform(color.NRGBA{0, 212, 255, 255})
	gray := image.NewUniform(color.NRGBA{136, 136, 136, 255})

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
		for x := 0; x < int(d.width); x++ {
			img.SetNRGBA(x, yy, color.NRGBA{64, 64, 128, 255})
		}
	}

	// Title
	drawText(4, y, cyan, "rip-bastion")
	y += lineH
	drawHRule(y)
	y += 4

	// Network section
	if snap.Network != nil {
		drawText(4, y, gray, "── Network ─────────────────────────────────")
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
		drawText(4, y, gray, "── Network ─────────────────────────────────")
		y += lineH
		drawText(4, y, red, "Error: "+errMsg)
		y += lineH
	}
	drawHRule(y)
	y += 4

	// mDNS section
	if snap.MDNS != nil {
		drawText(4, y, gray, "── mDNS ─────────────────────────────────────")
		y += lineH
		statusSrc := red
		statusText := "○ Stopped"
		if snap.MDNS.Running {
			statusSrc = green
			statusText = "● Running"
		}
		drawText(4, y, statusSrc, statusText)
		drawText(100, y, white, snap.MDNS.Hostname)
		y += lineH
	}
	drawHRule(y)
	y += 4

	// VPN section
	if snap.VPN != nil {
		drawText(4, y, gray, fmt.Sprintf("── VPN (%s) ────────────────────────────", snap.VPN.Name))
		y += lineH
		statusSrc := red
		statusText := "○ Disconnected"
		if snap.VPN.Connected {
			statusSrc = green
			statusText = "● Connected"
		}
		drawText(4, y, statusSrc, statusText)
		if snap.VPN.Connected {
			drawText(120, y, white, fmt.Sprintf("%s  %s", snap.VPN.Interface, snap.VPN.PeerIP))
		}
		y += lineH
	}

	// Messages section at bottom
	msgAreaTop := int(d.height) - 80
	if y < msgAreaTop {
		// Draw divider above messages area
		for x := 0; x < int(d.width); x++ {
			img.SetNRGBA(x, msgAreaTop-2, color.NRGBA{64, 64, 128, 255})
		}
		msgY := msgAreaTop + 13
		drawText(4, msgAreaTop+2, gray, "── Messages ─────────────────────────────────")
		msgY += lineH - 4
		msgSrc := image.NewUniform(color.NRGBA{170, 255, 170, 255})
		for _, msg := range snap.Messages {
			if msgY > int(d.height)-4 {
				break
			}
			drawText(4, msgY, msgSrc, "▸ "+msg)
			msgY += lineH
		}
	}

	// Convert NRGBA image to RGB565 and copy to framebuffer
	for y := 0; y < int(d.height); y++ {
		for x := 0; x < int(d.width); x++ {
			c := img.NRGBAAt(x, y)
			r5 := uint16(c.R) >> 3
			g6 := uint16(c.G) >> 2
			b5 := uint16(c.B) >> 3
			px := (r5 << 11) | (g6 << 5) | b5
			off := y*int(d.pitch) + x*2
			if off+1 < len(d.fb) {
				d.fb[off] = byte(px)
				d.fb[off+1] = byte(px >> 8)
			}
		}
	}

	return nil
}

// Close releases DRM resources.
func (d *Display) Close() error {
	if d.savedCRTC != nil {
		_ = ioctl(d.fd, ioctlModeSetCRTC, unsafe.Pointer(d.savedCRTC))
	}
	if d.fb != nil {
		_ = unix.Munmap(d.fb)
	}
	if d.dumbHandle != 0 {
		destroy := drmModeDestroyDumb{Handle: d.dumbHandle}
		_ = ioctl(d.fd, ioctlModeDestroyDumb, unsafe.Pointer(&destroy))
	}
	if d.fd >= 0 {
		return unix.Close(d.fd)
	}
	return nil
}
