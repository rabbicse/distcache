package server

import (
	"bufio"
	"net"
)

func (s *Server) handleConnection(conn net.Conn) {

	defer conn.Close()

	reader := bufio.NewReader(conn)

	for {

		req, err := protocol.Parse(reader)

		if err != nil {
			return
		}

		resp := s.execute(req)

		protocol.Write(conn, resp)
	}
}
