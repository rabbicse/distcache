// Recovery tests for append-only files whose tail was cut by a crash: every
// complete command must survive, the file must be truncated to the last
// complete unit, and it must load again after more writes are appended.
package server_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/rabbicse/distcache/internal/config"
	"github.com/rabbicse/distcache/internal/server"
	"github.com/rabbicse/distcache/internal/testutil"
)

// startAOFServer starts a server with appendonly on in dir.
func startAOFServer(t *testing.T, dir string) (*server.Server, *testutil.Client) {
	t.Helper()
	s, addr := testutil.StartServer(t, func(c *config.Config) {
		c.Dir = dir
		c.AppendOnly.Store(true)
		c.AppendFsync = "always"
	})
	return s, testutil.MustDial(t, addr)
}

// aofUnit is a command, or a whole MULTI...EXEC block, in an AOF.
type aofUnit struct {
	end  int64 // offset just past the unit
	keys int   // keys the unit creates
}

// splitAOFUnits parses a plain RESP AOF into units.
func splitAOFUnits(t *testing.T, data []byte) []aofUnit {
	t.Helper()
	var units []aofUnit
	pos, inMulti, pending := 0, false, 0
	line := func() int {
		i := bytes.Index(data[pos:], []byte("\r\n"))
		n, err := strconv.Atoi(string(data[pos+1 : pos+i]))
		if err != nil {
			t.Fatalf("bad AOF at %d: %v", pos, err)
		}
		pos += i + 2
		return n
	}
	for pos < len(data) {
		var args []string
		for range line() {
			l := line()
			args = append(args, string(data[pos:pos+l]))
			pos += l + 2
		}
		switch args[0] {
		case "MULTI":
			inMulti = true
		case "EXEC":
			inMulti = false
		case "SET":
			pending++
		}
		if !inMulti {
			units = append(units, aofUnit{end: int64(pos), keys: pending})
			pending = 0
		}
	}
	return units
}

func TestAOF_TruncatedTailAtEveryOffset(t *testing.T) {
	dir := t.TempDir()
	s, c := startAOFServer(t, dir)
	for _, cmd := range [][]string{
		{"SET", "a", "1"}, {"SET", "b", "22"}, {"MULTI"}, {"SET", "c", "333"},
		{"SET", "d", "4444"}, {"EXEC"}, {"SET", "e", "55555"},
	} {
		c.DoS(cmd...)
	}
	if err := s.Shutdown(false); err != nil {
		t.Fatal(err)
	}
	full, err := os.ReadFile(filepath.Join(dir, "appendonly.aof"))
	if err != nil {
		t.Fatal(err)
	}
	units := splitAOFUnits(t, full)
	for cut := int64(1); cut <= int64(len(full)); cut++ {
		var wantSize int64
		wantKeys := 0
		for _, u := range units {
			if u.end <= cut {
				wantSize = u.end
				wantKeys += u.keys
			}
		}
		t.Run(strconv.FormatInt(cut, 10), func(t *testing.T) {
			d := t.TempDir()
			aof := filepath.Join(d, "appendonly.aof")
			if err := os.WriteFile(aof, full[:cut], 0o644); err != nil {
				t.Fatal(err)
			}
			s, c := startAOFServer(t, d)
			if got, want := c.DoS("DBSIZE"), ":"+strconv.Itoa(wantKeys); got != want {
				t.Errorf("DBSIZE after recovery: got %s, want %s", got, want)
			}
			if fi, err := os.Stat(aof); err != nil || fi.Size() != wantSize {
				t.Errorf("AOF size after recovery: got %v (%v), want %d", fi.Size(), err, wantSize)
			}
			if got := c.DoS("SET", "after", "x"); got != "+OK" {
				t.Fatalf("SET after: got %s, want +OK", got)
			}
			if err := s.Shutdown(false); err != nil {
				t.Fatal(err)
			}
			_, c = startAOFServer(t, d)
			if got, want := c.DoS("DBSIZE"), ":"+strconv.Itoa(wantKeys+1); got != want {
				t.Errorf("DBSIZE after second restart: got %s, want %s", got, want)
			}
			if got := c.DoS("GET", "after"); got != `"x"` {
				t.Errorf("GET after: got %s, want \"x\"", got)
			}
		})
	}
}
