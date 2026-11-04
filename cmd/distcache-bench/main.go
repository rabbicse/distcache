package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	if err := realMain(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "distcache-bench:", err)
		os.Exit(1)
	}
}

// realMain parses redis-benchmark-compatible flags, runs the benchmark and
// prints results to stdout.
func realMain(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("distcache-bench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("h", "127.0.0.1", "server hostname")
	port := fs.Int("p", 6379, "server port")
	clients := fs.Int("c", 50, "number of parallel connections")
	requests := fs.Int("n", 100000, "total number of requests per test")
	pipeline := fs.Int("P", 1, "pipeline <numreq> requests")
	dataSize := fs.Int("d", 3, "data size of SET/LPUSH/HSET values in bytes")
	tests := fs.String("t", strings.Join(DefaultTests, ","), "comma-separated tests")
	keySpace := fs.Int("r", 0, "use random keys in the range [0, r)")
	quiet := fs.Bool("q", false, "quiet: one line per test")
	csv := fs.Bool("csv", false, "output CSV: test,ops_per_sec,p50_us,p95_us,p99_us")
	duration := fs.Duration("duration", 0, "run each test for this long instead of -n requests")
	if err := fs.Parse(args); err != nil {
		return err
	}

	results, err := Run(Config{
		Addr:     fmt.Sprintf("%s:%d", *host, *port),
		Clients:  *clients,
		Requests: *requests,
		Pipeline: *pipeline,
		DataSize: *dataSize,
		Tests:    strings.Split(*tests, ","),
		KeySpace: *keySpace,
		Duration: *duration,
	})
	if err != nil {
		return err
	}
	for _, r := range results {
		switch {
		case *csv:
			fmt.Fprintf(stdout, "%s,%.2f,%d,%d,%d\n", r.Test, r.OpsPerSec, r.P50.Microseconds(), r.P95.Microseconds(), r.P99.Microseconds())
		case *quiet:
			fmt.Fprintf(stdout, "%s: %.2f requests per second, p50=%s\n", strings.ToUpper(r.Test), r.OpsPerSec, r.P50.Round(time.Microsecond))
		default:
			fmt.Fprintf(stdout, "====== %s ======\n  %d requests in %.2f seconds\n  %.2f requests per second\n  latency per batch p50=%s p95=%s p99=%s\n\n",
				strings.ToUpper(r.Test), r.Requests, r.Seconds, r.OpsPerSec,
				r.P50.Round(time.Microsecond), r.P95.Round(time.Microsecond), r.P99.Round(time.Microsecond))
		}
	}
	return nil
}
