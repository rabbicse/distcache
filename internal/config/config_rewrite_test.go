// Tests that CONFIG REWRITE preserves the lines it does not manage.
package config

import (
	"os"
	"strings"
	"testing"
)

func TestRewrite_PreservesUnmanagedLines(t *testing.T) {
	dir := t.TempDir()
	inc := writeFile(t, dir, "inc.conf", "hz 20\n")
	body := "# my comment\n\nport 7000\nrename-command FLUSHALL NOFLUSH\nrename-command DEBUG \"\"\n  # indented comment\ninclude " + inc + "\nport 7001\n"
	path := writeFile(t, dir, "redis.conf", body)
	c := Default()
	if err := c.LoadFile(path); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("maxmemory", "64mb", false); err != nil {
		t.Fatal(err)
	}
	if err := c.Rewrite(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		"# my comment\n\nport \"7001\"\nrename-command FLUSHALL NOFLUSH\nrename-command DEBUG \"\"\n  # indented comment\ninclude " + inc + "\n",
		"maxmemory \"67108864\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rewritten file missing %q; got:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\nport "); n != 1 {
		t.Errorf("rewritten file has %d port lines, want 1; got:\n%s", n, got)
	}
	fresh := Default()
	if err := fresh.LoadFile(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fresh.Port != 7001 || fresh.MaxMemory.Load() != 64<<20 || fresh.Renamed()["flushall"] != "noflush" && fresh.Renamed()["flushall"] != "NOFLUSH" {
		t.Errorf("reload: port %d maxmemory %d renamed %v", fresh.Port, fresh.MaxMemory.Load(), fresh.Renamed())
	}
	if nn, ok := fresh.Renamed()["debug"]; !ok || nn != "" {
		t.Errorf("reload: Renamed()[debug] = %q, %v; want \"\", true", nn, ok)
	}
	// A second rewrite must be stable.
	if err := c.Rewrite(); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(path); string(again) != got {
		t.Errorf("second rewrite changed the file:\n%s\nwant:\n%s", again, got)
	}
}
