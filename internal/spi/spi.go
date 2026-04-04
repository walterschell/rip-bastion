//go:build rpi

package spi

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	spiIOCWriteMode       = 0x40016b01
	spiIOCWriteBitsPerWrd = 0x40016b03
	spiIOCWriteMaxSpeedHz = 0x40046b04

	spiIocMagic  = 107 // 'k'
	gpioIocMagic = 0xB4

	gpioGetLineHandleNr = 0x03
	gpioSetLineValueNr  = 0x09

	gpioHandleRequestOutput = 1 << 1

	defaultSPIPath      = "/dev/spidev0.0"
	defaultSpeedHz      = 8000000
	defaultDCPin        = 24
	defaultRSTPin       = 25
	defaultWidth        = 480
	defaultHeight       = 320
	defaultGRAMW        = 320
	defaultGRAMH        = 480
	defaultXOff         = 0
	defaultYOff         = 0
	defaultMADCTL       = 0x28
	defaultPixelFmt     = 0x66 // RGB666
	defaultColorOrd     = "grb"
	defaultColumnMajor  = false
	defaultPackPixels16 = false
)

type spiIocTransfer struct {
	TxBuf          uint64
	RxBuf          uint64
	Len            uint32
	SpeedHz        uint32
	DelayUsecs     uint16
	BitsPerWord    uint8
	CsChange       uint8
	TxNbits        uint8
	RxNbits        uint8
	WordDelayUsecs uint8
	Pad            uint8
}

type spiDev struct {
	fd    int
	speed uint32
	bpw   uint8
	maxTx int
}

func newSPIDev(path string, speed uint32, bitsPerWord uint8) (*spiDev, error) {
	fd, err := unix.Open(path, unix.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	mode := uint8(0)
	if err := ioctlSetPtr(fd, spiIOCWriteMode, unsafe.Pointer(&mode)); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("set SPI mode: %w", err)
	}

	bits := bitsPerWord
	if err := ioctlSetPtr(fd, spiIOCWriteBitsPerWrd, unsafe.Pointer(&bits)); err != nil {
		if bitsPerWord == 16 {
			fallback := uint8(8)
			if fallbackErr := ioctlSetPtr(fd, spiIOCWriteBitsPerWrd, unsafe.Pointer(&fallback)); fallbackErr != nil {
				_ = unix.Close(fd)
				return nil, fmt.Errorf("set SPI bits-per-word 16 failed (%v), fallback to 8-bit also failed (%v)", err, fallbackErr)
			}
			bits = fallback
		} else {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("set SPI bits-per-word: %w", err)
		}
	}

	maxSpeed := uint32(speed)
	if err := ioctlSetPtr(fd, spiIOCWriteMaxSpeedHz, unsafe.Pointer(&maxSpeed)); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("set SPI max speed: %w", err)
	}

	maxTx := readSpidevBufsiz()
	if maxTx <= 0 {
		maxTx = 4096
	}

	return &spiDev{fd: fd, speed: speed, bpw: bits, maxTx: maxTx}, nil
}

func readSpidevBufsiz() int {
	b, err := os.ReadFile("/sys/module/spidev/parameters/bufsiz")
	if err != nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return v
}

func ioctlSetPtr(fd int, req uintptr, p unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(p))
	if errno != 0 {
		return errno
	}
	return nil
}

func (s *spiDev) Close() error {
	return unix.Close(s.fd)
}

func spiIOCMessage(n uintptr) uintptr {
	sz := unsafe.Sizeof(spiIocTransfer{}) * n
	return iow(spiIocMagic, 0, sz)
}

func iow(t, nr, size uintptr) uintptr {
	const (
		iocNrbits   = 8
		iocTypebits = 8
		iocSizebits = 14

		iocNrshift   = 0
		iocTypeshift = iocNrshift + iocNrbits
		iocSizeshift = iocTypeshift + iocTypebits
		iocDirshift  = iocSizeshift + iocSizebits

		iocWrite = 1
	)

	return (iocWrite << iocDirshift) | (t << iocTypeshift) | (nr << iocNrshift) | (size << iocSizeshift)
}

