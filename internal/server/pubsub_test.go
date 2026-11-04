// Integration tests for pub/sub: SUBSCRIBE/PSUBSCRIBE/SSUBSCRIBE, PUBLISH
// delivery, RESP2 context restrictions, RESP3 pushes and PUBSUB introspection.
// They use in-process sessions so pushes are read with a bounded timeout.
package server_test

import (
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/server"
	"github.com/rabbicse/distcache/internal/testutil"
)

// pushWait bounds every wait for a pushed frame.
const pushWait = 2 * time.Second

// sessionDo runs cmd on ss, records it for the coverage gate and returns the
// rendered reply.
func sessionDo(t *testing.T, ss *server.Session, cmd ...string) string {
	t.Helper()
	testutil.RecordCommands(t)(cmd[0])
	v, err := ss.Exec(cmd...)
	if err != nil {
		t.Fatalf("Exec(%v): %v", cmd, err)
	}
	return testutil.Render(v, false)
}

// sessionPush returns the next pushed frame on ss, rendered.
func sessionPush(t *testing.T, ss *server.Session) string {
	t.Helper()
	v, err := ss.ReadPush(pushWait)
	if err != nil {
		t.Fatalf("ReadPush: %v", err)
	}
	return testutil.Render(v, false)
}

// sessionNoPush fails if a frame arrives within a short window.
func sessionNoPush(t *testing.T, ss *server.Session) {
	t.Helper()
	if v, err := ss.ReadPush(30 * time.Millisecond); err == nil {
		t.Fatalf("unexpected frame %s", testutil.Render(v, false))
	}
}

// expectReply fails unless the rendered reply to cmd equals want.
func expectReply(t *testing.T, ss *server.Session, want string, cmd ...string) {
	t.Helper()
	if got := sessionDo(t, ss, cmd...); got != want {
		t.Fatalf("%v: got %s want %s", cmd, got, want)
	}
}

func newSession(t *testing.T) (*server.Server, func() *server.Session) {
	t.Helper()
	s, _ := testutil.StartServer(t, nil)
	return s, func() *server.Session {
		ss := s.NewSession()
		t.Cleanup(ss.Close)
		return ss
	}
}

func TestPubSub_SubscribeAndPublish(t *testing.T) {
	_, mk := newSession(t)
	sub, pub := mk(), mk()
	expectReply(t, sub, `*["subscribe", "a", :1]`, "SUBSCRIBE", "a")
	expectReply(t, pub, ":0", "PUBLISH", "nobody", "x")
	expectReply(t, pub, ":1", "PUBLISH", "a", "hello")
	if got, want := sessionPush(t, sub), `*["message", "a", "hello"]`; got != want {
		t.Fatalf("message: got %s want %s", got, want)
	}
	// Subscribing twice to the same channel still acks but keeps the count.
	expectReply(t, sub, `*["subscribe", "b", :2]`, "SUBSCRIBE", "b")
	expectReply(t, sub, `*["subscribe", "a", :2]`, "SUBSCRIBE", "a")
	expectReply(t, sub, `*["unsubscribe", "a", :1]`, "UNSUBSCRIBE", "a")
	expectReply(t, pub, ":0", "PUBLISH", "a", "again")
	sessionNoPush(t, sub)
	// UNSUBSCRIBE without arguments leaves every channel.
	expectReply(t, sub, `*["unsubscribe", "b", :0]`, "UNSUBSCRIBE")
	expectReply(t, sub, `*["unsubscribe", (nil), :0]`, "UNSUBSCRIBE")
	expectReply(t, pub, ":0", "PUBLISH", "b", "x")
}

func TestPubSub_Patterns(t *testing.T) {
	_, mk := newSession(t)
	sub, pub := mk(), mk()
	expectReply(t, sub, `*["psubscribe", "n*", :1]`, "PSUBSCRIBE", "n*")
	expectReply(t, sub, `*["subscribe", "news", :2]`, "SUBSCRIBE", "news")
	// A client matching both a channel and a pattern gets two messages.
	expectReply(t, pub, ":2", "PUBLISH", "news", "m")
	got := []string{sessionPush(t, sub), sessionPush(t, sub)}
	want := map[string]bool{
		`*["message", "news", "m"]`:        true,
		`*["pmessage", "n*", "news", "m"]`: true,
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected frame %s, want one of %v", g, want)
		}
		delete(want, g)
	}
	expectReply(t, pub, ":0", "PUBLISH", "other", "m")
	expectReply(t, sub, `*["punsubscribe", "n*", :1]`, "PUNSUBSCRIBE", "n*")
	expectReply(t, sub, `*["punsubscribe", (nil), :1]`, "PUNSUBSCRIBE")
}

