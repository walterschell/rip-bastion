//go:build linux

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
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

	spiIocMagic = 107 // 'k'

	defaultWidth  = 320
	defaultHeight = 480
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

func newSPIDev(path string, speed uint32, bitsPerWord uint8) (*spiDev, uint8, error) {
	fd, err := unix.Open(path, unix.O_RDWR, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("open %s: %w", path, err)
	}

	mode := uint8(0)
	if err := ioctlSetPtr(fd, spiIOCWriteMode, unsafe.Pointer(&mode)); err != nil {
		_ = unix.Close(fd)
		return nil, 0, fmt.Errorf("set SPI mode: %w", err)
	}

	bits := bitsPerWord
	if err := ioctlSetPtr(fd, spiIOCWriteBitsPerWrd, unsafe.Pointer(&bits)); err != nil {
		if bitsPerWord == 16 {
			fallback := uint8(8)
			if fallbackErr := ioctlSetPtr(fd, spiIOCWriteBitsPerWrd, unsafe.Pointer(&fallback)); fallbackErr != nil {
				_ = unix.Close(fd)
				return nil, 0, fmt.Errorf("set SPI bits-per-word 16 failed (%v), fallback to 8-bit also failed (%v)", err, fallbackErr)
			}
			bits = fallback
		} else {
			_ = unix.Close(fd)
			return nil, 0, fmt.Errorf("set SPI bits-per-word: %w", err)
		}
	}

	maxSpeed := uint32(speed)
	if err := ioctlSetPtr(fd, spiIOCWriteMaxSpeedHz, unsafe.Pointer(&maxSpeed)); err != nil {
		_ = unix.Close(fd)
		return nil, 0, fmt.Errorf("set SPI max speed: %w", err)
	}

	maxTx := readSpidevBufsiz()
	if maxTx <= 0 {
		maxTx = 4096
	}

	return &spiDev{fd: fd, speed: speed, bpw: bits, maxTx: maxTx}, bits, nil
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
	pin        int
	usePinctrl bool
}