func iowr(t, nr, size uintptr) uintptr {
	const (
		iocNrbits   = 8
		iocTypebits = 8
		iocSizebits = 14

		iocNrshift   = 0
		iocTypeshift = iocNrshift + iocNrbits
		iocSizeshift = iocTypeshift + iocTypebits
		iocDirshift  = iocSizeshift + iocSizebits

		iocWrite = 1
		iocRead  = 2
	)

	return ((iocWrite | iocRead) << iocDirshift) | (t << iocTypeshift) | (nr << iocNrshift) | (size << iocSizeshift)
}

func (s *spiDev) write(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	chunkSize := s.maxTx
	if chunkSize <= 0 {
		chunkSize = 4096
	}

	for offset := 0; offset < len(data); offset += chunkSize {
		end := offset + chunkSize
		if end > len(data) {
			end = len(data)
		}
		if err := s.writeChunk(data[offset:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *spiDev) writeChunk(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	tr := spiIocTransfer{
		TxBuf:       uint64(uintptr(unsafe.Pointer(&data[0]))),
		Len:         uint32(len(data)),
		SpeedHz:     s.speed,
		BitsPerWord: s.bpw,
	}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(s.fd), spiIOCMessage(1), uintptr(unsafe.Pointer(&tr)))
	if errno != 0 {
		return errno
	}
	return nil
}

type gpioOut struct {
	pin      int
	lineFD   int
	useSysfs bool
}

type gpioHandleRequest struct {
	LineOffsets   [64]uint32
	Flags         uint32
	DefaultValues [64]uint8
	ConsumerLabel [32]byte
	Lines         uint32
	FD            int32
}

type gpioHandleData struct {
	Values [64]uint8
}

func gpioGetLineHandleIOCTL() uintptr {
	return iowr(gpioIocMagic, gpioGetLineHandleNr, unsafe.Sizeof(gpioHandleRequest{}))
}

func gpioSetLineValueIOCTL() uintptr {
	return iowr(gpioIocMagic, gpioSetLineValueNr, unsafe.Sizeof(gpioHandleData{}))
}

func newGPIOOut(pin int) (*gpioOut, error) {
	if err := ensureGPIO(pin); err == nil {
		if err := os.WriteFile(filepath.Join(gpioPath(pin), "direction"), []byte("out"), 0o644); err != nil {
			return nil, fmt.Errorf("set gpio%d direction: %w", pin, err)
		}
		return &gpioOut{pin: pin, useSysfs: true}, nil
	}

	lineFD, err := requestGPIOHandle(pin)
	if err != nil {
		return nil, fmt.Errorf("gpio%d unavailable via sysfs and gpiochip ioctl fallback failed: %w", pin, err)
	}
	return &gpioOut{pin: pin, lineFD: lineFD}, nil
}

func requestGPIOHandle(pin int) (int, error) {
	var lastErr error
	for chip := 0; chip < 8; chip++ {
		chipPath := fmt.Sprintf("/dev/gpiochip%d", chip)
		chipFD, err := unix.Open(chipPath, unix.O_RDWR, 0)
		if err != nil {
			lastErr = err
			continue
		}

		req := gpioHandleRequest{
			Flags: gpioHandleRequestOutput,
			Lines: 1,
		}
		req.LineOffsets[0] = uint32(pin)
		copy(req.ConsumerLabel[:], []byte("rip-bastion"))

		err = ioctlSetPtr(chipFD, gpioGetLineHandleIOCTL(), unsafe.Pointer(&req))
		_ = unix.Close(chipFD)
		if err != nil {
			lastErr = err
			continue
		}
		if req.FD <= 0 {
			lastErr = fmt.Errorf("kernel returned invalid line fd")
			continue
		}
		return int(req.FD), nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no gpiochip devices available")
	}
	return 0, lastErr
}

func ensureGPIO(pin int) error {
	gPath := gpioPath(pin)
	if _, err := os.Stat(gPath); err == nil {
		return nil
	}
	if err := os.WriteFile("/sys/class/gpio/export", []byte(strconv.Itoa(pin)), 0o644); err != nil {
		if strings.Contains(err.Error(), "busy") {
			return nil
		}
		return fmt.Errorf("export gpio%d: %w", pin, err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(gPath); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("gpio%d did not appear after export", pin)
}

func gpioPath(pin int) string {
	return fmt.Sprintf("/sys/class/gpio/gpio%d", pin)
}

func (g *gpioOut) set(v bool) error {
	if !g.useSysfs {
		if g.lineFD <= 0 {
			return fmt.Errorf("gpio%d line fd is not initialized", g.pin)
		}
		var data gpioHandleData
		if v {
			data.Values[0] = 1
		}
		if err := ioctlSetPtr(g.lineFD, gpioSetLineValueIOCTL(), unsafe.Pointer(&data)); err != nil {
			return fmt.Errorf("write gpio%d via gpiochip ioctl: %w", g.pin, err)
		}
		return nil
	}

	val := []byte("0")
	if v {
		val = []byte("1")
	}
	if err := os.WriteFile(filepath.Join(gpioPath(g.pin), "value"), val, 0o644); err != nil {
		return fmt.Errorf("write gpio%d value: %w", g.pin, err)
	}
	return nil
}

func (g *gpioOut) close() error {
	if g.lineFD > 0 {
		err := unix.Close(g.lineFD)
		g.lineFD = 0
		return err
	}
	return nil
}

type config struct {
	spiPath         string
	speedHz         uint32
	dcPin           int
	rstPin          int
	width           int
	height          int
	xoff            int
	yoff            int
	madctl          byte
	pixfmt          byte
	colorOrd        string
	bottomUp        bool
	streamFullFrame bool
	columnMajor     bool
	packPixels16    bool
	skipInitFlush   bool
	batchRows       int
}

func envString(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func envInt(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: %w", key, v, err)
	}
	return n, nil
}

func envBool(key string, def bool) (bool, error) {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return def, nil
	}
	switch v {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid %s=%q", key, v)
	}
}

func loadConfigFromEnv() (config, error) {
	spd, err := envInt("RIP_SPI_SPEED", defaultSpeedHz)
	if err != nil {
		return config{}, err
	}
	dcPin, err := envInt("RIP_SPI_DC", defaultDCPin)
	if err != nil {
		return config{}, err
	}
	rstPin, err := envInt("RIP_SPI_RST", defaultRSTPin)
	if err != nil {
		return config{}, err
	}
	width, err := envInt("RIP_SPI_WIDTH", defaultWidth)
	if err != nil {
		return config{}, err
	}
	height, err := envInt("RIP_SPI_HEIGHT", defaultHeight)
	if err != nil {
		return config{}, err
	}
	xoff, err := envInt("RIP_SPI_XOFF", defaultXOff)
	if err != nil {
		return config{}, err
	}
	yoff, err := envInt("RIP_SPI_YOFF", defaultYOff)
	if err != nil {
		return config{}, err
	}
	madctlInt, err := envInt("RIP_SPI_MADCTL", defaultMADCTL)
	if err != nil {
		return config{}, err
	}
	pixfmtInt, err := envInt("RIP_SPI_PIXFMT", defaultPixelFmt)
	if err != nil {
		return config{}, err
	}
	batchRows, err := envInt("RIP_SPI_BATCH_ROWS", 8)
	if err != nil {
		return config{}, err
	}
	if batchRows < 1 {
		batchRows = 1
	}
	colorOrd := strings.ToLower(envString("RIP_SPI_COLOR_ORDER", defaultColorOrd))
	switch colorOrd {
	case "rgb", "rbg", "grb", "gbr", "brg", "bgr":
	default:
		return config{}, fmt.Errorf("invalid RIP_SPI_COLOR_ORDER=%q", colorOrd)
	}
	bottomUp, err := envBool("RIP_SPI_BOTTOM_UP", false)
	if err != nil {
		return config{}, err
	}
	streamFullFrame, err := envBool("RIP_SPI_STREAM_FULL_FRAME", false)
	if err != nil {
		return config{}, err
	}
	columnMajor, err := envBool("RIP_SPI_COLUMN_MAJOR", defaultColumnMajor)
	if err != nil {
		return config{}, err
	}
	packPixels16, err := envBool("RIP_SPI_PACK_PIXELS16", defaultPackPixels16)
	if err != nil {
		return config{}, err
	}
	skipInitFlush, err := envBool("RIP_SPI_SKIP_INIT_FLUSH", false)
	if err != nil {
		return config{}, err
	}

	return config{
		spiPath:         envString("RIP_SPI_DEV", defaultSPIPath),
		speedHz:         uint32(spd),
		dcPin:           dcPin,
		rstPin:          rstPin,
		width:           width,
		height:          height,
		xoff:            xoff,
		yoff:            yoff,
		madctl:          byte(madctlInt),
		pixfmt:          byte(pixfmtInt),
		colorOrd:        colorOrd,
		bottomUp:        bottomUp,
		streamFullFrame: streamFullFrame,
		columnMajor:     columnMajor,
		packPixels16:    packPixels16,
		skipInitFlush:   skipInitFlush,
		batchRows:       batchRows,
	}, nil
}

// Display is an SPI panel implementation of display.Device.
type Display struct {
	spi *spiDev
	dc  *gpioOut
	rst *gpioOut

	img *image.NRGBA

	width  int
	height int
	gramW  int
	gramH  int
	xoff   int
	yoff   int

	packCmd16       bool
	packData16      bool
	pixfmt          byte
	madctl          byte
	colorOrd        string
	bottomUp        bool
	streamFullFrame bool
	columnMajor     bool
	packPixel16     bool
	skipInitFlush   bool
	batchRows       int
	flushBuf        []byte
	prevFrame       []byte
	hasPrevFrame    bool
}

// NewFromEnv creates an SPI display using environment-variable overrides.
func NewFromEnv() (*Display, error) {
	cfg, err := loadConfigFromEnv()
	if err != nil {
		return nil, err
	}
	return New(cfg)
}

// New creates and initializes the SPI display.
func New(cfg config) (*Display, error) {
	spi, err := newSPIDev(cfg.spiPath, cfg.speedHz, 8)
	if err != nil {
		return nil, err
	}

	dc, err := newGPIOOut(cfg.dcPin)
	if err != nil {
		_ = spi.Close()
		return nil, err
	}

	rst, err := newGPIOOut(cfg.rstPin)
	if err != nil {
		_ = spi.Close()
		return nil, err
	}

	d := &Display{
		spi:             spi,
		dc:              dc,
		rst:             rst,
		img:             image.NewNRGBA(image.Rect(0, 0, cfg.width, cfg.height)),
		width:           cfg.width,
		height:          cfg.height,
		gramW:           defaultGRAMW,
		gramH:           defaultGRAMH,
		xoff:            cfg.xoff,
		yoff:            cfg.yoff,
		packCmd16:       true,
		packData16:      true,
		pixfmt:          cfg.pixfmt,
		madctl:          cfg.madctl,
		colorOrd:        cfg.colorOrd,
		bottomUp:        cfg.bottomUp,
		streamFullFrame: cfg.streamFullFrame,
		columnMajor:     cfg.columnMajor,
		packPixel16:     cfg.packPixels16,
		skipInitFlush:   cfg.skipInitFlush,
		batchRows:       cfg.batchRows,
	}

	if err := d.reset(); err != nil {
		_ = spi.Close()
		return nil, err
	}
	if err := d.initILI9486(); err != nil {
		_ = spi.Close()
		return nil, err
	}

	if !d.skipInitFlush {
		d.Clear(color.Black)
		if err := d.Flush(); err != nil {
			_ = spi.Close()
			return nil, err
		}
	}

	return d, nil
}

func (d *Display) pack(data []byte, enabled bool) []byte {
	if !enabled {
		return data
	}
	out := make([]byte, 0, len(data)*2)
	for _, b := range data {
		out = append(out, 0x00, b)
	}
	return out
}

func (d *Display) cmd(cmd byte, data ...byte) error {
	if err := d.dc.set(false); err != nil {
		return err
	}
	if err := d.spi.write(d.pack([]byte{cmd}, d.packCmd16)); err != nil {
		return fmt.Errorf("spi cmd 0x%02x: %w", cmd, err)
	}
	if len(data) > 0 {
		if err := d.data(data); err != nil {
			return err
		}
	}
	return nil
}

func (d *Display) data(data []byte) error {
	if err := d.dc.set(true); err != nil {
		return err
	}
	if err := d.writePacked(data, d.packData16, 0); err != nil {
		return fmt.Errorf("spi data write: %w", err)
	}
	return nil
}

func (d *Display) pixelData(data []byte, rowStride int) error {
	if err := d.dc.set(true); err != nil {
		return err
	}
	if err := d.writePacked(data, d.packPixel16, rowStride); err != nil {
		return fmt.Errorf("spi pixel write: %w", err)
	}
	return nil
}

func (d *Display) writePacked(data []byte, pack bool, rowStride int) error {
	if len(data) == 0 {
		return nil
	}

	if !pack {
		if rowStride <= 0 || rowStride >= len(data) {
			return d.spi.write(data)
		}
		for offset := 0; offset < len(data); offset += rowStride {
			end := offset + rowStride
			if end > len(data) {
				end = len(data)
			}
			if err := d.spi.write(data[offset:end]); err != nil {
				return err
			}
		}
		return nil
	}

	if rowStride <= 0 {
		return d.spi.write(d.pack(data, true))
	}

	packedStride := rowStride * 2
	rowsPerChunk := 1
	if packedStride > 0 && d.spi.maxTx > packedStride {
		rowsPerChunk = d.spi.maxTx / packedStride
		if rowsPerChunk < 1 {
			rowsPerChunk = 1
		}
	}
	srcChunkSize := rowsPerChunk * rowStride
	for offset := 0; offset < len(data); offset += srcChunkSize {
		end := offset + srcChunkSize
		if end > len(data) {
			end = len(data)
		}
		if err := d.spi.write(d.pack(data[offset:end], true)); err != nil {
			return err
		}
	}
	return nil
}

func (d *Display) reset() error {
	if err := d.rst.set(true); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	if err := d.rst.set(false); err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	if err := d.rst.set(true); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond)
	return nil
}

func (d *Display) initILI9486() error {
	if err := d.cmd(0x01); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond)

	if err := d.cmd(0x11); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond)

	if err := d.cmd(0x3A, d.pixfmt); err != nil {
		return err
	}
	if err := d.cmd(0x36, d.madctl); err != nil {
		return err
	}

	if err := d.cmd(0xB6, 0x00, 0x22, 0x3B); err != nil {
		return err
	}

	if err := d.cmd(0xC0, 0x0D, 0x0D); err != nil {
		return err
	}
	if err := d.cmd(0xC1, 0x43, 0x00); err != nil {
		return err
	}
	if err := d.cmd(0xC2, 0x00); err != nil {
		return err
	}
	if err := d.cmd(0xC5, 0x00, 0x48); err != nil {
		return err
	}

	if err := d.cmd(0xE0,
		0x0F, 0x24, 0x1C, 0x0A, 0x0F, 0x08, 0x43, 0x88,
		0x32, 0x0F, 0x10, 0x06, 0x0F, 0x07, 0x00); err != nil {
		return err
	}
	if err := d.cmd(0xE1,
		0x0F, 0x38, 0x30, 0x09, 0x0F, 0x0F, 0x4E, 0x77,
		0x3C, 0x07, 0x10, 0x05, 0x23, 0x1B, 0x00); err != nil {
		return err
	}

	if err := d.cmd(0x29); err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	return nil
}

