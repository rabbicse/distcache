package server

import (
	"io"

	"github.com/rabbicse/distcache/internal/resp"
)

// RegisterTestCommand adds a write command named name (arity -1) running h.
// It writes to s.cmds without synchronisation, so call it before any client
// connects (right after testutil.StartServer returns, before dialling) and
// never while commands are executing.
func (s *Server) RegisterTestCommand(name string, h func(*Ctx)) {
	s.addCommand(regSpec{name, -1, "write", 1, 1, 1, "test", h, nil}, nil, nil)
}

// CommandNames returns the sorted names of every registered top-level command.
// It reads s.cmds unsynchronised, so call it when no commands are executing.
func (s *Server) CommandNames() []string { return s.sortedCommandNames() }

// DispatchFunc returns a function that runs argv through processCommand on a
// private in-process client whose replies are discarded. It measures dispatch
// cost without RESP parsing or socket I/O (BenchmarkProcessCommandGet). The
// returned function is not safe for concurrent use.
func (s *Server) DispatchFunc(argv [][]byte) func() {
	c := s.NewSession().c
	c.w = resp.NewWriter(io.Discard, 2)
	c.w.Mu = &c.wmu
	return func() {
		s.processCommand(c, argv)
		c.w.Reset()
	}
}
