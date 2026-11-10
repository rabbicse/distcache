// This file resolves the console's TLS mode (spec section 5): it decides
// between plain HTTP and HTTPS, loads operator-provided certificates, and
// creates, persists and renews a self-signed certificate otherwise.
package web

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/rabbicse/distcache/internal/config"
)

const (
	certFile = "web-cert.pem"
	keyFile  = "web-key.pem"

	certValidity = 365 * 24 * time.Hour
	// certRenewBefore regenerates the certificate this long before expiry.
	certRenewBefore = 30 * 24 * time.Hour
	// insecureWarnEvery is the interval of the insecure-mode warning.
	insecureWarnEvery = 10 * time.Minute
)

// isLoopbackBind reports whether a bind host only accepts local connections.
// Wildcard and empty hosts are not loopback.
func isLoopbackBind(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// insecureEffective reports whether the console runs in insecure mode: the
// operator asked for it and it matters (plain HTTP beyond loopback).
func insecureEffective(cfg *config.Config) bool {
	return cfg.WebInsecure && cfg.WebTLS == "off" && !isLoopbackBind(cfg.WebBind)
}

// resolveTLS returns the listener TLS configuration, or nil for plain HTTP.
// dir is the data directory holding the generated certificate. It refuses
// plain HTTP beyond loopback unless a trusted proxy or web-insecure is set.
func resolveTLS(cfg *config.Config, dir string, log *slog.Logger) (*tls.Config, error) {
	if (cfg.WebTLSCert == "") != (cfg.WebTLSKey == "") {
		return nil, errors.New("web-tls-cert and web-tls-key must be set together")
	}
	loopback := isLoopbackBind(cfg.WebBind)
	switch cfg.WebTLS {
	case "off":
		if !loopback && len(cfg.WebTrustedProxy) == 0 && !cfg.WebInsecure {
			return nil, errors.New("web-tls off on a non-loopback bind needs web-trusted-proxy or web-insecure yes")
		}
		if cfg.WebInsecure && !loopback {
			logInsecure(log)
		}
		return nil, nil
	case "on":
	case "auto", "":
		if loopback {
			return nil, nil
		}
	default:
		return nil, fmt.Errorf("invalid web-tls %q", cfg.WebTLS)
	}

	cert, err := certificateFor(cfg, dir, log)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

// certificateFor loads the provided certificate or the persisted self-signed one.
func certificateFor(cfg *config.Config, dir string, log *slog.Logger) (tls.Certificate, error) {
	if cfg.WebTLSCert != "" {
		cert, err := tls.LoadX509KeyPair(cfg.WebTLSCert, cfg.WebTLSKey)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("load web-tls-cert: %w", err)
		}
		return cert, nil
	}
	cert, err := loadOrCreateSelfSigned(dir, selfSignedHosts(cfg), time.Now())
	if err != nil {
		return tls.Certificate{}, err
	}
	log.Info("web tls self-signed", "fingerprint", fingerprint(cert.Certificate[0]))
	return cert, nil
}

// selfSignedHosts lists the extra SANs: configured hosts, the bind address
// (unless a wildcard) and the machine hostname.
func selfSignedHosts(cfg *config.Config) []string {
	hosts := slices.Clone(cfg.WebTLSHosts)
	if ip := net.ParseIP(cfg.WebBind); ip != nil && !ip.IsUnspecified() {
		hosts = append(hosts, ip.String())
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		hosts = append(hosts, h)
	}
	return hosts
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// splitSANs returns the sorted, de-duplicated DNS names (always including
// localhost) and IP addresses for the given hosts.
func splitSANs(hosts []string) (dns []string, ips []net.IP) {
	dns = []string{"localhost"}
	for _, h := range hosts {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, h)
		}
	}
	slices.Sort(dns)
	dns = slices.Compact(dns)
	return dns, ips
}

// loadOrCreateSelfSigned returns the persisted self-signed certificate from
// dir, creating it when missing, expiring within certRenewBefore of now, or no
// longer covering hosts.
func loadOrCreateSelfSigned(dir string, hosts []string, now time.Time) (tls.Certificate, error) {
	certPath, keyPath := filepath.Join(dir, certFile), filepath.Join(dir, keyFile)
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if leaf, perr := x509.ParseCertificate(cert.Certificate[0]); perr == nil && certUsable(leaf, hosts, now) {
			return cert, nil
		}
	}
	return createSelfSigned(certPath, keyPath, hosts, now)
}

// certUsable reports whether leaf is far enough from expiry and covers hosts.
func certUsable(leaf *x509.Certificate, hosts []string, now time.Time) bool {
	if now.Add(certRenewBefore).After(leaf.NotAfter) || now.Before(leaf.NotBefore) {
		return false
	}
	dns, ips := splitSANs(hosts)
	for _, n := range dns {
		if leaf.VerifyHostname(n) != nil {
			return false
		}
	}
	for _, ip := range ips {
		if leaf.VerifyHostname(ip.String()) != nil {
			return false
		}
	}
	return true
}

// createSelfSigned generates an ECDSA P-256 certificate and writes both PEM
// files atomically with mode 0600. The key is stored as PKCS#8 ("PRIVATE KEY")
// because it is algorithm-agnostic and what tls.LoadX509KeyPair prefers.
func createSelfSigned(certPath, keyPath string, hosts []string, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("serial: %w", err)
	}
	dns, ips := splitSANs(hosts)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "distcache web console"},
		// Backdate slightly to tolerate small clock skew with clients.
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(-time.Minute).Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dns,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	// Key first: a crash between the writes leaves an unmatched pair, which
	// fails to load and is regenerated on the next start.
	if err := writeFileAtomic(keyPath, keyPEM); err != nil {
		return tls.Certificate{}, err
	}
	if err := writeFileAtomic(certPath, certPEM); err != nil {
		return tls.Certificate{}, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load generated pair: %w", err)
	}
	return cert, nil
}

// writeFileAtomic writes data to a 0600 temp file in the target directory and
// renames it into place.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmp := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp) // best-effort cleanup of the partial file
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fail(err)
	}
	if _, err := bytes.NewReader(data).WriteTo(f); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// logInsecure emits the insecure-mode warning.
func logInsecure(log *slog.Logger) {
	log.Warn("web console is serving plain HTTP beyond loopback (web-insecure yes): traffic and sessions are unprotected")
}

// warnInsecure repeats the warning on every tick until done is closed. The
// startup warning is logged by resolveTLS.
func warnInsecure(log *slog.Logger, ticks <-chan time.Time, done <-chan struct{}) {
	for {
		select {
		case <-ticks:
			logInsecure(log)
		case <-done:
			return
		}
	}
}
