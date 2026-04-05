package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// selfSignedCert generates an ECDSA P-256 self-signed certificate valid for
// the given hostname (and the bare hostname without a .local suffix as a SAN).
// The certificate is valid for 10 years from the time of generation.
func selfSignedCert(hostname string) (*tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generating serial: %w", err)
	}

	now := time.Now()
	sans := buildSANs(hostname)

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   hostname,
			Organization: []string{"rip-bastion"},
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              sans,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("creating certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshaling key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("assembling key pair: %w", err)
	}
	return &cert, nil
}

// loadOrCreateSelfSignedCert returns a stable, per-host self-signed
// certificate from certDir. If the certificate does not yet exist, one is
// generated and persisted to disk.
func loadOrCreateSelfSignedCert(hostname, certDir string) (*tls.Certificate, error) {
	if certDir == "" {
		certDir = DefaultSelfSignedCertDir
	}
	if err := os.MkdirAll(certDir, 0700); err != nil {
		return nil, fmt.Errorf("creating cert dir %q: %w", certDir, err)
	}
	base := certFileBase(hostname)
	certPath := filepath.Join(certDir, base+".crt")
	keyPath := filepath.Join(certDir, base+".key")

	if _, certErr := os.Stat(certPath); certErr == nil {
		if _, keyErr := os.Stat(keyPath); keyErr == nil {
			return loadProvidedCert(certPath, keyPath)
		}
	}

	cert, err := selfSignedCert(hostname)
	if err != nil {
		return nil, err
	}

	if len(cert.Certificate) == 0 || cert.PrivateKey == nil {
		return nil, fmt.Errorf("generated certificate for %q is incomplete", hostname)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyDER, err := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("encoding private key for %q: %w", hostname, err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return nil, fmt.Errorf("writing cert %q: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return nil, fmt.Errorf("writing key %q: %w", keyPath, err)
	}

	return loadProvidedCert(certPath, keyPath)
}

// buildSANs returns the DNS SANs to include in a certificate for hostname.
// If the hostname looks like "foo.local" it also adds "foo" as a plain SAN.
// IP addresses are silently skipped (IPs are not expected here).
func buildSANs(hostname string) []string {
	// Strip trailing dot
	hostname = strings.TrimSuffix(hostname, ".")
	if net.ParseIP(hostname) != nil {
		return nil
	}
	sans := []string{hostname}
	// Also add the label without the .local suffix for convenience.
	if after, ok := strings.CutSuffix(hostname, ".local"); ok {
		sans = append(sans, after)
	}
	return sans
}

// loadProvidedCert loads a certificate and key from PEM files on disk.
func loadProvidedCert(certFile, keyFile string) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading cert pair cert=%q key=%q: %w", certFile, keyFile, err)
	}
	return &cert, nil
}

func certFileBase(hostname string) string {
	normalized := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(hostname, ".")))
	if normalized == "" {
		return "default"
	}
	b := strings.Builder{}
	b.Grow(len(normalized))
	for _, ch := range normalized {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '-' {
			b.WriteRune(ch)
			continue
		}
		b.WriteRune('_')
	}
	out := b.String()
	if out == "" {
		return "default"
	}
	return out
}
