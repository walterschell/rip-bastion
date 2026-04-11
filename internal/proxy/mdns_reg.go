package proxy

import (
	"context"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

type mdnsTarget struct {
	hostname  string
	ip        string
	httpPort  int
	httpsPort int
	autoIP    bool
}

type mdnsProcSet struct {
	target   mdnsTarget
	httpSvc  *zeroconf.Server
	httpsSvc *zeroconf.Server
}

// avahiProcess wraps a running avahi-publish-address process.
type avahiProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

// mdnsRegistry manages in-process mDNS publications for host A-records and
// DNS-SD web services.
type mdnsRegistry struct {
	mu      sync.Mutex
	procs   map[string]mdnsProcSet  // fqdn hostname → running zeroconf processes
	avahi   map[string]*avahiProcess // fqdn hostname → avahi-publish process
	desired map[string]mdnsTarget
	localIP string

	stopCh    chan struct{}
	wakeCh    chan struct{}
	closeOnce sync.Once
}

func newMDNSRegistry() *mdnsRegistry {
	r := &mdnsRegistry{
		procs:   make(map[string]mdnsProcSet),
		avahi:   make(map[string]*avahiProcess),
		desired: make(map[string]mdnsTarget),
		stopCh:  make(chan struct{}),
		wakeCh:  make(chan struct{}, 1),
	}
	go r.run()
	return r
}

// Sync updates desired mDNS publications and schedules a reconcile.
func (r *mdnsRegistry) Sync(desired []mdnsTarget) {
	r.mu.Lock()
	desiredByHost := make(map[string]mdnsTarget, len(desired))

	for _, target := range desired {
		fqdn := normaliseMDNSHost(target.hostname)
		if fqdn == "" {
			continue
		}
		target.hostname = fqdn
		target.autoIP = target.ip == ""
		if target.httpPort <= 0 {
			target.httpPort = 80
		}
		if target.httpsPort <= 0 {
			target.httpsPort = 443
		}
		desiredByHost[fqdn] = target
	}
	r.desired = desiredByHost
	r.mu.Unlock()
	r.requestReconcile()
}

func (r *mdnsRegistry) run() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.reconcile()
		case <-r.wakeCh:
			r.reconcile()
		case <-r.stopCh:
			return
		}
	}
}

func (r *mdnsRegistry) requestReconcile() {
	select {
	case r.wakeCh <- struct{}{}:
	default:
	}
}

// reconcile makes running mDNS registrations match desired.
func (r *mdnsRegistry) reconcile() {
	r.mu.Lock()
	defer r.mu.Unlock()

	localIP := r.resolveLocalIP()
	if localIP != r.localIP {
		log.Printf("proxy/mdns: local IP changed: %q -> %q", r.localIP, localIP)
		r.localIP = localIP
	}

	// Stop zeroconf entries that are no longer desired.
	for host, set := range r.procs {
		if _, keep := r.desired[host]; !keep {
			r.stop(host, set)
		}
	}

	// Stop avahi entries that are no longer desired.
	for host, entry := range r.avahi {
		if _, keep := r.desired[host]; !keep {
			r.stopAvahi(host, entry)
		}
	}

	// (Re)start desired entries.
	for host, target := range r.desired {
		effective := target
		if effective.autoIP {
			effective.ip = localIP
		}
		if effective.ip == "" {
			if existing, running := r.procs[host]; running && existing.target.autoIP {
				r.stop(host, existing)
			}
			if entry, running := r.avahi[host]; running {
				r.stopAvahi(host, entry)
			}
			log.Printf("proxy/mdns: waiting for network before advertising %s", host)
			continue
		}

		// Zeroconf services.
		if existing, running := r.procs[host]; running {
			if mdnsTargetEqual(existing.target, effective) {
				// Already running with correct config.
			} else {
				r.stop(host, existing)
				set := r.start(effective)
				if set.httpSvc != nil || set.httpsSvc != nil {
					r.procs[host] = set
				}
			}
		} else {
			set := r.start(effective)
			if set.httpSvc != nil || set.httpsSvc != nil {
				r.procs[host] = set
			}
		}

		// Avahi hostname (A record) publishing.
		if existing, running := r.avahi[host]; running {
			if existing != nil {
				// Already running - avahi entries are stable, no need to restart
				// unless IP changed (handled by stopping above when IP changes).
			}
		} else {
			entry := r.startAvahi(effective)
			if entry != nil {
				r.avahi[host] = entry
			}
		}
	}
}