func (d *Display) setWindowRaw(x0, y0, x1, y1 int) error {
	x0 += d.xoff
	x1 += d.xoff
	y0 += d.yoff
	y1 += d.yoff

	if x0 < 0 || y0 < 0 || x1 < 0 || y1 < 0 || x0 > 0xFFFF || y0 > 0xFFFF || x1 > 0xFFFF || y1 > 0xFFFF {
		return fmt.Errorf("setWindow out of range after offset: x0=%d y0=%d x1=%d y1=%d", x0, y0, x1, y1)
	}

	if err := d.cmd(0x2A,
		byte(x0>>8), byte(x0),
		byte(x1>>8), byte(x1)); err != nil {
		return err
	}
	if err := d.cmd(0x2B,
		byte(y0>>8), byte(y0),
		byte(y1>>8), byte(y1)); err != nil {
		return err
	}
	if err := d.cmd(0x2C); err != nil {
		return err
	}
	return nil
}

func (d *Display) setWindow(x0, y0, x1, y1 int) error {
	return d.setWindowRaw(x0, y0, x1, y1)
}

func (d *Display) setWindowPhysical(x0, y0, x1, y1 int) error {
	if x0 < 0 || y0 < 0 || x1 < 0 || y1 < 0 || x0 > 0xFFFF || y0 > 0xFFFF || x1 > 0xFFFF || y1 > 0xFFFF {
		return fmt.Errorf("setWindow physical out of range: x0=%d y0=%d x1=%d y1=%d", x0, y0, x1, y1)
	}

	if err := d.cmd(0x2A,
		byte(x0>>8), byte(x0),
		byte(x1>>8), byte(x1)); err != nil {
		return err
	}
	if err := d.cmd(0x2B,
		byte(y0>>8), byte(y0),
		byte(y1>>8), byte(y1)); err != nil {
		return err
	}
	if err := d.cmd(0x2C); err != nil {
		return err
	}
	return nil
}

