// Tests that AOF write failures are not swallowed: once one happens, write
// commands are refused with MISCONF until the AOF is healthy again.
package server_test

import (
	"strings"
	"testing"
)

func TestAOF_WriteErrorRejectsWrites(t *testing.T) {
	s, c := startAOFServer(t, t.TempDir())
	if got := c.DoS("SET", "a", "1"); got != "+OK" {
		t.Fatalf("SET a: got %s, want +OK", got)
	}
	if err := s.BreakAOF(); err != nil {
		t.Fatal(err)
	}
	c.DoS("SET", "b", "2") // the write that hits the broken file
	if got := c.DoS("SET", "c", "3"); !strings.HasPrefix(got, "-MISCONF Errors writing to the AOF file: ") {
		t.Errorf("SET c after AOF failure: got %s, want -MISCONF Errors writing to the AOF file: ...", got)
	}
	if got := c.DoS("GET", "a"); got != `"1"` {
		t.Errorf("GET a after AOF failure: got %s, want \"1\"", got)
	}
	if got := c.DoS("CONFIG", "SET", "appendonly", "no"); got != "+OK" {
		t.Fatalf("CONFIG SET appendonly no: got %s, want +OK", got)
	}
	if got := c.DoS("SET", "c", "3"); got != "+OK" {
		t.Errorf("SET c with AOF off: got %s, want +OK", got)
	}
}