func newGPIOOut(pin int) (*gpioOut, error) {
	if err := ensureGPIO(pin); err == nil {
		if err := os.WriteFile(filepath.Join(gpioPath(pin), "direction"), []byte("out"), 0o644); err != nil {
			return nil, fmt.Errorf("set gpio%d direction: %w", pin, err)
		}
		return &gpioOut{pin: pin}, nil
	}

	if _, lookErr := exec.LookPath("pinctrl"); lookErr != nil {
		return nil, fmt.Errorf("gpio%d unavailable via sysfs and pinctrl not found in PATH", pin)
	}
	if err := runPinctrl("set", strconv.Itoa(pin), "op"); err != nil {
		return nil, fmt.Errorf("configure gpio%d via pinctrl: %w", pin, err)
	}
	return &gpioOut{pin: pin, usePinctrl: true}, nil
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
	if g.usePinctrl {
		level := "dl"
		if v {
			level = "dh"
		}
		if err := runPinctrl("set", strconv.Itoa(g.pin), level); err != nil {
			return fmt.Errorf("write gpio%d via pinctrl: %w", g.pin, err)
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

func runPinctrl(args ...string) error {
	cmd := exec.Command("pinctrl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("pinctrl %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

type probe struct {
	spi        *spiDev
	dc         *gpioOut
	rst        *gpioOut
	width      int
	height     int
	xoff       int
	yoff       int
	packCmd16  bool
	packData16 bool
	swap16     bool
	pix666     bool
}

func rgbFrom565(c uint16) (byte, byte, byte) {
	r5 := (c >> 11) & 0x1F
	g6 := (c >> 5) & 0x3F
	b5 := c & 0x1F

	r8 := byte((r5 << 3) | (r5 >> 2))
	g8 := byte((g6 << 2) | (g6 >> 4))
	b8 := byte((b5 << 3) | (b5 >> 2))
	return r8, g8, b8
}

func (p *probe) cmd(cmd byte, data ...byte) error {
	if err := p.dc.set(false); err != nil {
		return err
	}
	if err := p.spi.write(p.pack([]byte{cmd}, p.packCmd16)); err != nil {
		return fmt.Errorf("spi cmd 0x%02x: %w", cmd, err)
	}
	if len(data) > 0 {
		if err := p.data(data); err != nil {
			return err
		}
	}
	return nil
}

func (p *probe) data(data []byte) error {
	if err := p.dc.set(true); err != nil {
		return err
	}
	if err := p.spi.write(p.pack(data, p.packData16)); err != nil {
		return fmt.Errorf("spi data write: %w", err)
	}
	return nil
}

func (p *probe) pack(data []byte, enabled bool) []byte {
	if !enabled {
		return data
	}
	out := make([]byte, 0, len(data)*2)
	for _, b := range data {
		out = append(out, 0x00, b)
	}
	return out
}

func (p *probe) reset() error {
	if err := p.rst.set(true); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	if err := p.rst.set(false); err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	if err := p.rst.set(true); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond)
	return nil
}

func (p *probe) initILI9486(madctl, pixfmt byte) error {
	if err := p.cmd(0x01); err != nil { // Software reset
		return err
	}
	time.Sleep(120 * time.Millisecond)

	if err := p.cmd(0x11); err != nil { // Sleep out
		return err
	}
	time.Sleep(120 * time.Millisecond)

	if err := p.cmd(0x3A, pixfmt); err != nil { // RGB format
		return err
	}

	if err := p.cmd(0x36, madctl); err != nil { // MADCTL
		return err
	}

	if err := p.cmd(0xB6, 0x00, 0x22, 0x3B); err != nil {
		return err
	}

	if err := p.cmd(0xC0, 0x0D, 0x0D); err != nil {
		return err
	}
	if err := p.cmd(0xC1, 0x43, 0x00); err != nil {
		return err
	}
	if err := p.cmd(0xC2, 0x00); err != nil {
		return err
	}
	if err := p.cmd(0xC5, 0x00, 0x48); err != nil {
		return err
	}

	if err := p.cmd(0xE0,
		0x0F, 0x24, 0x1C, 0x0A, 0x0F, 0x08, 0x43, 0x88,
		0x32, 0x0F, 0x10, 0x06, 0x0F, 0x07, 0x00); err != nil {
		return err
	}
	if err := p.cmd(0xE1,
		0x0F, 0x38, 0x30, 0x09, 0x0F, 0x0F, 0x4E, 0x77,
		0x3C, 0x07, 0x10, 0x05, 0x23, 0x1B, 0x00); err != nil {
		return err
	}

	if err := p.cmd(0x29); err != nil { // Display ON
		return err
	}
	time.Sleep(20 * time.Millisecond)

	return nil
}

func (p *probe) initILI9341(madctl, pixfmt byte) error {
	if err := p.cmd(0x01); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond)

	if err := p.cmd(0x11); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond)

	if err := p.cmd(0x3A, pixfmt); err != nil {
		return err
	}
	if err := p.cmd(0x36, madctl); err != nil {
		return err
	}

	if err := p.cmd(0xC0, 0x23); err != nil {
		return err
	}
	if err := p.cmd(0xC1, 0x10); err != nil {
		return err
	}
	if err := p.cmd(0xC5, 0x3E, 0x28); err != nil {
		return err
	}
	if err := p.cmd(0xC7, 0x86); err != nil {
		return err
	}

	if err := p.cmd(0xE0,
		0x0F, 0x31, 0x2B, 0x0C, 0x0E, 0x08, 0x4E, 0xF1,
		0x37, 0x07, 0x10, 0x03, 0x0E, 0x09, 0x00); err != nil {
		return err
	}
	if err := p.cmd(0xE1,
		0x00, 0x0E, 0x14, 0x03, 0x11, 0x07, 0x31, 0xC1,
		0x48, 0x08, 0x0F, 0x0C, 0x31, 0x36, 0x0F); err != nil {
		return err
	}

	if err := p.cmd(0x29); err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	return nil
}

func (p *probe) initHX8357D(madctl, pixfmt byte) error {
	if err := p.cmd(0x01); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond)

	if err := p.cmd(0x11); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)

	if err := p.cmd(0x36, madctl); err != nil {
		return err
	}
	if err := p.cmd(0x3A, pixfmt); err != nil {
		return err
	}

	if err := p.cmd(0xB9, 0xFF, 0x83, 0x57); err != nil {
		return err
	}
	if err := p.cmd(0xB6, 0x2C); err != nil {
		return err
	}

	if err := p.cmd(0x29); err != nil {
		return err
	}
	time.Sleep(20 * time.Millisecond)
	return nil
}