func toNRGBA(c color.Color) color.NRGBA {
	r32, g32, b32, a32 := c.RGBA()
	return color.NRGBA{R: uint8(r32 >> 8), G: uint8(g32 >> 8), B: uint8(b32 >> 8), A: uint8(a32 >> 8)}
}

func reorderRGB(order string, r, g, b uint8) (uint8, uint8, uint8) {
	switch order {
	case "rgb":
		return r, g, b
	case "rbg":
		return r, b, g
	case "grb":
		return g, r, b
	case "gbr":
		return g, b, r
	case "brg":
		return b, r, g
	case "bgr":
		return b, g, r
	default:
		return r, g, b
	}
}

func (d *Display) bytesPerPixel() int {
	if d.pixfmt == 0x66 {
		return 3
	}
	return 2
}

func (d *Display) usesNativeRotatedStream() bool {
	return false
}

func (d *Display) encodePixel(buf []byte, off int, c color.NRGBA) {
	if d.pixfmt == 0x66 {
		r, g, b := reorderRGB(d.colorOrd, c.R, c.G, c.B)
		buf[off] = r & 0xFC
		buf[off+1] = g & 0xFC
		buf[off+2] = b & 0xFC
		return
	}
	r5 := uint16(c.R>>3) & 0x1F
	g6 := uint16(c.G>>2) & 0x3F
	b5 := uint16(c.B>>3) & 0x1F
	rgb565 := (r5 << 11) | (g6 << 5) | b5
	buf[off] = byte(rgb565 >> 8)
	buf[off+1] = byte(rgb565)
}

