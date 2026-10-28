// Command distcache-server runs a DistCache node.
//
// Usage mirrors redis-server:
//
//	distcache-server [/path/to/distcache.conf] [--port 6379] [--appendonly yes] ...
package main

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/server"
	"github.com/rabbicse/distcache/pkg/logger"
)

func main() {
	args := os.Args[1:]
	if len(args) == 1 && (args[0] == "-v" || args[0] == "--version") {
		fmt.Printf("DistCache server v=%s redis-compat=%s go=%s\n", server.Version, server.RedisVersion, runtime.Version())
		return
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Println("Usage: distcache-server [/path/to/distcache.conf] [--<option> <value> ...]")
		fmt.Println("Examples:")
		fmt.Println("  distcache-server --port 6380 --appendonly yes")
		fmt.Println("  distcache-server /etc/distcache.conf --maxmemory 2gb --maxmemory-policy allkeys-lru")
		return
	}
	cfg := config.Default()
	if err := cfg.ParseArgs(args); err != nil {
		fmt.Fprintln(os.Stderr, "*** FATAL CONFIG ERROR ***")
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log, closer, err := logger.New(cfg.LogLevel, cfg.LogFile, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot open log file:", err)
		os.Exit(1)
	}
	defer closer.Close()
	srv := server.New(cfg, log)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.Info("received signal, scheduling shutdown", "signal", s.String())
		save := len(cfg.Save) > 0 && !cfg.AppendOnly.Load()
		_ = srv.Shutdown(save)
	}()
	if err := srv.Start(); err != nil {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
}
