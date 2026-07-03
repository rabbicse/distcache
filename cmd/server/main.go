package main

import (
	"flag"
	"log"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/server"
)

const defaultListenAddr = ":5001"

func main() {
	listenAddr := flag.String("listenAddr", defaultListenAddr, "listen address of the goredis server")
	flag.Parse()
	server := server.NewServer(&config.Config{
		ListenAddr: *listenAddr,
	})
	log.Fatal(server.Start())
}