func (d *Display) encodeFullFrame() ([]byte, int) {
	bytesPerPixel := d.bytesPerPixel()
	framePixels := d.width * d.height
	if d.usesNativeRotatedStream() {
		framePixels = d.gramW * d.gramH
	}
	frameSize := framePixels * bytesPerPixel
	if cap(d.flushBuf) < frameSize {
		d.flushBuf = make([]byte, frameSize)
	}
	buf := d.flushBuf[:frameSize]
	for i := range buf {
		buf[i] = 0
	}

	if d.usesNativeRotatedStream() {
		for y := 0; y < d.height; y++ {
			for x := 0; x < d.width; x++ {
				physX := y
				physY := x
				off := (physY*d.gramW + physX) * bytesPerPixel
				d.encodePixel(buf, off, d.img.NRGBAAt(x, y))
			}
		}
		return buf, bytesPerPixel
	}

	if d.columnMajor {
		for x := 0; x < d.width; x++ {
			for y := 0; y < d.height; y++ {
				off := (x*d.height + y) * bytesPerPixel
				d.encodePixel(buf, off, d.img.NRGBAAt(x, y))
			}
		}
		return buf, bytesPerPixel
	}

	for y := 0; y < d.height; y++ {
		for x := 0; x < d.width; x++ {
			off := (y*d.width + x) * bytesPerPixel
			d.encodePixel(buf, off, d.img.NRGBAAt(x, y))
		}
	}
	return buf, bytesPerPixel
}

