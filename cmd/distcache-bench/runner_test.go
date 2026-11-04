package main

import (
	"strconv"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

func TestRun_AgainstInProcessServer(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	res, err := Run(Config{Addr: addr, Clients: 4, Requests: 2000, Pipeline: 8, Tests: []string{"set", "get"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2", len(res))
	}
	for _, r := range res {
		if r.Requests != 2000 || r.OpsPerSec <= 0 {
			t.Errorf("%s: got requests=%d ops=%v, want 2000 and >0", r.Test, r.Requests, r.OpsPerSec)
		}
		if r.P50 <= 0 || r.P99 < r.P50 {
			t.Errorf("%s: bad percentiles p50=%v p99=%v", r.Test, r.P50, r.P99)
		}
	}
}

func TestRun_KeySpaceBoundsKeys(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	if _, err := Run(Config{Addr: addr, Clients: 2, Requests: 5000, Pipeline: 4, Tests: []string{"set"}, KeySpace: 1000}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := testutil.MustDial(t, addr)
	v, err := c.Do("DBSIZE")
	if err != nil {
		t.Fatal(err)
	}
	if v.Int < 1 || v.Int > 1000 {
		t.Errorf("DBSIZE: got %d, want 1..1000", v.Int)
	}
	k := c.DoS("EXISTS", "key:"+strconv.Itoa(1000))
	if k != ":0" {
		t.Errorf("EXISTS key:1000: got %s, want :0", k)
	}
}

func TestRun_UnknownTest(t *testing.T) {
	if _, err := Run(Config{Addr: "127.0.0.1:1", Tests: []string{"nope"}}); err == nil {
		t.Error("got nil error, want unknown test error")
	}
}
