// Command server is kept for backward compatibility with
// `go run cmd/server/main.go`; it behaves exactly like distcache-server.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/server"
	"github.com/rabbicse/distcache/pkg/logger"
)

func main() {
	cfg := config.Default()
	if err := cfg.ParseArgs(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log, closer, err := logger.New(cfg.LogLevel, cfg.LogFile, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer closer.Close()
	srv := server.New(cfg, log)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; _ = srv.Shutdown(len(cfg.Save) > 0 && !cfg.AppendOnly.Load()) }()
	if err := srv.Start(); err != nil {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
}
