package vpn

// Status holds VPN state.
type Status struct {
	Name      string // VPN provider name, e.g. "WireGuard"
	Connected bool
	Interface string // network interface name, e.g. "wg0"
	PeerIP    string // remote peer IP if connected
}

// Provider is the interface for VPN implementations.
type Provider interface {
	Name() string
	Status() (*Status, error)
}

// StubProvider is a placeholder VPN provider that always returns disconnected.
type StubProvider struct{}

func (s *StubProvider) Name() string { return "Stub (Not Configured)" }

func (s *StubProvider) Status() (*Status, error) {
	return &Status{Name: s.Name(), Connected: false}, nil
}

// DefaultProvider returns the configured VPN provider.
// Returns a StubProvider until a real VPN is configured.
func DefaultProvider() Provider {
	return &StubProvider{}
}
