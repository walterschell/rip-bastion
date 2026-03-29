package network

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
)

// Info holds network configuration details.
type Info struct {
	InterfaceName string
	IP            string
	Netmask       string
	Gateway       string
	DNS           []string
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
	return info, nil
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
