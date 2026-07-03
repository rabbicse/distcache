package server

import (
	"strings"

	"github.com/rabbicse/distcache/internal/commands"
)

func (s *Server) execute(req []string) any {

	cmd := strings.ToUpper(req[0])

	args := req[1:]

	switch cmd {

	case "PING":
		return commands.Ping()

	case "SET":
		return commands.Set(s.store, args)

	case "GET":
		return commands.Get(s.store, args)

	case "DEL":
		return commands.Del(s.store, args)

	default:
		return "ERR unknown command"
	}
}
