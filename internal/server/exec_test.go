package server

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/resp"
)

func newExecServer(t *testing.T) *Server {
	t.Helper()
	return New(config.Default(), nil)
}

func mustExec(t *testing.T, ss *Session, args ...string) resp.Value {
	t.Helper()
	v, err := ss.Exec(args...)
	if err != nil {
		t.Fatalf("Exec(%v): %v", args, err)
	}
	return v
}

func TestExec_SetGet(t *testing.T) {
	s := newExecServer(t)
	if v, err := s.Exec("SET", "k", "v"); err != nil || v.Type != '+' || string(v.Str) != "OK" {
		t.Fatalf("SET: got %+v, %v, want +OK", v, err)
	}
	if v, err := s.Exec("GET", "k"); err != nil || v.Type != '$' || string(v.Str) != "v" {
		t.Fatalf("GET: got %+v, %v, want $v", v, err)
	}
}

func TestSession_KeepsState(t *testing.T) {
	s := newExecServer(t)
	ss := s.NewSession()
	t.Cleanup(ss.Close)
	mustExec(t, ss, "SELECT", "3")
	mustExec(t, ss, "SET", "a", "1")
	if v := mustExec(t, ss, "DBSIZE"); v.Type != ':' || v.Int != 1 {
		t.Fatalf("session DBSIZE: got %+v, want :1", v)
	}
	if v, _ := s.Exec("DBSIZE"); v.Int != 0 {
		t.Fatalf("fresh DBSIZE: got %+v, want :0", v)
	}
}

func TestExec_UnknownCommand(t *testing.T) {
	v, err := newExecServer(t).Exec("NOSUCH", "x")
	if err != nil || v.Type != '-' || !strings.HasPrefix(string(v.Str), "ERR unknown command") {
		t.Fatalf("got %+v, %v, want -ERR unknown command", v, err)
	}
}

func TestExec_BlockingTimeoutArgument(t *testing.T) {
	s := newExecServer(t)
	zero := []struct {
		name string
		args []string
	}{
		{"BLPOP", []string{"BLPOP", "q", "0"}},
		{"BRPOP", []string{"BRPOP", "q", "r", "0"}},
		{"BZPOPMIN", []string{"BZPOPMIN", "q", "0"}},
		{"BZPOPMAX", []string{"BZPOPMAX", "q", "0.0"}},
		{"BLMOVE", []string{"BLMOVE", "a", "b", "LEFT", "RIGHT", "0"}},
		{"BRPOPLPUSH", []string{"BRPOPLPUSH", "a", "b", "0"}},
		{"BLMPOP", []string{"BLMPOP", "0", "1", "q", "LEFT"}},
		{"BZMPOP", []string{"BZMPOP", "0", "1", "q", "MIN"}},
		{"XREAD", []string{"XREAD", "COUNT", "1", "BLOCK", "0", "STREAMS", "q", "$"}},
		{"XREADGROUP", []string{"XREADGROUP", "GROUP", "g", "c", "BLOCK", "0", "STREAMS", "q", ">"}},
		{"lowercase", []string{"blpop", "q", "0"}},
	}
	for _, name := range []string{"BLMPOP", "BZMPOP", "BLPOP", "XREAD", "BLMOVE"} {
		t.Run("bare/"+name, func(t *testing.T) {
			v, err := s.Exec(name)
			if err != nil || v.Type != '-' || !strings.Contains(string(v.Str), "wrong number of arguments") {
				t.Fatalf("got %+v, %v, want wrong-arity error reply", v, err)
			}
		})
	}
	for _, tc := range zero {
		t.Run("zero/"+tc.name, func(t *testing.T) {
			if _, err := s.Exec(tc.args...); !errors.Is(err, ErrWouldBlock) {
				t.Fatalf("got %v, want ErrWouldBlock", err)
			}
		})
	}
	nonzero := []struct {
		name string
		args []string
	}{
		{"BLPOP", []string{"BLPOP", "q", "0.05"}},
		{"BRPOP", []string{"BRPOP", "q", "r", "0.05"}},
		{"BZPOPMIN", []string{"BZPOPMIN", "q", "0.05"}},
		{"BZPOPMAX", []string{"BZPOPMAX", "q", "0.05"}},
		{"BLMOVE", []string{"BLMOVE", "a", "b", "LEFT", "RIGHT", "0.05"}},
		{"BRPOPLPUSH", []string{"BRPOPLPUSH", "a", "b", "0.05"}},
		{"BLMPOP", []string{"BLMPOP", "0.05", "1", "q", "LEFT"}},
		{"BZMPOP", []string{"BZMPOP", "0.05", "1", "q", "MIN"}},
		{"XREAD", []string{"XREAD", "BLOCK", "50", "STREAMS", "q", "$"}},
		{"XREAD key named block", []string{"XREAD", "STREAMS", "BLOCK", "0"}},
		{"XREADGROUP", []string{"XREADGROUP", "GROUP", "g", "c", "BLOCK", "50", "STREAMS", "q", ">"}},
	}
	for _, tc := range nonzero {
		t.Run("nonzero/"+tc.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { _, err := s.Exec(tc.args...); done <- err }()
			select {
			case err := <-done:
				if errors.Is(err, ErrWouldBlock) {
					t.Fatalf("got ErrWouldBlock for non-zero timeout")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("did not return within 2s")
			}
		})
	}
}

