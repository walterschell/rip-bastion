// Package proxy implements a built-in TLS-terminating SNI reverse proxy with
// YAML-based configuration and hot reload.
//
// Usage:
//
//	srv, err := proxy.NewServer("/etc/rip-bastion/proxy.yaml")
//	if err != nil { log.Fatal(err) }
//	log.Fatal(srv.ListenAndServe())
package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// routeTable is the hot-swappable routing state derived from a Config.
type routeTable struct {
	cfg     *Config
	proxies map[string]*httputil.ReverseProxy // normalised host → backend proxy
	certs   map[string]*tls.Certificate       // normalised host → TLS cert
}

// Server is a built-in reverse proxy that reads its routing rules from a YAML
// file and hot-reloads whenever that file changes.
type Server struct {
	configPath string

	mu    sync.RWMutex
	table *routeTable

	mdns *mdnsRegistry

	stopWatcher func()
}

// NewServer creates a Server and performs an initial config load.  The YAML
// file at configPath is also watched for changes so that routes are updated
// without a restart.
func NewServer(configPath string) (*Server, error) {
	s := &Server{
		configPath: configPath,
		mdns:       newMDNSRegistry(),
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("proxy: initial config load: %w", err)
	}
	if err := s.apply(cfg); err != nil {
		return nil, fmt.Errorf("proxy: initial apply: %w", err)
	}

	stopWatcher, err := watchConfig(configPath, func() {
		log.Printf("proxy: config file changed – reloading %s", configPath)
		newCfg, err := LoadConfig(configPath)
		if err != nil {
			log.Printf("proxy: reload failed (keeping current config): %v", err)
			return
		}
		if err := s.apply(newCfg); err != nil {
			log.Printf("proxy: apply failed (keeping current config): %v", err)
			return
		}
		log.Printf("proxy: config reloaded – %d route(s) active", len(newCfg.Routes))
	})
	if err != nil {
		return nil, fmt.Errorf("proxy: starting config watcher: %w", err)
	}
	s.stopWatcher = stopWatcher

	return s, nil
}

// apply builds a new routeTable from cfg and atomically installs it.
func (s *Server) apply(cfg *Config) error {
	table, err := buildRouteTable(cfg)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.table = table
	s.mu.Unlock()

	// Update mDNS registrations.
	httpPort := portFromListenAddr(cfg.Listen.HTTP, 80)
	httpsPort := portFromListenAddr(cfg.Listen.HTTPS, 443)
	desired := make([]mdnsTarget, 0, len(cfg.Routes))
	for _, r := range cfg.Routes {
		if r.MDNS && r.Host != "" {
			desired = append(desired, mdnsTarget{
				hostname:  r.Host,
				httpPort:  httpPort,
				httpsPort: httpsPort,
			})
		}
	}
	s.mdns.Sync(desired)

	return nil
}

// buildRouteTable constructs proxies and TLS certs for every route in cfg.
func buildRouteTable(cfg *Config) (*routeTable, error) {
	proxies := make(map[string]*httputil.ReverseProxy, len(cfg.Routes))
	certs := make(map[string]*tls.Certificate, len(cfg.Routes))

	for _, r := range cfg.Routes {
		if r.Host == "" || r.Target == "" {
			log.Printf("proxy: skipping incomplete route %q (missing host or target)", r.Name)
			continue
		}

		// Build the reverse proxy for this route.
		target, err := url.Parse(r.Target)
		if err != nil {
			return nil, fmt.Errorf("route %q: invalid target URL %q: %w", r.Name, r.Target, err)
		}
		rp := httputil.NewSingleHostReverseProxy(target)
		rp.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
			log.Printf("proxy: upstream error [%s → %s]: %v", req.Host, target, err)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		}
		proxies[normaliseHost(r.Host)] = rp

		// Build or load the TLS certificate.
		var cert *tls.Certificate
		switch cfg.TLS.Mode {
		case "provided":
			cert, err = loadProvidedCert(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		default: // "self-signed"
			cert, err = loadOrCreateSelfSignedCert(r.Host, cfg.TLS.CertDir)
		}
		if err != nil {
			return nil, fmt.Errorf("route %q: TLS cert: %w", r.Name, err)
		}
		certs[normaliseHost(r.Host)] = cert
	}

	return &routeTable{cfg: cfg, proxies: proxies, certs: certs}, nil
}

