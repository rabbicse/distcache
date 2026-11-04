package server_test

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/testutil"
)

// TestPubSub_PushesDoNotInterleaveWithReplies guards ordering, deadlock and
// message loss (the lock itself is covered by resp.TestWriter_Flush_HoldsMutex). It has a RESP3 subscriber pipeline
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

// TestSubscribe_NoMessageLostAfterAck subscribes mid-stream while a publisher
// runs; every PUBLISH issued after the ack was read must be delivered, in order.
func TestSubscribe_NoMessageLostAfterAck(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	pub := testutil.MustDial(t, addr)
	t.Cleanup(func() { _ = pub.Close() })
	sub := testutil.MustDial(t, addr)
	t.Cleanup(func() { _ = sub.Close() })

	var acked atomic.Bool
	var firstAfter atomic.Int64 // seq of first PUBLISH started after the ack
	firstAfter.Store(-1)
	stop := make(chan struct{})
	done := make(chan error, 1)
	var afterOK atomic.Int64
	var last atomic.Int64
	go func() {
		for seq := int64(0); ; seq++ {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			after := acked.Load()
			if after && firstAfter.Load() < 0 {
				firstAfter.Store(seq)
			}
			v, err := pub.Do("PUBLISH", "ch", strconv.FormatInt(seq, 10))
			if err != nil {
				done <- err
				return
			}
			if after {
				if v.Int != 1 {
					done <- fmt.Errorf("PUBLISH %d after ack returned %d, want 1", seq, v.Int)
					return
				}
				afterOK.Add(1)
			}
			last.Store(seq)
		}
	}()

	time.Sleep(time.Duration(1+time.Now().UnixNano()%5) * time.Millisecond)
	ack, err := sub.Do("SUBSCRIBE", "ch")
	if err != nil || len(ack.Elems) != 3 {
		t.Fatalf("ack: %v %v", ack, err)
	}
	acked.Store(true)
	for afterOK.Load() < 200 {
		time.Sleep(time.Millisecond)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	start := firstAfter.Load()
	end := last.Load()
	if start < 0 {
		t.Fatal("no publish after ack")
	}
	next, prev := start, int64(-1)
	for next <= end {
		v, err := sub.Read()
		if err != nil {
			t.Fatalf("waiting for seq %d (last %d): %v", next, end, err)
		}
		got, _ := strconv.ParseInt(string(v.Elems[2].Str), 10, 64)
		if got <= prev {
			t.Fatalf("out of order: %d after %d", got, prev)
		}
		prev = got
		if got < next {
			continue // published before the ack was observed
		}
		if got != next {
			t.Fatalf("gap: got seq %d, want %d", got, next)
		}
		next++
	}
}

// TestSubscribe_ManyChannelsNoDeadlock subscribes to enough channels that the
// acks exceed the writer's auto-flush threshold while wmu is held.
func TestSubscribe_ManyChannelsNoDeadlock(t *testing.T) {
	const n = 5000
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	t.Cleanup(func() { _ = c.Close() })
	args := []string{"SUBSCRIBE"}
	for i := range n {
		args = append(args, fmt.Sprintf("%s-%d", strings.Repeat("c", 40), i))
	}
	if err := c.Send(args...); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		v, err := c.Read()
		if err != nil {
			t.Fatalf("ack %d: %v", i, err)
		}
		if v.Type != '*' && v.Type != '>' || len(v.Elems) != 3 || v.Elems[2].Int != int64(i+1) {
			t.Fatalf("ack %d: bad frame type %q elems %d", i, v.Type, len(v.Elems))
		}
	}
}