// Close stops all running mDNS registrations.
func (r *mdnsRegistry) Close() {
	r.closeOnce.Do(func() {
		close(r.stopCh)
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	for host, set := range r.procs {
		r.stop(host, set)
	}
	for host, entry := range r.avahi {
		r.stopAvahi(host, entry)
	}
}

// start launches in-process zeroconf service publishers. Each service
// registration also advertises the host's A/AAAA records so hostname
// resolution works alongside DNS-SD browsing.
// Must be called with r.mu held.
func (r *mdnsRegistry) start(target mdnsTarget) mdnsProcSet {
	set := mdnsProcSet{target: target}

	instanceName := strings.TrimSuffix(target.hostname, ".local")
	hostLabel := instanceName
	ipList := []string{target.ip}

	httpSvc, err := zeroconf.RegisterProxy(
		instanceName+" HTTP",
		"_http._tcp",
		"local.",
		target.httpPort,
		hostLabel,
		ipList,
		[]string{"path=/"},
		nil,
	)
	if err != nil {
		log.Printf("proxy/mdns: failed to advertise HTTP service for %s: %v", target.hostname, err)
	} else {
		set.httpSvc = httpSvc
		log.Printf("proxy/mdns: advertising in-process service %s _http._tcp:%d", target.hostname, target.httpPort)
	}

	httpsSvc, err := zeroconf.RegisterProxy(
		instanceName+" HTTPS",
		"_https._tcp",
		"local.",
		target.httpsPort,
		hostLabel,
		ipList,
		[]string{"path=/"},
		nil,
	)
	if err != nil {
		log.Printf("proxy/mdns: failed to advertise HTTPS service for %s: %v", target.hostname, err)
	} else {
		set.httpsSvc = httpsSvc
		log.Printf("proxy/mdns: advertising in-process service %s _https._tcp:%d", target.hostname, target.httpsPort)
	}

	if set.httpSvc == nil || set.httpsSvc == nil {
		log.Printf(
			"proxy/mdns: incomplete service publication for %s (http=%t https=%t)",
			target.hostname,
			set.httpSvc != nil,
			set.httpsSvc != nil,
		)
	}

	return set
}

// stop shuts down all in-process publishers for hostname and removes it.
// Must be called with r.mu held.
func (r *mdnsRegistry) stop(hostname string, set mdnsProcSet) {
	if set.httpSvc != nil {
		set.httpSvc.Shutdown()
	}
	if set.httpsSvc != nil {
		set.httpsSvc.Shutdown()
	}
	delete(r.procs, hostname)
	log.Printf("proxy/mdns: stopped advertising %s", hostname)
}

func normaliseMDNSHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
	if host == "" {
		return ""
	}
	if !strings.HasSuffix(host, ".local") {
		host += ".local"
	}
	return host
}

func mdnsTargetEqual(a, b mdnsTarget) bool {
	return a.hostname == b.hostname && a.ip == b.ip && a.httpPort == b.httpPort && a.httpsPort == b.httpsPort && a.autoIP == b.autoIP
}

// resolveLocalIP returns the first non-loopback IPv4 address of this host.
// It re-checks interfaces on every call so network down/up and DHCP changes
// are reflected while the process is running.
func (r *mdnsRegistry) resolveLocalIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				// Skip link-local
				if ip4[0] == 169 && ip4[1] == 254 {
					continue
				}
				return ip4.String()
			}
		}
	}
	return ""
}

// avahiPublishPath is the path to the avahi-publish-address command.
var avahiPublishPath = findAvahiPublish()

func findAvahiPublish() string {
	for _, p := range []string{"/usr/bin/avahi-publish-address", "/usr/bin/avahi-publish", "/bin/avahi-publish-address"} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	// Fall back to hoping it's in PATH.
	return "avahi-publish-address"
}

// startAvahi publishes a hostname A record using avahi-publish-address.
// This runs as a subprocess that stays alive to maintain the publication.
// Must be called with r.mu held.
func (r *mdnsRegistry) startAvahi(target mdnsTarget) *avahiProcess {
	// Validate IP.
	ip := net.ParseIP(target.ip)
	if ip == nil || ip.To4() == nil {
		log.Printf("proxy/mdns/avahi: invalid IPv4 address %q", target.ip)
		return nil
	}

	// Hostname should include .local for avahi-publish-address.
	hostname := target.hostname
	if !strings.HasSuffix(hostname, ".local") {
		hostname += ".local"
	}

	ctx, cancel := context.WithCancel(context.Background())

	// avahi-publish-address [options] <name> <address>
	// -R: don't fail if the address is already published (update it)
	cmd := exec.CommandContext(ctx, avahiPublishPath, "-R", hostname, target.ip)

	if err := cmd.Start(); err != nil {
		cancel()
		log.Printf("proxy/mdns/avahi: failed to start avahi-publish-address for %s: %v", hostname, err)
		return nil
	}

	// Monitor the process in the background.
	go func() {
		err := cmd.Wait()
		if err != nil && ctx.Err() == nil {
			// Process exited unexpectedly (not due to cancellation).
			log.Printf("proxy/mdns/avahi: avahi-publish-address for %s exited: %v", hostname, err)
		}
	}()

	log.Printf("proxy/mdns/avahi: publishing hostname %s -> %s (pid %d)", hostname, target.ip, cmd.Process.Pid)
	return &avahiProcess{cmd: cmd, cancel: cancel}
}

// stopAvahi terminates the avahi-publish-address process.
// Must be called with r.mu held.
func (r *mdnsRegistry) stopAvahi(hostname string, proc *avahiProcess) {
	if proc == nil {
		delete(r.avahi, hostname)
		return
	}

	// Cancel the context to kill the process.
	proc.cancel()

	delete(r.avahi, hostname)
	log.Printf("proxy/mdns/avahi: stopped publishing hostname %s", hostname)
}
