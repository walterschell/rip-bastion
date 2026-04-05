package vpn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Status holds a point-in-time snapshot of VPN state.
type Status struct {
	Name      string // VPN provider or network name, e.g. "WireGuard" or "ZeroTier"
	Connected bool
	Interface string // network interface name, e.g. "wg0"
	LocalCIDR string // local interface address in CIDR notation, e.g. "10.0.0.2/24"
	PeerIP    string // remote peer IP or node identifier when connected
}

// Provider is the interface that VPN implementations must satisfy.
//
// Status returns the current state synchronously (useful for an initial read).
// Subscribe returns a channel on which the provider pushes Status updates
// whenever the VPN state changes; the channel is closed when ctx is done.
// Callers should prefer Subscribe for reactive updates and use Status only
// when a one-shot synchronous read is needed.
type Provider interface {
	Name() string
	Status() (*Status, error)
	Subscribe(ctx context.Context) <-chan *Status
}

const (
	defaultZeroTierAPIBaseURL   = "http://127.0.0.1:9993"
	defaultZeroTierTokenFile    = "/var/lib/zerotier-one/authtoken.secret"
	defaultZeroTierPollInterval = 10 * time.Second
)

// ZeroTierProvider reports VPN status from the local ZeroTier One service API.
// It never shells out to zerotier-cli.
type ZeroTierProvider struct {
	client       *zeroTierClient
	networkID    string
	pollInterval time.Duration
}

type zeroTierClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type zeroTierServiceStatus struct {
	Address string `json:"address"`
	Online  bool   `json:"online"`
	Version string `json:"version"`
}

type zeroTierNetwork struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Status            string   `json:"status"`
	PortDeviceName    string   `json:"portDeviceName"`
	AssignedAddresses []string `json:"assignedAddresses"`
}

// NewZeroTierProvider creates a ZeroTier-backed Provider using the supplied
// local service API base URL, auth token, optional network ID, and poll interval.
func NewZeroTierProvider(apiBaseURL, token, networkID string, pollInterval time.Duration) *ZeroTierProvider {
	if strings.TrimSpace(apiBaseURL) == "" {
		apiBaseURL = defaultZeroTierAPIBaseURL
	}
	if pollInterval <= 0 {
		pollInterval = defaultZeroTierPollInterval
	}

	return &ZeroTierProvider{
		client: &zeroTierClient{
			baseURL: strings.TrimRight(strings.TrimSpace(apiBaseURL), "/"),
			token:   strings.TrimSpace(token),
			httpClient: &http.Client{
				Timeout: 3 * time.Second,
			},
		},
		networkID:    strings.TrimSpace(networkID),
		pollInterval: pollInterval,
	}
}

// NewZeroTierProviderFromEnv constructs a ZeroTier provider from environment variables.
//
// Supported environment variables:
//   - RIP_ZEROTIER_API_BASE_URL
//   - RIP_ZEROTIER_AUTH_TOKEN
//   - RIP_ZEROTIER_AUTH_TOKEN_FILE
//   - RIP_ZEROTIER_NETWORK_ID
//   - RIP_ZEROTIER_POLL_INTERVAL
func NewZeroTierProviderFromEnv() Provider {
	apiBaseURL := envOrDefault("RIP_ZEROTIER_API_BASE_URL", defaultZeroTierAPIBaseURL)
	token := strings.TrimSpace(os.Getenv("RIP_ZEROTIER_AUTH_TOKEN"))
	if token == "" {
		tokenFile := envOrDefault("RIP_ZEROTIER_AUTH_TOKEN_FILE", defaultZeroTierTokenFile)
		if b, err := os.ReadFile(tokenFile); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}
	return NewZeroTierProvider(
		apiBaseURL,
		token,
		os.Getenv("RIP_ZEROTIER_NETWORK_ID"),
		parseDurationEnv("RIP_ZEROTIER_POLL_INTERVAL", defaultZeroTierPollInterval),
	)
}

func (p *ZeroTierProvider) Name() string { return "ZeroTier" }

func (p *ZeroTierProvider) Status() (*Status, error) {
	return p.snapshot(context.Background()), nil
}

// Subscribe polls the local ZeroTier API and emits updates whenever the
// observable VPN status changes.
func (p *ZeroTierProvider) Subscribe(ctx context.Context) <-chan *Status {
	ch := make(chan *Status, 1)

	go func() {
		defer close(ch)

		current := p.snapshot(ctx)
		last := cloneStatus(current)
		if !sendStatus(ctx, ch, current) {
			return
		}

		ticker := time.NewTicker(p.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current = p.snapshot(ctx)
				if statusEqual(last, current) {
					continue
				}
				last = cloneStatus(current)
				if !sendStatus(ctx, ch, current) {
					return
				}
			}
		}
	}()

	return ch
}

func (p *ZeroTierProvider) snapshot(ctx context.Context) *Status {
	status := &Status{Name: p.Name(), Connected: false}

	service, err := p.client.serviceStatus(ctx)
	if err != nil {
		return status
	}
	if service.Address != "" {
		status.PeerIP = service.Address
	}

	networks, err := p.client.networks(ctx)
	if err != nil {
		return status
	}

	network := selectZeroTierNetwork(networks, p.networkID)
	if network == nil {
		return status
	}

	if strings.TrimSpace(network.Name) != "" {
		status.Name = strings.TrimSpace(network.Name)
	}
	status.Interface = strings.TrimSpace(network.PortDeviceName)
	status.LocalCIDR = firstNonEmpty(network.AssignedAddresses)
	status.Connected = service.Online && strings.EqualFold(strings.TrimSpace(network.Status), "OK")

	return status
}

func (c *zeroTierClient) serviceStatus(ctx context.Context) (*zeroTierServiceStatus, error) {
	var out zeroTierServiceStatus
	if err := c.getJSON(ctx, "/status", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *zeroTierClient) networks(ctx context.Context) ([]zeroTierNetwork, error) {
	var out []zeroTierNetwork
	if err := c.getJSON(ctx, "/network", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *zeroTierClient) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("X-ZT1-Auth", c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("zerotier api %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	return nil
}

func selectZeroTierNetwork(networks []zeroTierNetwork, networkID string) *zeroTierNetwork {
	if id := strings.TrimSpace(networkID); id != "" {
		for i := range networks {
			if strings.EqualFold(strings.TrimSpace(networks[i].ID), id) {
				return &networks[i]
			}
		}
		return nil
	}

	for i := range networks {
		if strings.EqualFold(strings.TrimSpace(networks[i].Status), "OK") {
			return &networks[i]
		}
	}
	for i := range networks {
		if strings.TrimSpace(networks[i].PortDeviceName) != "" || len(networks[i].AssignedAddresses) > 0 {
			return &networks[i]
		}
	}
	if len(networks) == 0 {
		return nil
	}
	return &networks[0]
}

func firstNonEmpty(values []string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func cloneStatus(s *Status) *Status {
	if s == nil {
		return nil
	}
	copy := *s
	return &copy
}

func statusEqual(a, b *Status) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Name == b.Name &&
		a.Connected == b.Connected &&
		a.Interface == b.Interface &&
		a.LocalCIDR == b.LocalCIDR &&
		a.PeerIP == b.PeerIP
}

func sendStatus(ctx context.Context, ch chan<- *Status, status *Status) bool {
	select {
	case ch <- cloneStatus(status):
		return true
	case <-ctx.Done():
		return false
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func parseDurationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// DefaultProvider returns the configured VPN provider.
// It defaults to the local ZeroTier service API.
func DefaultProvider() Provider {
	return NewZeroTierProviderFromEnv()
}
