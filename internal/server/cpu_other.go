//go:build !unix

// cpuTimes stub for platforms without getrusage.

package server

func cpuTimes() (user, sys float64) { return 0, 0 }