func TestExec_BlockingWithTimeout(t *testing.T) {
	s := newExecServer(t)
	start := time.Now()
	v, err := s.Exec("BLPOP", "q", "0.05")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Null {
		t.Fatalf("got %+v, want null array", v)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v, want < 1s", d)
	}
}

func TestExec_BlockingInMultiDoesNotBlock(t *testing.T) {
	ss := newExecServer(t).NewSession()
	t.Cleanup(ss.Close)
	mustExec(t, ss, "MULTI")
	if v := mustExec(t, ss, "BLPOP", "q", "0"); string(v.Str) != "QUEUED" {
		t.Fatalf("got %+v, want QUEUED", v)
	}
}

func TestSession_CloseRemovesSubscription(t *testing.T) {
	s := newExecServer(t)
	ss := s.NewSession()
	mustExec(t, ss, "SUBSCRIBE", "news")
	numsub := func() int64 {
		v, err := s.Exec("PUBSUB", "NUMSUB", "news")
		if err != nil || len(v.Elems) != 2 {
			t.Fatalf("NUMSUB: got %+v, %v", v, err)
		}
		return v.Elems[1].Int
	}
	if n := numsub(); n != 1 {
		t.Fatalf("before Close: NUMSUB got %d, want 1", n)
	}
	if v, _ := s.Exec("PUBLISH", "news", "hi"); v.Int != 1 {
		t.Fatalf("PUBLISH: got %d receivers, want 1", v.Int)
	}
	ss.Close()
	ss.Close() // idempotent
	if n := numsub(); n != 0 {
		t.Fatalf("after Close: NUMSUB got %d, want 0", n)
	}
}

func TestSession_CloseUnwatches(t *testing.T) {
	s := newExecServer(t)
	ss := s.NewSession()
	mustExec(t, ss, "WATCH", "k")
	if s.watches.count.Load() != 1 {
		t.Fatalf("watch count before Close: got %d, want 1", s.watches.count.Load())
	}
	ss.Close()
	s.watches.mu.Lock()
	defer s.watches.mu.Unlock()
	for wk, set := range s.watches.m {
		if _, ok := set[ss.c]; ok {
			t.Fatalf("closed session still watches %v", wk)
		}
	}
	if n := s.watches.count.Load(); n != 0 {
		t.Fatalf("watch count after Close: got %d, want 0", n)
	}
}

func TestSession_CloseReleasesBlockedExec(t *testing.T) {
	s := newExecServer(t)
	ss := s.NewSession()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ss.Exec("BLPOP", "q", "30")
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !ss.c.blockedNow.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Exec never blocked")
		}
		time.Sleep(time.Millisecond)
	}
	ss.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Exec still blocked 1s after Close")
	}
}

