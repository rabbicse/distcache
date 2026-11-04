// Integration tests for keyspace notifications (notify-keyspace-events),
// observed through pattern subscribers on in-process sessions.
package server_test

import (
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/testutil"
)

func TestNotify_KeyeventAndKeyspace(t *testing.T) {
	_, mk := newSession(t)
	sub, w := mk(), mk()
	expectReply(t, w, "+OK", "CONFIG", "SET", "notify-keyspace-events", "KEA")
	expectReply(t, w, `*["notify-keyspace-events", "AKE"]`, "CONFIG", "GET", "notify-keyspace-events")
	expectReply(t, sub, `*["psubscribe", "__key*__:*", :1]`, "PSUBSCRIBE", "__key*__:*")
	expectReply(t, w, "+OK", "SET", "k", "v")
	got := []string{sessionPush(t, sub), sessionPush(t, sub)}
	want := map[string]bool{
		`*["pmessage", "__key*__:*", "__keyspace@0__:k", "set"]`: true,
		`*["pmessage", "__key*__:*", "__keyevent@0__:set", "k"]`: true,
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected notification %s, want one of %v", g, want)
		}
		delete(want, g)
	}
	// DEL emits "del"; a missing key emits nothing.
	expectReply(t, w, ":1", "DEL", "k")
	if g, wnt := sessionPush(t, sub), `*["pmessage", "__key*__:*", "__keyspace@0__:k", "del"]`; g != wnt {
		t.Fatalf("got %s want %s", g, wnt)
	}
	sessionPush(t, sub) // the matching keyevent frame
	expectReply(t, w, ":0", "DEL", "k")
	sessionNoPush(t, sub)
}

func TestNotify_KeyeventChannelOnly(t *testing.T) {
	_, mk := newSession(t)
	sub, w := mk(), mk()
	expectReply(t, w, "+OK", "CONFIG", "SET", "notify-keyspace-events", "KEA")
	expectReply(t, sub, `*["subscribe", "__keyevent@0__:set", :1]`, "SUBSCRIBE", "__keyevent@0__:set")
	expectReply(t, w, "+OK", "SET", "mykey", "v")
	if g, want := sessionPush(t, sub), `*["message", "__keyevent@0__:set", "mykey"]`; g != want {
		t.Fatalf("got %s want %s", g, want)
	}
	expectReply(t, w, ":1", "LPUSH", "l", "x")
	sessionNoPush(t, sub)
}

func TestNotify_DisabledAndClassFilter(t *testing.T) {
	_, mk := newSession(t)
	sub, w := mk(), mk()
	expectReply(t, sub, `*["psubscribe", "__key*__:*", :1]`, "PSUBSCRIBE", "__key*__:*")
	// Disabled by default.
	expectReply(t, w, "+OK", "SET", "k", "v")
	sessionNoPush(t, sub)
	// "Eg": keyevent channel, generic commands only.
	expectReply(t, w, "+OK", "CONFIG", "SET", "notify-keyspace-events", "Eg")
	expectReply(t, w, "+OK", "SET", "k", "v2")
	sessionNoPush(t, sub)
	expectReply(t, w, ":1", "DEL", "k")
	if g, want := sessionPush(t, sub), `*["pmessage", "__key*__:*", "__keyevent@0__:del", "k"]`; g != want {
		t.Fatalf("got %s want %s", g, want)
	}
	expectReply(t, w, "+OK", "CONFIG", "SET", "notify-keyspace-events", "")
	expectReply(t, w, "+OK", "SET", "k", "v3")
	sessionNoPush(t, sub)
}

func TestNotify_ExpiredWithFakeClock(t *testing.T) {
	clock := testutil.NewFakeClock(time.Unix(1_700_000_000, 0))
	s, _ := testutil.StartServerOpts(t, testutil.Options{Clock: clock.Now})
	sub, w := s.NewSession(), s.NewSession()
	t.Cleanup(sub.Close)
	t.Cleanup(w.Close)
	expectReply(t, w, "+OK", "CONFIG", "SET", "notify-keyspace-events", "Ex")
	expectReply(t, sub, `*["subscribe", "__keyevent@0__:expired", :1]`, "SUBSCRIBE", "__keyevent@0__:expired")
	expectReply(t, w, "+OK", "SET", "tmp", "v", "PX", "100")
	sessionNoPush(t, sub)
	clock.Advance(time.Second)
	// Whichever of lazy expiry on access or the active cycle fires first, the
	// key expires exactly once.
	expectReply(t, w, "(nil)", "GET", "tmp")
	if g, want := sessionPush(t, sub), `*["message", "__keyevent@0__:expired", "tmp"]`; g != want {
		t.Fatalf("got %s want %s", g, want)
	}
	sessionNoPush(t, sub)
}
