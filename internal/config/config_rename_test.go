// Tests that rename-command with an empty new name disables a command.
package config

import "testing"

func TestLoadFile_RenameCommandEmpty_DisablesCommand(t *testing.T) {
	path := writeFile(t, t.TempDir(), "redis.conf", "rename-command FLUSHALL \"\"\nrename-command CONFIG CFG\n")
	c := Default()
	if err := c.LoadFile(path); err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if nn, ok := c.Renamed()["flushall"]; !ok || nn != "" {
		t.Errorf("Renamed()[flushall] = %q, %v; want \"\", true", nn, ok)
	}
	if nn := c.Renamed()["config"]; nn != "CFG" {
		t.Errorf("Renamed()[config] = %q, want CFG", nn)
	}
}

func TestParseArgs_RenameCommandEmpty_DisablesCommand(t *testing.T) {
	c := Default()
	if err := c.ParseArgs([]string{"--rename-command", "FLUSHALL", ""}); err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if nn, ok := c.Renamed()["flushall"]; !ok || nn != "" {
		t.Errorf("Renamed()[flushall] = %q, %v; want \"\", true", nn, ok)
	}
}
