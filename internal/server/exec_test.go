package server

import (
	"errors"
	"strings"
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
	ss.Close()
	if v, _ := s.Exec("CLIENT", "LIST"); strings.Contains(string(v.Str), "watch=1") {
		t.Fatalf("watch still registered: %q", v.Str)
	}
}
