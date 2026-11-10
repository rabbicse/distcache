package web

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
)

// logBuf is a goroutine-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newBufLogger() (*slog.Logger, *logBuf) {
	lb := &logBuf{}
	return slog.New(slog.NewTextHandler(lb, nil)), lb
}

func TestTLS_ModeTable(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		bind     string
		proxy    []string
		insecure bool
		wantTLS  bool
		wantErr  bool
		wantWarn bool
	}{
		{name: "auto loopback v4", mode: "auto", bind: "127.0.0.1"},
		{name: "auto loopback v6", mode: "auto", bind: "::1"},
		{name: "auto non-loopback", mode: "auto", bind: "0.0.0.0", wantTLS: true},
		{name: "on loopback v4", mode: "on", bind: "127.0.0.1", wantTLS: true},
		{name: "on loopback v6", mode: "on", bind: "::1", wantTLS: true},
		{name: "on non-loopback", mode: "on", bind: "0.0.0.0", wantTLS: true},
		{name: "off loopback v4", mode: "off", bind: "127.0.0.1"},
		{name: "off loopback v6", mode: "off", bind: "::1"},
		{name: "off non-loopback refused", mode: "off", bind: "0.0.0.0", wantErr: true},
		{name: "off non-loopback proxy", mode: "off", bind: "0.0.0.0", proxy: []string{"10.0.0.0/8"}},
		{name: "off non-loopback insecure", mode: "off", bind: "0.0.0.0", insecure: true, wantWarn: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.WebTLS, cfg.WebBind, cfg.WebTrustedProxy, cfg.WebInsecure = tc.mode, tc.bind, tc.proxy, tc.insecure
			log, lb := newBufLogger()
			got, err := resolveTLS(cfg, t.TempDir(), log)
			if (err != nil) != tc.wantErr {
				t.Fatalf("got err %v, want error=%v", err, tc.wantErr)
			}
			if (got != nil) != tc.wantTLS {
				t.Fatalf("got tls config %v, want tls=%v", got != nil, tc.wantTLS)
			}
			if warned := strings.Contains(lb.String(), "insecure"); warned != tc.wantWarn {
				t.Fatalf("got warning=%v, want %v; log: %s", warned, tc.wantWarn, lb.String())
			}
		})
	}
}

func TestTLS_CertWithoutKeyRejected(t *testing.T) {
	for _, tc := range []struct{ cert, key string }{{"c.pem", ""}, {"", "k.pem"}} {
		cfg := config.Default()
		cfg.WebTLSCert, cfg.WebTLSKey = tc.cert, tc.key
		if _, err := resolveTLS(cfg, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
			t.Errorf("cert=%q key=%q: got nil error, want error", tc.cert, tc.key)
		}
	}
}