// ListenAndServe starts the HTTP (redirect) and HTTPS (proxy) servers and
// blocks until both exit.  It is the caller's responsibility to call Close to
// shut down cleanly.
func (s *Server) ListenAndServe() error {
	t := s.currentTable()
	httpAddr := t.cfg.Listen.HTTP
	httpsAddr := t.cfg.Listen.HTTPS

	// --- HTTPS server ---
	tlsCfg := &tls.Config{
		GetCertificate: s.getCertificate,
		MinVersion:     tls.VersionTLS12,
	}
	httpsServer := &http.Server{
		Addr:         httpsAddr,
		Handler:      http.HandlerFunc(s.serveHTTPS),
		TLSConfig:    tlsCfg,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// --- HTTP redirect server ---
	httpServer := &http.Server{
		Addr:         httpAddr,
		Handler:      http.HandlerFunc(s.serveHTTPRedirect),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	errCh := make(chan error, 2)

	go func() {
		log.Printf("proxy: HTTPS listening on %s", httpsAddr)
		ln, err := net.Listen("tcp", httpsAddr)
		if err != nil {
			errCh <- fmt.Errorf("proxy: HTTPS listen: %w", err)
			return
		}
		errCh <- httpsServer.ServeTLS(ln, "", "")
	}()

	go func() {
		log.Printf("proxy: HTTP (redirect) listening on %s", httpAddr)
		errCh <- httpServer.ListenAndServe()
	}()

	return <-errCh
}

// Close stops the config file watcher and releases mDNS registrations.
// It does not shut down the HTTP servers; callers should use a context with
// ListenAndServe or shut down the servers externally.
func (s *Server) Close() {
	if s.stopWatcher != nil {
		s.stopWatcher()
	}
	s.mdns.Close()
}

// ListenAndServeContext starts the proxy and shuts it down gracefully when ctx
// is cancelled.
func (s *Server) ListenAndServeContext(ctx context.Context) error {
	t := s.currentTable()
	httpAddr := t.cfg.Listen.HTTP
	httpsAddr := t.cfg.Listen.HTTPS

	tlsCfg := &tls.Config{
		GetCertificate: s.getCertificate,
		MinVersion:     tls.VersionTLS12,
	}
	httpsServer := &http.Server{
		Addr:         httpsAddr,
		Handler:      http.HandlerFunc(s.serveHTTPS),
		TLSConfig:    tlsCfg,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	httpServer := &http.Server{
		Addr:         httpAddr,
		Handler:      http.HandlerFunc(s.serveHTTPRedirect),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	errCh := make(chan error, 2)

	go func() {
		log.Printf("proxy: HTTPS listening on %s", httpsAddr)
		ln, err := net.Listen("tcp", httpsAddr)
		if err != nil {
			errCh <- fmt.Errorf("proxy: HTTPS listen: %w", err)
			return
		}
		if err := httpsServer.ServeTLS(ln, "", ""); err != http.ErrServerClosed {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	go func() {
		log.Printf("proxy: HTTP (redirect) listening on %s", httpAddr)
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	// Wait for context cancellation and shut down gracefully.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpsServer.Shutdown(shutCtx)
		_ = httpServer.Shutdown(shutCtx)
	}()

	// Return the first non-nil error (or nil if both shut down cleanly).
	var firstErr error
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.Close()
	return firstErr
}

// ----- internal helpers -----

func (s *Server) currentTable() *routeTable {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.table
}

// getCertificate is the tls.Config.GetCertificate callback.  It looks up the
// certificate for the SNI server name supplied by the client.
func (s *Server) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	t := s.currentTable()
	key := normaliseHost(hello.ServerName)
	if cert, ok := t.certs[key]; ok {
		return cert, nil
	}
	// No match – return a self-signed fallback cert for the bastion IP/hostname.
	if len(t.certs) > 0 {
		// Return the first available cert so TLS can at least complete.
		for _, c := range t.certs {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no certificate for %q", hello.ServerName)
}

// serveHTTPS handles HTTPS requests: route to the matching backend or show the
// fallback page.
func (s *Server) serveHTTPS(w http.ResponseWriter, r *http.Request) {
	t := s.currentTable()
	key := normaliseHost(r.Host)
	if rp, ok := t.proxies[key]; ok {
		rp.ServeHTTP(w, r)
		return
	}
	// Fallback: show the route listing.
	serveFallback(w, r, t.cfg.Fallback.Title, t.cfg.Routes)
}

// serveHTTPRedirect responds to plain-HTTP requests with a 301 redirect to
// the HTTPS equivalent.
func (s *Server) serveHTTPRedirect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	// Strip port from host if present (we want :443 for HTTPS).
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	target := "https://" + host + r.URL.RequestURI()
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// normaliseHost strips the port and lowercases the hostname for map lookups.
func normaliseHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func portFromListenAddr(addr string, fallback int) int {
	if addr == "" {
		return fallback
	}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		if p, parseErr := strconv.Atoi(port); parseErr == nil && p > 0 {
			return p
		}
		return fallback
	}
	if strings.HasPrefix(addr, ":") {
		if p, parseErr := strconv.Atoi(strings.TrimPrefix(addr, ":")); parseErr == nil && p > 0 {
			return p
		}
	}
	return fallback
}
