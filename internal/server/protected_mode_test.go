// Tests protected mode: without a password only local peers may connect.
package server

import (
	"net"
	"testing"

	"github.com/rabbicse/distcache/internal/config"
)

func TestProtectedModeDenies(t *testing.T) {
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 5000}
	cases := []struct {
		name      string
		addr      net.Addr
		protected bool
		pass      string
		want      bool
	}{
		{"ipv4 loopback allowed", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}, true, "", false},
		{"ipv6 loopback allowed", &net.TCPAddr{IP: net.ParseIP("::1"), Port: 1}, true, "", false},
		{"unix socket allowed", &net.UnixAddr{Name: "/tmp/x.sock", Net: "unix"}, true, "", false},
		{"remote denied", remote, true, "", true},
		{"remote allowed with requirepass", remote, true, "secret", false},
		{"remote allowed with protected-mode no", remote, false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.ProtectedMode.Store(tc.protected)
			if err := cfg.Set("requirepass", tc.pass, true); err != nil {
				t.Fatal(err)
			}
			if got := protectedModeDenies(cfg, tc.addr); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
