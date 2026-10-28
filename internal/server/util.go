package server

import (
	"bufio"
	"bytes"
	"io"

	"github.com/rabbicse/distcache/internal/ds"
)

type bytesBuf struct{ b []byte }

func (w *bytesBuf) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

func newBufioReader(b []byte) *bufio.Reader { return bufio.NewReaderSize(bytes.NewReader(b), 1<<16) }

func ioReadFull(r io.Reader, buf []byte) (int, error) { return io.ReadFull(r, buf) }

func matchGlob(pattern, s string) bool { return ds.Match(pattern, s, true) }