func (p *probe) initByProfile(profile string, madctl, pixfmt byte) error {
	switch profile {
	case "ili9486":
		return p.initILI9486(madctl, pixfmt)
	case "ili9341":
		return p.initILI9341(madctl, pixfmt)
	case "hx8357d":
		return p.initHX8357D(madctl, pixfmt)
	default:
		return fmt.Errorf("unknown profile %q", profile)
	}
}

func (p *probe) setWindow(x0, y0, x1, y1 int) error {
	x0 += p.xoff
	x1 += p.xoff
	y0 += p.yoff
	y1 += p.yoff

	if err := p.cmd(0x2A,
		byte(x0>>8), byte(x0),
		byte(x1>>8), byte(x1)); err != nil {
		return err
	}
	if err := p.cmd(0x2B,
		byte(y0>>8), byte(y0),
		byte(y1>>8), byte(y1)); err != nil {
		return err
	}
	if err := p.cmd(0x2C); err != nil {
		return err
	}
	return nil
}

func (p *probe) fill(color uint16) error {
	if err := p.setWindow(0, 0, p.width-1, p.height-1); err != nil {
		return err
	}

	pix := p.width * p.height
	if p.pix666 {
		r8, g8, b8 := rgbFrom565(color)
		chunkLen := 4095
		chunk := make([]byte, chunkLen)
		for i := 0; i+2 < len(chunk); i += 3 {
			chunk[i] = r8 & 0xFC
			chunk[i+1] = g8 & 0xFC
			chunk[i+2] = b8 & 0xFC
		}

		remaining := pix * 3
		for remaining > 0 {
			n := len(chunk)
			if n > remaining {
				n = remaining
			}
			n -= n % 3
			if n == 0 {
				return fmt.Errorf("rgb666 stream alignment error: remaining=%d", remaining)
			}
			if err := p.data(chunk[:n]); err != nil {
				return err
			}
			remaining -= n
		}
		return nil
	}

	h := byte(color >> 8)
	l := byte(color)
	if p.swap16 {
		h, l = l, h
	}

	chunk := make([]byte, 4096)
	for i := 0; i < len(chunk); i += 2 {
		chunk[i] = h
		chunk[i+1] = l
	}

	remaining := pix * 2
	for remaining > 0 {
		n := len(chunk)
		if n > remaining {
			n = remaining
		}
		if err := p.data(chunk[:n]); err != nil {
			return err
		}
		remaining -= n
	}
	return nil
}

