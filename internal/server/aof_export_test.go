package server

// BreakAOF closes the AOF's file descriptor behind the propagator's back, so
// every later AOF write fails as it would on a dead disk.
func (s *Server) BreakAOF() error {
	p := s.propagator
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.aofFile.Close()
}
