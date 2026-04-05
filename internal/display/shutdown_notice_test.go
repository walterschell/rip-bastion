package display

import (
	"image"
	"image/color"
	"testing"
)

func TestChooseNeedsRebootLayoutPrefersLargerTwoLineFit(t *testing.T) {
	lines, scale := chooseNeedsRebootLayout(220, 120)
	if scale < 1 {
		t.Fatalf("expected positive scale, got %d", scale)
	}
	if len(lines) != 2 || lines[0] != "NEEDS" || lines[1] != "REBOOT" {
		t.Fatalf("expected two-line layout, got %v", lines)
	}
}

func TestRenderNeedsRebootNotice(t *testing.T) {
	dev := NewImageDevice(320, 240)
	sd := NewSystemDisplay(dev)

	if err := sd.RenderNeedsRebootNotice(); err != nil {
		t.Fatalf("RenderNeedsRebootNotice returned error: %v", err)
	}

	border := warningBorderThickness(dev.Width(), dev.Height())
	innerProbe := dev.img.NRGBAAt(border+4, border+4)
	if innerProbe != toNRGBA(ColorBackground) {
		t.Fatalf("expected inner panel to be black, got %#v", innerProbe)
	}

	topBorder := image.Rect(0, 0, dev.Width(), border)
	if countColor(dev.img, topBorder, toNRGBA(ColorWarningBg)) == 0 {
		t.Fatal("expected warning yellow pixels in the border")
	}
	if countColor(dev.img, topBorder, toNRGBA(ColorWarningInk)) == 0 {
		t.Fatal("expected black hash pixels in the border")
	}
	if countColor(dev.img, dev.img.Bounds(), toNRGBA(ColorWarningTxt)) == 0 {
		t.Fatal("expected red text pixels in the rendered notice")
	}
}

func countColor(img *image.NRGBA, rect image.Rectangle, want color.NRGBA) int {
	rect = rect.Intersect(img.Bounds())
	count := 0
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if img.NRGBAAt(x, y) == want {
				count++
			}
		}
	}
	return count
}
