package server

import (
	"net"

	"github.com/rabbicse/distcache/internal/storage"
)

type Server struct {
	addr  string
	store storage.Store
}

func NewServer(addr string, store storage.Store) *Server {
	return &Server{
		addr:  addr,
		store: store,
	}
}

func (s *Server) Start() error {

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}

	for {

		conn, err := ln.Accept()
		if err != nil {
			continue
		}

		go s.handleConnection(conn)
	}
}
