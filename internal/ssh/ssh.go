package ssh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Status holds SSH service state.
type Status struct {
	Running bool
}

// Get reports whether an SSH daemon process is currently running.
func Get() (*Status, error) {
	running, err := isSSHDRunning()
	if err != nil {
		return nil, fmt.Errorf("checking sshd: %w", err)
	}
	return &Status{Running: running}, nil
}

func isSSHDRunning() (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, fmt.Errorf("reading /proc: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
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
		if comm == "sshd" || comm == "dropbear" {
			return true, nil
		}
	}

	return false, nil
}
