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
	"io"
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

// Shared transport for all reverse proxies. Using a single transport allows
// connection pooling across all backends and prevents resource exhaustion.
var sharedTransport = &http.Transport{
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	MaxIdleConns:          200,
	MaxIdleConnsPerHost:   20,
	MaxConnsPerHost:       100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ResponseHeaderTimeout: 0, // no timeout; rely on server timeouts
	ForceAttemptHTTP2:     true,
	// Disable keep-alives if they're causing connection reuse issues.
	// DisableKeepAlives: true,
}

// wsDialer is used for WebSocket backend connections.
var wsDialer = &net.Dialer{
	Timeout:   30 * time.Second,
	KeepAlive: 30 * time.Second,
}

// routeTable is the hot-swappable routing state derived from a Config.
type routeTable struct {
	cfg     *Config
	proxies map[string]*httputil.ReverseProxy // normalised host → backend proxy
	targets map[string]*url.URL               // normalised host → backend URL (for WebSocket)
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
	targets := make(map[string]*url.URL, len(cfg.Routes))
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

		// Create a custom Director that preserves X-Forwarded headers and handles
		// path joining correctly.
		targetURL := target // capture for closure
		director := func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = targetURL.Host

			// Join paths if target has a path prefix.
			if targetURL.Path != "" && targetURL.Path != "/" {
				req.URL.Path = singleJoiningSlash(targetURL.Path, req.URL.Path)
			}

			// Preserve or set X-Forwarded headers for the backend.
			if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
				if prior := req.Header.Get("X-Forwarded-For"); prior != "" {
					req.Header.Set("X-Forwarded-For", prior+", "+clientIP)
				} else {
					req.Header.Set("X-Forwarded-For", clientIP)
				}
			}
			if req.TLS != nil {
				req.Header.Set("X-Forwarded-Proto", "https")
			} else {
				req.Header.Set("X-Forwarded-Proto", "http")
			}
		}

		rp := &httputil.ReverseProxy{
			Director:      director,
			Transport:     sharedTransport,
			FlushInterval: 100 * time.Millisecond,
			ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
				log.Printf("proxy: upstream error [%s → %s]: %v", req.Host, targetURL, err)
				// Close connection on error to prevent reusing broken connections.
				w.Header().Set("Connection", "close")
				http.Error(w, "Bad Gateway", http.StatusBadGateway)
			},
		}
		proxies[normaliseHost(r.Host)] = rp
		targets[normaliseHost(r.Host)] = target

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

	return &routeTable{cfg: cfg, proxies: proxies, targets: targets, certs: certs}, nil
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
		Addr:      httpsAddr,
		Handler:   http.HandlerFunc(s.serveHTTPS),
		TLSConfig: tlsCfg,
		// Timeouts disabled to support WebSockets and large uploads/downloads.
		// Backend and client timeouts handle idle connection cleanup.
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 30 * time.Second,
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
		Addr:      httpsAddr,
		Handler:   http.HandlerFunc(s.serveHTTPS),
		TLSConfig: tlsCfg,
		// Timeouts disabled to support WebSockets and large uploads/downloads.
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 30 * time.Second,
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
// fallback page. WebSocket upgrade requests are handled via TCP tunnel.
func (s *Server) serveHTTPS(w http.ResponseWriter, r *http.Request) {
	t := s.currentTable()
	key := normaliseHost(r.Host)

	// Check for WebSocket upgrade request.
	if isWebSocketUpgrade(r) {
		if target, ok := t.targets[key]; ok {
			s.handleWebSocket(w, r, target)
			return
		}
		http.Error(w, "No backend for WebSocket", http.StatusBadGateway)
		return
	}

	if rp, ok := t.proxies[key]; ok {
		rp.ServeHTTP(w, r)
		return
	}
	// Fallback: show the route listing.
	serveFallback(w, r, t.cfg.Fallback.Title, t.cfg.Routes)
}

// isWebSocketUpgrade checks if the request is a WebSocket upgrade.
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// handleWebSocket proxies a WebSocket connection by hijacking the client conn
// and establishing a TCP tunnel to the backend.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request, target *url.URL) {
	// Determine backend address.
	backendHost := target.Host
	if target.Port() == "" {
		if target.Scheme == "https" {
			backendHost = target.Host + ":443"
		} else {
			backendHost = target.Host + ":80"
		}
	}

	// Dial the backend with context for cancellation.
	ctx := r.Context()
	backendConn, err := wsDialer.DialContext(ctx, "tcp", backendHost)
	if err != nil {
		log.Printf("proxy: WebSocket dial to %s failed: %v", backendHost, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	// Enable TCP keepalive on the backend connection.
	if tcpConn, ok := backendConn.(*net.TCPConn); ok {
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	// Hijack the client connection.
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		backendConn.Close()
		log.Printf("proxy: WebSocket hijack not supported")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		backendConn.Close()
		log.Printf("proxy: WebSocket hijack failed: %v", err)
		return
	}

	// Enable TCP keepalive on the client connection.
	if tcpConn, ok := clientConn.(*net.TCPConn); ok {
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	// Rewrite the request URL path to include the target path prefix if any.
	outReq := r.Clone(context.Background()) // fresh context, not tied to HTTP handler
	outReq.URL.Scheme = target.Scheme
	outReq.URL.Host = target.Host
	if target.Path != "" && target.Path != "/" {
		outReq.URL.Path = singleJoiningSlash(target.Path, r.URL.Path)
	}
	outReq.Host = target.Host
	outReq.RequestURI = outReq.URL.RequestURI()

	// Add X-Forwarded headers for the backend.
	if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		outReq.Header.Set("X-Forwarded-For", clientIP)
	}
	outReq.Header.Set("X-Forwarded-Proto", "https")

	// Forward the request to backend (including Upgrade headers).
	if err := outReq.Write(backendConn); err != nil {
		clientConn.Close()
		backendConn.Close()
		log.Printf("proxy: WebSocket forward request failed: %v", err)
		return
	}

	// Flush any buffered data from the client.
	if clientBuf.Reader.Buffered() > 0 {
		buffered := make([]byte, clientBuf.Reader.Buffered())
		_, _ = clientBuf.Read(buffered)
		_, _ = backendConn.Write(buffered)
	}

	// Bidirectional copy with proper shutdown.
	// When one direction ends, close both connections to unblock the other.
	var once sync.Once
	closeAll := func() {
		clientConn.Close()
		backendConn.Close()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(backendConn, clientConn)
		once.Do(closeAll)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, backendConn)
		once.Do(closeAll)
	}()
	wg.Wait()
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
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
