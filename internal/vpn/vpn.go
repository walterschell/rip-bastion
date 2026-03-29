package vpn

import "context"

// Status holds a point-in-time snapshot of VPN state.
type Status struct {
	Name      string // VPN provider name, e.g. "WireGuard"
	Connected bool
	Interface string // network interface name, e.g. "wg0"
	PeerIP    string // remote peer IP when connected
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

// StubProvider is a placeholder VPN provider that always reports disconnected.
// It is used until a real VPN backend is configured.
type StubProvider struct{}

func (s *StubProvider) Name() string { return "Stub (Not Configured)" }

func (s *StubProvider) Status() (*Status, error) {
	return &Status{Name: s.Name(), Connected: false}, nil
}

// Subscribe returns a channel that immediately delivers one disconnected
// Status, then blocks until ctx is cancelled at which point it is closed.
func (s *StubProvider) Subscribe(ctx context.Context) <-chan *Status {
	ch := make(chan *Status, 1)
	ch <- &Status{Name: s.Name(), Connected: false}
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}

// DefaultProvider returns the configured VPN provider.
// Returns a StubProvider until a real VPN is configured.
func DefaultProvider() Provider {
	return &StubProvider{}
}
