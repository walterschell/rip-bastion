package vpn

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestZeroTierProviderStatusUsesConfiguredNetwork(t *testing.T) {
	t.Parallel()

	const token = "secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-ZT1-Auth"); got != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/status":
			_, _ = fmt.Fprint(w, `{"address":"abcdef1234","online":true,"version":"1.14.2"}`)
		case "/network":
			_, _ = fmt.Fprint(w, `[
				{"id":"1111111111111111","name":"ignored","status":"OK","portDeviceName":"ztfoo","assignedAddresses":["10.0.0.10/24"]},
				{"id":"2222222222222222","name":"prod-mesh","status":"OK","portDeviceName":"ztbar","assignedAddresses":["10.147.20.190/24"]}
			]`)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	provider := NewZeroTierProvider(server.URL, token, "2222222222222222", time.Second)
	status, err := provider.Status()
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	if status.Name != "prod-mesh" {
		t.Fatalf("Name = %q, want %q", status.Name, "prod-mesh")
	}
	if !status.Connected {
		t.Fatal("Connected = false, want true")
	}
	if status.Interface != "ztbar" {
		t.Fatalf("Interface = %q, want %q", status.Interface, "ztbar")
	}
	if status.LocalCIDR != "10.147.20.190/24" {
		t.Fatalf("LocalCIDR = %q, want %q", status.LocalCIDR, "10.147.20.190/24")
	}
	if status.PeerIP != "abcdef1234" {
		t.Fatalf("PeerIP = %q, want %q", status.PeerIP, "abcdef1234")
	}
}

func TestZeroTierProviderStatusFallsBackDisconnected(t *testing.T) {
	t.Parallel()

	provider := NewZeroTierProvider("http://127.0.0.1:1", "", "", time.Second)
	status, err := provider.Status()
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	if status.Name != "ZeroTier" {
		t.Fatalf("Name = %q, want %q", status.Name, "ZeroTier")
	}
	if status.Connected {
		t.Fatal("Connected = true, want false")
	}
	if status.Interface != "" {
		t.Fatalf("Interface = %q, want empty", status.Interface)
	}
	if status.LocalCIDR != "" {
		t.Fatalf("LocalCIDR = %q, want empty", status.LocalCIDR)
	}
}
