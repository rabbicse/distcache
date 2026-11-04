package server

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