func TestSession_ReadPush(t *testing.T) {
	s := newExecServer(t)
	ss := s.NewSession()
	t.Cleanup(ss.Close)
	mustExec(t, ss, "SUBSCRIBE", "ch")
	if _, err := s.Exec("PUBLISH", "ch", "hello"); err != nil {
		t.Fatal(err)
	}
	v, err := ss.ReadPush(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Elems) != 3 || string(v.Elems[0].Str) != "message" || string(v.Elems[1].Str) != "ch" || string(v.Elems[2].Str) != "hello" {
		t.Fatalf("got %+v, want [message ch hello]", v)
	}
	if _, err := ss.ReadPush(10 * time.Millisecond); err == nil {
		t.Fatal("expected timeout error with no pending push")
	}
}

func TestExec_BlockOptionSkipsGroupNames(t *testing.T) {
	s := newExecServer(t)
	for _, args := range [][]string{
		{"XREADGROUP", "GROUP", "g", "block", "BLOCK", "0", "STREAMS", "s", ">"},
		{"XREADGROUP", "GROUP", "streams", "c", "BLOCK", "0", "STREAMS", "s", ">"},
		{"XREAD", "COUNT", "1", "BLOCK", "0", "STREAMS", "s", "$"},
	} {
		if _, err := s.Exec(args...); !errors.Is(err, ErrWouldBlock) {
			t.Errorf("%v: got %v, want ErrWouldBlock", args, err)
		}
	}
}

func TestServer_CommandKeyCount(t *testing.T) {
	s := newExecServer(t)
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"SET", "a", "b"}, 1},
		{[]string{"MSET", "a", "1", "b", "2"}, 2},
		{[]string{"PING"}, 0},
		{[]string{"nosuch", "x"}, 0},
		{[]string{"GET"}, 0},
	} {
		if got := s.CommandKeyCount(tc.args); got != tc.want {
			t.Errorf("%v: got %d, want %d", tc.args, got, tc.want)
		}
	}
	if got := s.CommandName("sEt"); got != "set" && got != "SET" {
		t.Errorf("CommandName: got %q", got)
	}
	if got := s.CommandName("hunter2"); got != "" {
		t.Errorf("CommandName unknown: got %q", got)
	}
}

func TestExec_XReadLastBlockWins(t *testing.T) {
	s := newExecServer(t)
	if _, err := s.Exec("XREAD", "BLOCK", "1", "BLOCK", "0", "STREAMS", "s", "$"); !errors.Is(err, ErrWouldBlock) {
		t.Errorf("got %v, want ErrWouldBlock", err)
	}
	if v, ok := XReadBlockArg([]string{"XREAD", "BLOCK", "1", "BLOCK", "999999", "STREAMS", "s", "$"}); !ok || v != "999999" {
		t.Errorf("got %q %v, want 999999", v, ok)
	}
}

func TestXRead_KeysSkipOptionValues(t *testing.T) {
	s := newExecServer(t)
	v, err := s.Exec("COMMAND", "GETKEYS", "XREADGROUP", "GROUP", "streams", "c", "STREAMS", "s", ">")
	if err != nil || len(v.Elems) != 1 || string(v.Elems[0].Str) != "s" {
		t.Fatalf("got %+v %v, want [s]", v, err)
	}
	v, err = s.Exec("COMMAND", "GETKEYS", "XREAD", "COUNT", "1", "BLOCK", "5", "STREAMS", "a", "b", "0", "0")
	if err != nil || len(v.Elems) != 2 || string(v.Elems[0].Str) != "a" || string(v.Elems[1].Str) != "b" {
		t.Fatalf("got %+v %v, want [a b]", v, err)
	}
}

func TestXReadGroup_GroupNamedStreamsConcurrentWriters(t *testing.T) {
	s := newExecServer(t)
	mustExec(t, s.NewSession(), "XGROUP", "CREATE", "s", "streams", "$", "MKSTREAM")
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ss := s.NewSession()
			defer ss.Close()
			for range 100 {
				if _, err := ss.Exec("XADD", "s", "*", "f", "v"); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ss := s.NewSession()
		defer ss.Close()
		for range 100 {
			if _, err := ss.Exec("XREADGROUP", "GROUP", "streams", "c", "COUNT", "10", "STREAMS", "s", ">"); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
}
