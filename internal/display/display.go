package display

import "github.com/walterschell/rip-bastion/internal/sysinfo"

// Display is the interface that all rendering backends must implement.
type Display interface {
	// Render updates the display with the latest system snapshot.
	Render(snap *sysinfo.Snapshot) error
	// Close releases any resources held by the display.
	Close() error
}