func (d *Display) streamFullFrameBuffer(buf []byte) error {
	if d.usesNativeRotatedStream() {
		if err := d.setWindowPhysical(d.xoff, d.yoff, d.xoff+d.gramW-1, d.yoff+d.gramH-1); err != nil {
			return err
		}
	} else if err := d.setWindow(0, 0, d.width-1, d.height-1); err != nil {
		return err
	}
	rowStride := 0
	if !d.usesNativeRotatedStream() && !d.columnMajor {
		rowStride = d.width * d.bytesPerPixel()
	}
	if err := d.pixelData(buf, rowStride); err != nil {
		return err
	}
	return nil
}

func (d *Display) Width() int { return d.width }

func (d *Display) Height() int { return d.height }

func (d *Display) SetPixel(x, y int, c color.Color) {
	if x < 0 || x >= d.width || y < 0 || y >= d.height {
		return
	}
	d.img.SetNRGBA(x, y, toNRGBA(c))
}

func (d *Display) Clear(c color.Color) {
	nc := toNRGBA(c)
	for y := 0; y < d.height; y++ {
		for x := 0; x < d.width; x++ {
			d.img.SetNRGBA(x, y, nc)
		}
	}
}

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
	nc := toNRGBA(c)
	for x := x0; x <= x1; x++ {
		d.img.SetNRGBA(x, y, nc)
	}
}

