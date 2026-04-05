package proxy

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigPath        = "/etc/rip-bastion/proxy.yaml"
	DefaultSelfSignedCertDir = "/var/lib/rip-bastion/proxy-certs"
)

const placeholderConfigYAML = `# rip-bastion proxy configuration
#
# This file is hot-reloaded when changed.
#
# - HTTP listens on :80 and redirects to HTTPS
# - HTTPS listens on :443 and routes by Host/SNI

listen:
  http: ":80"
  https: ":443"

tls:
  # "self-signed" (recommended default):
  #   Creates per-host certificates and stores them in cert_dir.
  #   Certificates are reused on restart and survive reboots.
  # "provided":
  #   Uses cert_file/key_file for all routes.
  mode: self-signed
  cert_dir: /var/lib/rip-bastion/proxy-certs
  # cert_file: /etc/rip-bastion/tls/server.crt
  # key_file: /etc/rip-bastion/tls/server.key

routes:
  - name: Home Assistant
    host: homeassistant.local
    target: http://192.168.1.100:8123
    mdns: true

  - name: Grafana
    host: grafana.local
    target: http://192.168.1.101:3000
    mdns: true

fallback:
  title: "rip-bastion proxy"
`

// Config is the top-level proxy configuration.
type Config struct {
	Listen   ListenConfig   `yaml:"listen"`
	TLS      TLSConfig      `yaml:"tls"`
	Routes   []Route        `yaml:"routes"`
	Fallback FallbackConfig `yaml:"fallback"`
}

// ListenConfig defines which addresses the proxy listens on.
type ListenConfig struct {
	HTTP  string `yaml:"http"`
	HTTPS string `yaml:"https"`
}

// TLSConfig controls how TLS certificates are sourced.
type TLSConfig struct {
	// Mode is either "self-signed" (auto-generate per-host certs) or "provided"
	// (use CertFile/KeyFile for all hosts).
	Mode string `yaml:"mode"`
	// CertDir is where self-signed per-host certificates are stored.
	CertDir  string `yaml:"cert_dir"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// Route defines a single reverse-proxy entry.
type Route struct {
	// Name is a human-readable label used in the fallback UI.
	Name string `yaml:"name"`
	// Host is the virtual hostname browsers use, e.g. "site1.local".
	Host string `yaml:"host"`
	// Target is the backend URL, e.g. "http://192.168.1.100:8001".
	Target string `yaml:"target"`
	// MDNS, when true, registers an mDNS A-record for Host via avahi.
	MDNS bool `yaml:"mdns"`
}

// FallbackConfig controls the catch-all page shown when no route matches.
type FallbackConfig struct {
	Title string `yaml:"title"`
}

// DefaultConfig returns a Config pre-filled with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Listen: ListenConfig{
			HTTP:  ":80",
			HTTPS: ":443",
		},
		TLS: TLSConfig{
			Mode:    "self-signed",
			CertDir: DefaultSelfSignedCertDir,
		},
		Fallback: FallbackConfig{
			Title: "rip-bastion proxy",
		},
	}
}

// LoadConfig reads and parses a YAML config file, falling back to defaults for
// any fields not present in the file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}
	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %q: %w", path, err)
	}
	return &cfg, nil
}

// PlaceholderConfigYAML returns the documented placeholder config used by
// --write-proxy-config.
func PlaceholderConfigYAML() string {
	return placeholderConfigYAML
}

// WritePlaceholderConfig writes a documented proxy configuration to path.
// If overwrite is false and the file exists, the existing file is left intact.
func WritePlaceholderConfig(path string, overwrite bool) (bool, error) {
	if path == "" {
		path = DefaultConfigPath
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, fmt.Errorf("creating config directory for %q: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(placeholderConfigYAML), 0644); err != nil {
		return false, fmt.Errorf("writing placeholder config %q: %w", path, err)
	}
	return true, nil
}

// ConfiguredSiteLabels loads the YAML config and returns labels suitable for
// display in the Services section.
func ConfiguredSiteLabels(path string) ([]string, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(cfg.Routes))
	for _, route := range cfg.Routes {
		if !route.MDNS || route.Host == "" {
			continue
		}
		labels = append(labels, route.Host)
	}
	return labels, nil
}
