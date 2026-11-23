//go:build unix

// syncDir makes a rename inside a directory durable on Unix.

package server

import (
	"errors"
	"os"
)

// syncDir fsyncs dir so a file renamed into it survives a power loss.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
