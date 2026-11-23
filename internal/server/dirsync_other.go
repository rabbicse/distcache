//go:build !unix

// syncDir stub for platforms where directories cannot be fsynced (Windows
// makes renames durable through the file system itself).

package server

func syncDir(string) error { return nil }
