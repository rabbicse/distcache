package server_test

import (
	"bytes"
	"sync"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

// TestPubSub_PushesDoNotInterleaveWithReplies has a RESP3 subscriber pipeline
// large GETs while a publisher floods it with large messages. Every frame must
// parse (no interleaved bytes), the subscribe ack must come first, and every
// push and reply must arrive before the deadline.
func TestPubSub_PushesDoNotInterleaveWithReplies(t *testing.T) {
	const (
		nPush = 50
		nGet  = 10
		size  = 100 * 1024
	)
	_, addr := testutil.StartServer(t, nil)
	sub := testutil.MustDial(t, addr)
	t.Cleanup(func() { _ = sub.Close() })
	pub := testutil.MustDial(t, addr)
	t.Cleanup(func() { _ = pub.Close() })

	val := string(bytes.Repeat([]byte("v"), size))
	if got := sub.DoS("HELLO", "3"); got == "" {
		t.Fatal("empty HELLO reply")
	}
	if got := sub.DoS("SET", "big", val); got != "+OK" {
		t.Fatalf("SET: got %q, want +OK", got)
	}

	ack, err := sub.Do("SUBSCRIBE", "ch")
	if err != nil {
		t.Fatal(err)
	}
	if ack.Type != '>' || len(ack.Elems) != 3 || string(ack.Elems[0].Str) != "subscribe" {
		t.Fatalf("first frame: got type %q elems %d, want subscribe push", ack.Type, len(ack.Elems))
	}

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		for range nPush {
			if _, err := pub.Do("PUBLISH", "ch", val); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range nGet {
			if err := sub.Send("GET", "big"); err != nil {
				errs <- err
				return
			}
		}
	}()

	pushes, gets := 0, 0
	for pushes < nPush || gets < nGet {
		v, err := sub.Read()
		if err != nil {
			t.Fatalf("after %d pushes and %d replies: %v", pushes, gets, err)
		}
		switch {
		case v.Type == '>' && len(v.Elems) == 3 && string(v.Elems[0].Str) == "message":
			if len(v.Elems[2].Str) != size {
				t.Fatalf("push payload: got %d bytes, want %d", len(v.Elems[2].Str), size)
			}
			pushes++
		case v.Type == '$' && len(v.Str) == size:
			gets++
		default:
			t.Fatalf("unexpected frame: type %q, %d bytes, %d elems", v.Type, len(v.Str), len(v.Elems))
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
