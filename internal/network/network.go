package network

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Info holds network configuration details.
type Info struct {
	InterfaceName string
	IP            string
	Netmask       string
	CIDR          string
	Gateway       string
	DNS           []string
	ExternalIP    string
}

// Get returns the primary network interface information.
// It picks the first non-loopback interface with an IPv4 address.
func Get() (*Info, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("listing interfaces: %w", err)
	}

	var info *Info
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ipNet *net.IPNet
			switch v := addr.(type) {
			case *net.IPNet:
				ipNet = v
			case *net.IPAddr:
				ipNet = &net.IPNet{IP: v.IP, Mask: v.IP.DefaultMask()}
			}
			if ipNet == nil {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue
			}
			// Skip link-local addresses
			if ip4[0] == 169 && ip4[1] == 254 {
				continue
			}

			netmask := fmt.Sprintf("%d.%d.%d.%d",
				ipNet.Mask[0], ipNet.Mask[1], ipNet.Mask[2], ipNet.Mask[3])

			info = &Info{
				InterfaceName: iface.Name,
				IP:            ip4.String(),
				Netmask:       netmask,
				CIDR:          toCIDR(ip4.String(), ipNet.Mask),
			}
			break
		}
		if info != nil {
			break
		}
	}

	if info == nil {
		return nil, fmt.Errorf("no suitable network interface found")
	}

	info.Gateway = getGateway(info.InterfaceName)
	info.DNS = getDNS()
	info.ExternalIP = getExternalIP()
	return info, nil
}

// InterfaceCIDR returns the first non-link-local IPv4 CIDR assigned to iface.
func InterfaceCIDR(ifaceName string) string {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return ""
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}

	for _, addr := range addrs {
		var ipNet *net.IPNet
		switch v := addr.(type) {
		case *net.IPNet:
			ipNet = v
		case *net.IPAddr:
			ipNet = &net.IPNet{IP: v.IP, Mask: v.IP.DefaultMask()}
		}
		if ipNet == nil {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil {
			continue
		}
		if ip4[0] == 169 && ip4[1] == 254 {
			continue
		}
		return toCIDR(ip4.String(), ipNet.Mask)
	}

	return ""
}

func toCIDR(ip string, mask net.IPMask) string {
	ones, bits := mask.Size()
	if bits != 32 || ones < 0 {
		return ""
	}
	return fmt.Sprintf("%s/%d", ip, ones)
}

// InterfaceByteCounters returns total received/transmitted byte counters for
// iface from /sys/class/net.
func InterfaceByteCounters(iface string) (rxBytes, txBytes uint64, err error) {
	rxPath := filepath.Join("/sys/class/net", iface, "statistics", "rx_bytes")
	txPath := filepath.Join("/sys/class/net", iface, "statistics", "tx_bytes")

	rxRaw, err := os.ReadFile(rxPath)
	if err != nil {
		return 0, 0, fmt.Errorf("reading %s: %w", rxPath, err)
	}
	txRaw, err := os.ReadFile(txPath)
	if err != nil {
		return 0, 0, fmt.Errorf("reading %s: %w", txPath, err)
	}

	rxBytes, err = strconv.ParseUint(strings.TrimSpace(string(rxRaw)), 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parsing rx bytes for %s: %w", iface, err)
	}
	txBytes, err = strconv.ParseUint(strings.TrimSpace(string(txRaw)), 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parsing tx bytes for %s: %w", iface, err)
	}

	return rxBytes, txBytes, nil
}

// getGateway parses /proc/net/route to find the default gateway.
func getGateway(iface string) string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Skip header line
	scanner.Scan()
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		// fields[1] is Destination, fields[2] is Gateway, fields[0] is Iface
		if fields[1] != "00000000" {
			continue
		}
		// Default route found; convert hex gateway to dotted-quad (little-endian)
		gwHex := fields[2]
		if len(gwHex) != 8 {
			continue
		}
		b, err := hex.DecodeString(gwHex)
		if err != nil || len(b) != 4 {
			continue
		}
		// Little-endian byte order
		return fmt.Sprintf("%d.%d.%d.%d", b[3], b[2], b[1], b[0])
	}
	return ""
}

// getDNS parses /etc/resolv.conf for nameserver entries.
func getDNS() []string {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	defer f.Close()

	var servers []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "nameserver") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				servers = append(servers, parts[1])
			}
		}
	}
	return servers
}

// getExternalIP performs a best-effort lookup of the public IPv4 address.
// It times out quickly so dashboard refreshes remain responsive.
func getExternalIP() string {
	client := &http.Client{Timeout: 750 * time.Millisecond}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}

	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}