func TestTLS_SelfSignedPersistedAndReused(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	c1, err := loadOrCreateSelfSigned(dir, []string{"example.test", "10.1.2.3"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web-cert.pem", "web-key.pem"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s: got mode %o, want 600", name, st.Mode().Perm())
		}
	}
	leaf, err := x509.ParseCertificate(c1.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(leaf.DNSNames, "localhost") || !slices.Contains(leaf.DNSNames, "example.test") {
		t.Errorf("got DNS SANs %v, want localhost and example.test", leaf.DNSNames)
	}
	var hasIP bool
	for _, ip := range leaf.IPAddresses {
		hasIP = hasIP || ip.String() == "10.1.2.3"
	}
	if !hasIP {
		t.Errorf("got IP SANs %v, want 10.1.2.3", leaf.IPAddresses)
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Errorf("got key %T, want ECDSA P-256", leaf.PublicKey)
	}
	if d := leaf.NotAfter.Sub(leaf.NotBefore); d < 365*24*time.Hour || d > 366*24*time.Hour {
		t.Errorf("got validity %v, want 365 days", d)
	}

	c2, err := loadOrCreateSelfSigned(dir, []string{"example.test", "10.1.2.3"}, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c1.Certificate[0], c2.Certificate[0]) {
		t.Error("second call regenerated the certificate, want reuse")
	}

	c3, err := loadOrCreateSelfSigned(dir, []string{"example.test"}, now.Add(340*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(c1.Certificate[0], c3.Certificate[0]) {
		t.Error("certificate within 30 days of expiry was reused, want regenerated")
	}
}

func TestTLS_SelfSignedRegeneratedWhenHostsChange(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	c1, err := loadOrCreateSelfSigned(dir, []string{"a.test"}, now)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := loadOrCreateSelfSigned(dir, []string{"b.test"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(c1.Certificate[0], c2.Certificate[0]) {
		t.Error("certificate reused after SAN set changed, want regenerated")
	}
}

func TestTLS_ProvidedCertUsed(t *testing.T) {
	src := t.TempDir()
	c, err := loadOrCreateSelfSigned(src, []string{"provided.test"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WebTLS, cfg.WebBind = "on", "127.0.0.1"
	cfg.WebTLSCert, cfg.WebTLSKey = filepath.Join(src, "web-cert.pem"), filepath.Join(src, "web-key.pem")
	dir := t.TempDir()
	got, err := resolveTLS(cfg, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Certificates) != 1 || !bytes.Equal(got.Certificates[0].Certificate[0], c.Certificate[0]) {
		t.Error("provided certificate not used")
	}
	if _, err := os.Stat(filepath.Join(dir, "web-cert.pem")); err == nil {
		t.Error("self-signed cert generated despite provided cert")
	}
}

func TestTLS_ProvidedCertUnreadable(t *testing.T) {
	cfg := config.Default()
	cfg.WebTLS = "on"
	cfg.WebTLSCert, cfg.WebTLSKey = "/nonexistent/c.pem", "/nonexistent/k.pem"
	if _, err := resolveTLS(cfg, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("got nil error, want error")
	}
}

func TestTLS_MinVersion12(t *testing.T) {
	cfg := config.Default()
	cfg.WebTLS = "on"
	got, err := resolveTLS(cfg, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got.MinVersion != tls.VersionTLS12 {
		t.Errorf("got MinVersion %x, want %x", got.MinVersion, tls.VersionTLS12)
	}
}

func TestTLS_GeneratedKeyIsPKCS8(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadOrCreateSelfSigned(dir, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "web-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(raw)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		t.Fatalf("got PEM %v, want PRIVATE KEY block", blk)
	}
}

func TestTLS_InsecureWarnsPeriodically(t *testing.T) {
	log, lb := newBufLogger()
	cfg := config.Default()
	cfg.WebBind, cfg.WebPort, cfg.WebTLS, cfg.WebInsecure = "0.0.0.0", 0, "off", true
	cfg.Dir = t.TempDir()
	c, err := New(Options{Config: cfg, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time)
	var gotInterval time.Duration
	c.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
		gotInterval = d
		return ticks, func() {}
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if gotInterval != 10*time.Minute {
		t.Errorf("got interval %v, want 10m", gotInterval)
	}
	count := func() int { return strings.Count(lb.String(), "insecure") }
	waitCount := func(want int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for count() < want && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if got := count(); got != want {
			t.Fatalf("got %d warnings, want %d", got, want)
		}
	}
	waitCount(1)
	ticks <- time.Now()
	waitCount(2)

	rec := get(t, c.Handler(), "/login")
	if !strings.Contains(rec.Body.String(), `class="insecure-banner"`) {
		t.Error("insecure banner not rendered")
	}
	if strings.Contains(get(t, newTestConsole(t).Handler(), "/login").Body.String(), "insecure-banner") {
		t.Error("banner rendered on a secure console")
	}
}

func TestTLS_SerialIs128BitCapable(t *testing.T) {
	// A 128-bit random serial exceeds 127 bits about half the time; 40 draws
	// make a false failure vanishingly unlikely.
	for range 40 {
		dir := t.TempDir()
		c, err := loadOrCreateSelfSigned(dir, nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		leaf, _ := x509.ParseCertificate(c.Certificate[0])
		if leaf.SerialNumber.BitLen() == 128 {
			return
		}
	}
	t.Error("no serial reached 128 bits in 40 draws")
}

func TestTLS_ReuseTightensKeyMode(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if _, err := loadOrCreateSelfSigned(dir, nil, now); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "web-key.pem")
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	log, lb := newBufLogger()
	if _, err := loadOrCreateSelfSignedLog(dir, nil, now, log); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(keyPath)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("got mode %o, want 600", st.Mode().Perm())
	}
	if !strings.Contains(lb.String(), "loose permissions") {
		t.Errorf("missing warning; log: %s", lb.String())
	}
}

func TestTLS_CorruptPairRegeneratedWithLog(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if _, err := loadOrCreateSelfSigned(dir, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web-cert.pem"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	log, lb := newBufLogger()
	if _, err := loadOrCreateSelfSignedLog(dir, nil, now, log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lb.String(), "regenerating") || !strings.Contains(lb.String(), "level=INFO") {
		t.Errorf("missing info log; log: %s", lb.String())
	}
	if _, err := tls.LoadX509KeyPair(filepath.Join(dir, "web-cert.pem"), filepath.Join(dir, "web-key.pem")); err != nil {
		t.Errorf("regenerated pair does not load: %v", err)
	}
}

func TestTLS_CreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if _, err := loadOrCreateSelfSigned(dir, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestTLS_DirErrorNamesPath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadOrCreateSelfSigned(filepath.Join(file, "sub"), nil, time.Now())
	if err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("got err %v, want one naming %s", err, file)
	}
}