func (d *Display) DrawRect(x, y, w, h int, c color.Color) {
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

func (d *Display) DrawCircle(cx, cy, r int, c color.Color) {
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

// FillSolid clears the backing image and forces a full-screen hardware fill.
// This bypasses differential updates so stale panel contents are overwritten.
func (d *Display) FillSolid(c color.Color) error {
	nc := toNRGBA(c)
	d.Clear(nc)

	bytesPerPixel := d.bytesPerPixel()

	if d.streamFullFrame {
		buf, _ := d.encodeFullFrame()
		if len(d.prevFrame) != len(buf) {
			d.prevFrame = make([]byte, len(buf))
		}
		if err := d.streamFullFrameBuffer(buf); err != nil {
			return err
		}
		copy(d.prevFrame, buf)
		d.hasPrevFrame = true
		return nil
	}

	rowsPerBatch := d.batchRows
	if rowsPerBatch < 1 {
		rowsPerBatch = 1
	}
	if rowsPerBatch > d.height {
		rowsPerBatch = d.height
	}

	clearWidth := d.width
	if d.xoff > 0 {
		clearWidth += d.xoff
	}
	clearHeight := d.height
	if d.yoff > 0 {
		clearHeight += d.yoff
	}
	if clearWidth < 1 {
		clearWidth = d.width
	}
	if clearHeight < 1 {
		clearHeight = d.height
	}
	if rowsPerBatch > clearHeight {
		rowsPerBatch = clearHeight
	}

	batchCap := clearWidth * rowsPerBatch * bytesPerPixel
	if cap(d.flushBuf) < batchCap {
		d.flushBuf = make([]byte, batchCap)
	}

	r, g, b := reorderRGB(d.colorOrd, nc.R, nc.G, nc.B)
	for batchStart := 0; batchStart < clearHeight; batchStart += rowsPerBatch {
		y0 := batchStart
		y1 := y0 + rowsPerBatch - 1
		if y1 >= clearHeight {
			y1 = clearHeight - 1
		}
		if d.bottomUp {
			y1 = clearHeight - 1 - batchStart
			y0 = y1 - rowsPerBatch + 1
			if y0 < 0 {
				y0 = 0
			}
		}
		rows := y1 - y0 + 1
		n := clearWidth * rows * bytesPerPixel
		buf := d.flushBuf[:n]

		for off := 0; off < n; off += bytesPerPixel {
			if d.pixfmt == 0x66 {
				buf[off] = r & 0xFC
				buf[off+1] = g & 0xFC
				buf[off+2] = b & 0xFC
			} else {
				r5 := uint16(nc.R>>3) & 0x1F
				g6 := uint16(nc.G>>2) & 0x3F
				b5 := uint16(nc.B>>3) & 0x1F
				rgb565 := (r5 << 11) | (g6 << 5) | b5
				buf[off] = byte(rgb565 >> 8)
				buf[off+1] = byte(rgb565)
			}
		}

		if err := d.setWindowPhysical(0, y0, clearWidth-1, y1); err != nil {
			return err
		}
		if err := d.pixelData(buf, clearWidth*bytesPerPixel); err != nil {
			return err
		}

		if y0 < d.height {
			copyRows := rows
			if y0+copyRows > d.height {
				copyRows = d.height - y0
			}
			if copyRows > 0 {
				for row := 0; row < copyRows; row++ {
					srcStart := row * clearWidth * bytesPerPixel
					srcEnd := srcStart + d.width*bytesPerPixel
					dstStart := (y0 + row) * d.width * bytesPerPixel
					dstEnd := dstStart + d.width*bytesPerPixel
					copy(d.prevFrame[dstStart:dstEnd], buf[srcStart:srcEnd])
				}
			}
		}
	}

	d.hasPrevFrame = true
	return nil
}

func (d *Display) Flush() error {
	bytesPerPixel := d.bytesPerPixel()

	frameSize := d.width * d.height * bytesPerPixel

	if d.streamFullFrame {
		buf, _ := d.encodeFullFrame()
		if len(d.prevFrame) != len(buf) {
			d.prevFrame = make([]byte, len(buf))
			d.hasPrevFrame = false
		}
		if d.hasPrevFrame && bytes.Equal(buf, d.prevFrame) {
			return nil
		}
		if err := d.streamFullFrameBuffer(buf); err != nil {
			return err
		}
		copy(d.prevFrame, buf)
		d.hasPrevFrame = true
		return nil
	}

	if len(d.prevFrame) != frameSize {
		d.prevFrame = make([]byte, frameSize)
		d.hasPrevFrame = false
	}

	rowsPerBatch := d.batchRows
	if rowsPerBatch < 1 {
		rowsPerBatch = 1
	}
	if rowsPerBatch > d.height {
		rowsPerBatch = d.height
	}

	batchCap := d.width * rowsPerBatch * bytesPerPixel
	if cap(d.flushBuf) < batchCap {
		d.flushBuf = make([]byte, batchCap)
	}

	for batchStart := 0; batchStart < d.height; batchStart += rowsPerBatch {
		y0 := batchStart
		y1 := y0 + rowsPerBatch - 1
		if y1 >= d.height {
			y1 = d.height - 1
		}
		if d.bottomUp {
			y1 = d.height - 1 - batchStart
			y0 = y1 - rowsPerBatch + 1
			if y0 < 0 {
				y0 = 0
			}
		}
		rows := y1 - y0 + 1
		n := d.width * rows * bytesPerPixel
		buf := d.flushBuf[:n]
		regionOffset := y0 * d.width * bytesPerPixel

		for y := y0; y <= y1; y++ {
			for x := 0; x < d.width; x++ {
				c := d.img.NRGBAAt(x, y)
				off := ((y-y0)*d.width + x) * bytesPerPixel
				if d.pixfmt == 0x66 {
					r, g, b := reorderRGB(d.colorOrd, c.R, c.G, c.B)
					buf[off] = r & 0xFC
					buf[off+1] = g & 0xFC
					buf[off+2] = b & 0xFC
				} else {
					r5 := uint16(c.R>>3) & 0x1F
					g6 := uint16(c.G>>2) & 0x3F
					b5 := uint16(c.B>>3) & 0x1F
					rgb565 := (r5 << 11) | (g6 << 5) | b5
					buf[off] = byte(rgb565 >> 8)
					buf[off+1] = byte(rgb565)
				}
			}
		}

		prevRegion := d.prevFrame[regionOffset : regionOffset+n]
		if d.hasPrevFrame && bytes.Equal(buf, prevRegion) {
			continue
		}

		if err := d.setWindow(0, y0, d.width-1, y1); err != nil {
			return err
		}
		if err := d.pixelData(buf, d.width*bytesPerPixel); err != nil {
			return err
		}
		copy(prevRegion, buf)
	}
	d.hasPrevFrame = true

	return nil
}

func (d *Display) Close() error {
	var firstErr error
	if d.dc != nil {
		if err := d.dc.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if d.rst != nil {
		if err := d.rst.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if d.spi != nil {
		if err := d.spi.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
