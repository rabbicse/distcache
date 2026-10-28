//go:build !unix

package server

func cpuTimes() (user, sys float64) { return 0, 0 }