func (p *probe) bars() error {
	if err := p.setWindow(0, 0, p.width-1, p.height-1); err != nil {
		return err
	}

	colors := []uint16{0xF800, 0x07E0, 0x001F, 0xFFFF, 0x0000, 0xFFE0, 0xF81F, 0x07FF}
	chunkRows := 8
	bytesPerPixel := 2
	if p.pix666 {
		bytesPerPixel = 3
	}
	line := make([]byte, p.width*bytesPerPixel*chunkRows)

	for y := 0; y < p.height; y += chunkRows {
		rows := chunkRows
		if y+rows > p.height {
			rows = p.height - y
		}
		for row := 0; row < rows; row++ {
			for x := 0; x < p.width; x++ {
				idx := (x * len(colors)) / p.width
				c := colors[idx]
				off := row*p.width*bytesPerPixel + x*bytesPerPixel
				if p.pix666 {
					r8, g8, b8 := rgbFrom565(c)
					line[off] = r8 & 0xFC
					line[off+1] = g8 & 0xFC
					line[off+2] = b8 & 0xFC
				} else {
					h := byte(c >> 8)
					l := byte(c)
					if p.swap16 {
						h, l = l, h
					}
					line[off] = h
					line[off+1] = l
				}
			}
		}
		if err := p.data(line[:rows*p.width*bytesPerPixel]); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	spiPath := flag.String("spi", "/dev/spidev0.0", "SPI device path")
	speed := flag.Uint("speed", 16000000, "SPI clock in Hz")
	dcPin := flag.Int("dc", 24, "GPIO pin for D/C")
	rstPin := flag.Int("rst", 25, "GPIO pin for reset")
	width := flag.Int("width", defaultWidth, "panel width in pixels")
	height := flag.Int("height", defaultHeight, "panel height in pixels")
	profile := flag.String("profile", "ili9486", "init profile: ili9486|ili9341|hx8357d|auto")
	bpw := flag.Uint("bpw", 8, "SPI bits per word: 8 or 16")
	pack16 := flag.Bool("pack16", false, "send each command/data byte as 16-bit frame (00 xx)")
	packCmd16 := flag.Bool("packcmd16", false, "send command bytes as 16-bit frame (00 xx)")
	packData16 := flag.Bool("packdata16", false, "send data bytes as 16-bit frame (00 xx)")
	swap16 := flag.Bool("swap16", false, "swap RGB565 byte order (LSB then MSB)")
	pixfmt := flag.String("pixfmt", "565", "pixel format: 565 or 666")
	madctl := flag.Int("madctl", 0x28, "MADCTL register value (0-255)")
	xoff := flag.Int("xoff", 0, "GRAM X start offset")
	yoff := flag.Int("yoff", 0, "GRAM Y start offset")
	flag.Parse()

	if os.Geteuid() != 0 {
		log.Fatal("run as root (GPIO sysfs + spidev access required)")
	}

	if *bpw != 8 && *bpw != 16 {
		log.Fatal("-bpw must be 8 or 16")
	}

	pixfmtVal := byte(0x55)
	switch *pixfmt {
	case "565":
		pixfmtVal = 0x55
	case "666":
		pixfmtVal = 0x66
	default:
		log.Fatal("-pixfmt must be 565 or 666")
	}

	if *madctl < 0 || *madctl > 255 {
		log.Fatal("-madctl must be 0..255")
	}

	spi, actualBPW, err := newSPIDev(*spiPath, uint32(*speed), uint8(*bpw))
	if err != nil {
		log.Fatalf("SPI open/setup failed: %v", err)
	}
	if actualBPW != uint8(*bpw) {
		log.Printf("requested bpw=%d but kernel accepted bpw=%d; keeping logical 16-bit packed writes enabled", *bpw, actualBPW)
	}
	defer func() {
		if closeErr := spi.Close(); closeErr != nil {
			log.Printf("SPI close error: %v", closeErr)
		}
	}()

	dc, err := newGPIOOut(*dcPin)
	if err != nil {
		log.Fatalf("D/C GPIO setup failed: %v", err)
	}

	rst, err := newGPIOOut(*rstPin)
	if err != nil {
		log.Fatalf("RST GPIO setup failed: %v", err)
	}

	cmdPacked := *pack16 || *packCmd16
	dataPacked := *pack16 || *packData16
	p := &probe{spi: spi, dc: dc, rst: rst, width: *width, height: *height, xoff: *xoff, yoff: *yoff, packCmd16: cmdPacked, packData16: dataPacked, swap16: *swap16, pix666: *pixfmt == "666"}

	profiles := []string{*profile}
	if *profile == "auto" {
		profiles = []string{"ili9486", "ili9341", "hx8357d"}
	}

	for _, prof := range profiles {
		log.Printf("SPI probe start: spi=%s speed=%d bpw=%d packCmd16=%t packData16=%t swap16=%t profile=%s pixfmt=%s madctl=0x%02X xoff=%d yoff=%d dc=%d rst=%d %dx%d", *spiPath, *speed, *bpw, cmdPacked, dataPacked, *swap16, prof, *pixfmt, *madctl, *xoff, *yoff, *dcPin, *rstPin, *width, *height)
		if err := p.reset(); err != nil {
			log.Fatalf("panel reset failed: %v", err)
		}
		if err := p.initByProfile(prof, byte(*madctl), pixfmtVal); err != nil {
			log.Printf("init %s failed: %v", prof, err)
			continue
		}

		log.Printf("drawing red")
		if err := p.fill(0xF800); err != nil {
			log.Printf("fill red failed for %s: %v", prof, err)
			continue
		}
		time.Sleep(800 * time.Millisecond)

		log.Printf("drawing green")
		if err := p.fill(0x07E0); err != nil {
			log.Printf("fill green failed for %s: %v", prof, err)
			continue
		}
		time.Sleep(800 * time.Millisecond)

		log.Printf("drawing blue")
		if err := p.fill(0x001F); err != nil {
			log.Printf("fill blue failed for %s: %v", prof, err)
			continue
		}
		time.Sleep(800 * time.Millisecond)

		log.Printf("drawing white")
		if err := p.fill(0xFFFF); err != nil {
			log.Printf("fill white failed for %s: %v", prof, err)
			continue
		}
		time.Sleep(800 * time.Millisecond)

		log.Printf("drawing bars")
		if err := p.bars(); err != nil {
			log.Printf("bars failed for %s: %v", prof, err)
			continue
		}

		log.Printf("probe complete with profile=%s", prof)
		return
	}

	log.Fatal("probe finished, no profile produced a successful draw sequence")
}
