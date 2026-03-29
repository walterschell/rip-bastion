package mdns

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Status holds mDNS service state.
type Status struct {
	Running  bool
	Hostname string // e.g. "raspberrypi.local"
}

// Get returns the current mDNS status.
// It checks if avahi-daemon is running via /proc filesystem and reads the
// hostname from /etc/hostname, appending ".local".
func Get() (*Status, error) {
	running, err := isAvahiRunning()
	if err != nil {
		return nil, fmt.Errorf("checking avahi-daemon: %w", err)
	}

	hostname, err := readHostname()
	if err != nil {
		return nil, fmt.Errorf("reading hostname: %w", err)
	}

	return &Status{
		Running:  running,
		Hostname: hostname + ".local",
	}, nil
}

// isAvahiRunning walks /proc looking for a process named "avahi-daemon".
func isAvahiRunning() (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, fmt.Errorf("reading /proc: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Only look at numeric directories (PIDs)
		name := entry.Name()
		isPID := true
		for _, ch := range name {
			if ch < '0' || ch > '9' {
				isPID = false
				break
			}
		}
		if !isPID {
			continue
		}

		commPath := filepath.Join("/proc", name, "comm")
		data, err := os.ReadFile(commPath)
		if err != nil {
			continue
		}
		comm := strings.TrimSpace(string(data))
		if comm == "avahi-daemon" {
			return true, nil
		}
	}
	return false, nil
}

// readHostname reads the system hostname from /etc/hostname.
func readHostname() (string, error) {
	data, err := os.ReadFile("/etc/hostname")
	if err != nil {
		// Fall back to os.Hostname()
		hostname, herr := os.Hostname()
		if herr != nil {
			return "unknown", nil
		}
		return strings.TrimSpace(hostname), nil
	}
	return strings.TrimSpace(string(data)), nil
}