func TestPubSub_RESP2ContextRestriction(t *testing.T) {
	_, mk := newSession(t)
	sub := mk()
	expectReply(t, sub, "+PONG", "PING") // not subscribed yet: normal PONG
	expectReply(t, sub, `*["subscribe", "a", :1]`, "SUBSCRIBE", "a")
	expectReply(t, sub, "-ERR Can't execute 'get': only (P|S)SUBSCRIBE / (P|S)UNSUBSCRIBE / PING / QUIT / RESET are allowed in this context", "GET", "k")
	expectReply(t, sub, `*["pong", ""]`, "PING")
	expectReply(t, sub, `*["unsubscribe", "a", :0]`, "UNSUBSCRIBE", "a")
	expectReply(t, sub, "(nil)", "GET", "k")
}

func TestPubSub_RESP3Pushes(t *testing.T) {
	_, mk := newSession(t)
	sub, pub := mk(), mk()
	sessionDo(t, sub, "HELLO", "3")
	expectReply(t, sub, `>["subscribe", "c", :1]`, "SUBSCRIBE", "c")
	expectReply(t, pub, ":1", "PUBLISH", "c", "v")
	if got, want := sessionPush(t, sub), `>["message", "c", "v"]`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	// RESP3 allows regular commands while subscribed.
	expectReply(t, sub, "(nil)", "GET", "k")
}

func TestPubSub_ShardChannels(t *testing.T) {
	_, mk := newSession(t)
	sub, pub := mk(), mk()
	expectReply(t, sub, `*["ssubscribe", "sh", :1]`, "SSUBSCRIBE", "sh")
	expectReply(t, pub, ":1", "SPUBLISH", "sh", "m")
	if got, want := sessionPush(t, sub), `*["smessage", "sh", "m"]`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	// A plain PUBLISH does not reach shard subscribers.
	expectReply(t, pub, ":0", "PUBLISH", "sh", "m")
	sessionNoPush(t, sub)
	expectReply(t, sub, `*["sunsubscribe", "sh", :0]`, "SUNSUBSCRIBE", "sh")
	expectReply(t, pub, ":0", "SPUBLISH", "sh", "m")
}

func TestPubSub_Introspection(t *testing.T) {
	_, mk := newSession(t)
	sub, adm := mk(), mk()
	expectReply(t, adm, "*[]", "PUBSUB", "CHANNELS")
	expectReply(t, adm, ":0", "PUBSUB", "NUMPAT")
	sessionDo(t, sub, "SUBSCRIBE", "alpha")
	sessionDo(t, sub, "SUBSCRIBE", "beta")
	sessionDo(t, mk(), "PSUBSCRIBE", "p*")
	sessionDo(t, sub, "UNSUBSCRIBE", "beta")
	expectReply(t, adm, `*["alpha"]`, "PUBSUB", "CHANNELS")
	expectReply(t, adm, `*["alpha"]`, "PUBSUB", "CHANNELS", "a*")
	expectReply(t, adm, `*[]`, "PUBSUB", "CHANNELS", "z*")
	expectReply(t, adm, `*["alpha", :1, "beta", :0]`, "PUBSUB", "NUMSUB", "alpha", "beta")
	expectReply(t, adm, `*[]`, "PUBSUB", "NUMSUB")
	expectReply(t, adm, ":1", "PUBSUB", "NUMPAT")
	shard := mk()
	sessionDo(t, shard, "SSUBSCRIBE", "s1")
	expectReply(t, adm, `*["s1"]`, "PUBSUB", "SHARDCHANNELS")
	expectReply(t, adm, `*["s1"]`, "PUBSUB", "SHARDCHANNELS", "s*")
	expectReply(t, adm, `*["s1", :1]`, "PUBSUB", "SHARDNUMSUB", "s1")
	expectReply(t, adm, "-ERR unknown subcommand 'FOO'. Try PUBSUB HELP.", "PUBSUB", "FOO")
	expectReply(t, adm, "-ERR wrong number of arguments for 'pubsub|numpat' command", "PUBSUB", "NUMPAT", "x")
	expectReply(t, adm, "-ERR wrong number of arguments for 'publish' command", "PUBLISH", "a")
}
