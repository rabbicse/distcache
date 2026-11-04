// Integration and differential tests for the server commands (COMMAND,
// CONFIG, INFO, persistence, MEMORY, LATENCY, SLOWLOG, ACL, WAIT, ROLE, ...).
// DEBUG, SHUTDOWN, MONITOR, MODULE and the replication handshake commands are
// covered by e2e tests (see coverageExempt).
package server_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rabbicse/distcache/internal/testutil"
)

// serverCmdScript covers the server commands with replies that are
// deterministic on both servers or allowlisted. BGSAVE is last so its
// background save cannot collide with SAVE.
var serverCmdScript = concat(serverCmdIntrospectionScript, serverCmdAdminScript)

var serverCmdIntrospectionScript = []testutil.Step{
	testutil.S(`*["a"]`, "COMMAND", "GETKEYS", "SET", "a", "b"),
	testutil.S(`*["a", "b"]`, "COMMAND", "GETKEYS", "MSET", "a", "1", "b", "2"),
	testutil.S("-ERR The command has no key arguments", "COMMAND", "GETKEYS", "PING"),
	testutil.S("-ERR Invalid command specified", "COMMAND", "GETKEYS", "nosuch"),
	testutil.S("-ERR Invalid number of arguments specified for command", "COMMAND", "GETKEYS", "GET"),
	testutil.S("-ERR unknown subcommand 'FOO'. Try COMMAND HELP.", "COMMAND", "FOO"),
	testutil.S(`*["maxmemory", "0"]`, "CONFIG", "GET", "maxmemory"),
	testutil.S("+OK", "CONFIG", "SET", "maxmemory", "0"),
	testutil.S(`*[]`, "CONFIG", "GET", "nosuch"),
	testutil.S("-ERR Unknown option or number of arguments for CONFIG SET - 'nosuch'", "CONFIG", "SET", "nosuch", "1"),
	testutil.S("+OK", "CONFIG", "RESETSTAT"),
	testutil.S(arityErr("config|get"), "CONFIG", "GET"),
	testutil.S("-ERR unknown subcommand 'FOO'. Try CONFIG HELP.", "CONFIG", "FOO"),
	testutil.S("+OK", "SET", "k", "v"),
	testutil.S("(nil)", "MEMORY", "USAGE", "nope"),
	testutil.S("-ERR unknown subcommand 'FOO'. Try MEMORY HELP.", "MEMORY", "FOO"),
	testutil.S(`*[]`, "LATENCY", "LATEST"),
	testutil.S(":0", "LATENCY", "RESET"),
	testutil.S("-ERR unknown subcommand 'FOO'. Try LATENCY HELP.", "LATENCY", "FOO"),
	testutil.S("+OK", "SLOWLOG", "RESET"),
	testutil.S(":0", "SLOWLOG", "LEN"),
	testutil.S("-ERR unknown subcommand 'FOO'. Try SLOWLOG HELP.", "SLOWLOG", "FOO"),
	testutil.S(`"default"`, "ACL", "WHOAMI"),
	testutil.S(`*["default"]`, "ACL", "USERS"),
	testutil.S(`*[]`, "ACL", "LOG"),
	testutil.S(`*["master", :0, *[]]`, "ROLE"),
	testutil.S(arityErr("role"), "ROLE", "x"),
}

var serverCmdAdminScript = []testutil.Step{
	testutil.S(":0", "WAIT", "0", "0"),
	testutil.S(errNotInt, "WAIT", "x", "0"),
	testutil.S("-ERR timeout is negative", "WAIT", "0", "-1"),
	testutil.S(arityErr("wait"), "WAIT", "0"),
	testutil.S("*[:0, :0]", "WAITAOF", "0", "0", "0"),
	testutil.S("-ERR WAITAOF cannot be used when numlocal is set but appendonly is disabled.", "WAITAOF", "1", "0", "0"),
	testutil.S(arityErr("waitaof"), "WAITAOF", "0", "0"),
	testutil.S("+OK", "REPLICAOF", "NO", "ONE"),
	testutil.S("+OK", "SLAVEOF", "NO", "ONE"),
	testutil.S(arityErr("replicaof"), "REPLICAOF", "NO"),
	testutil.S(errClusterDisabled, "CLUSTER", "INFO"),
	testutil.S(arityErr("time"), "TIME", "x"),
	testutil.S(arityErr("lastsave"), "LASTSAVE", "x"),
	testutil.S(arityErr("save"), "SAVE", "x"),
	testutil.S("+OK", "SAVE"),
	testutil.S(errSyntax, "BGSAVE", "FOO"),
	testutil.S("+Background saving started", "BGSAVE"),
}

// serverCmdDivergences adds the wall-clock commands, whose values cannot
// match between two servers, to the shared allowlist.
var serverCmdDivergences = append(append([]testutil.Divergence(nil), testutil.KnownDivergences...),
	testutil.Divergence{Cmd: "time", Reason: "wall-clock value"},
	testutil.Divergence{Cmd: "lastsave", Reason: "timestamp of the server's own last save"},
)

// serverCmdAllowlistedScript runs allowlisted or clock-dependent commands
// whose content is checked by shape in TestServerCmdCommands_Shapes.
var serverCmdAllowlistedScript = []testutil.Step{
	testutil.S("", "COMMAND"),
	testutil.S("", "COMMAND", "COUNT"),
	testutil.S("", "COMMAND", "INFO", "get"),
	testutil.S("", "COMMAND", "DOCS", "get"),
	testutil.S("", "COMMAND", "LIST"),
	testutil.S("", "INFO"),
	testutil.S("", "INFO", "server"),
	testutil.S("", "TIME"),
	testutil.S("", "LASTSAVE"),
	testutil.S("", "MEMORY", "USAGE", "nope"),
	testutil.S("", "MEMORY", "DOCTOR"),
	testutil.S("", "MEMORY", "STATS"),
	testutil.S("", "MEMORY", "HELP"),
	testutil.S("", "LATENCY", "HELP"),
	testutil.S("", "LOLWUT"),
	testutil.S("", "SLOWLOG", "GET"),
	testutil.S("", "ACL", "CAT"),
	testutil.S("", "ACL", "LIST"),
	testutil.S("+OK", "SLOWLOG", "RESET"),
}

func TestServerCmdCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, serverCmdScript)
	waitInfoField(t, c, "persistence", "rdb_bgsave_in_progress:0")
	testutil.RunScript(t, c, []testutil.Step{testutil.S(":0", "WAIT", "0", "0")})
}

func TestServerCmdCommandsDifferential(t *testing.T) {
	testutil.Differential(t, concat(serverCmdAllowlistedScript, serverCmdScript), serverCmdDivergences)
}

// waitInfoField polls INFO section until it contains line, failing after 5s.
func waitInfoField(t *testing.T, c *testutil.Client, section, line string) {
	t.Helper()
	testutil.RecordCommands(t)("info")
	deadline := time.Now().Add(5 * time.Second)
	for {
		v, err := c.Do("INFO", section)
		if err != nil {
			t.Fatalf("INFO %s: %v", section, err)
		}
		if strings.Contains(string(v.Str), line+"\r\n") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("INFO %s: got %q, want a line %q", section, v.Str, line)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestServerCmdCommands_Shapes checks allowlisted or clock-dependent replies
// by their shape: INFO section headers, array/integer types, help text.
func TestServerCmdCommands_Shapes(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	rec := testutil.RecordCommands(t)
	if got := c.DoS("SET", "k", "v"); got != "+OK" {
		t.Fatalf("SET: got %s, want +OK", got)
	}
	tests := []struct {
		cmd    []string
		prefix string
	}{
		{[]string{"COMMAND"}, `*[*["`},
		{[]string{"COMMAND", "COUNT"}, ":"},
		{[]string{"COMMAND", "INFO", "get"}, `*[*["get", :2, `},
		{[]string{"COMMAND", "DOCS", "get"}, `*["get", `},
		{[]string{"COMMAND", "LIST"}, `*["`},
		{[]string{"TIME"}, `*["1`},
		{[]string{"LASTSAVE"}, ":"},
		{[]string{"MEMORY", "USAGE", "k"}, ":"},
		{[]string{"MEMORY", "DOCTOR"}, `"`},
		{[]string{"MEMORY", "STATS"}, `*["peak.allocated", `},
		{[]string{"MEMORY", "HELP"}, `*[+MEMORY <subcommand>`},
		{[]string{"LATENCY", "HELP"}, `*[+LATENCY <subcommand>`},
		{[]string{"LOLWUT"}, `"`},
		{[]string{"ACL", "CAT"}, `*["`},
		{[]string{"ACL", "LIST"}, `*["user default on`},
		{[]string{"SLOWLOG", "GET"}, `*[`},
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.cmd, " "), func(t *testing.T) {
			rec(tc.cmd[0])
			if got := c.DoS(tc.cmd...); !strings.HasPrefix(got, tc.prefix) {
				t.Fatalf("got %s, want prefix %s", got, tc.prefix)
			}
		})
	}
	rec("info")
	v, err := c.Do("INFO")
	if err != nil {
		t.Fatalf("INFO: %v", err)
	}
	for _, h := range []string{"# Server", "# Clients", "# Memory", "# Persistence", "# Stats", "# Replication", "# CPU", "# Keyspace"} {
		if !strings.Contains(string(v.Str), h+"\r\n") {
			t.Errorf("INFO: missing section header %q", h)
		}
	}
	v, err = c.Do("INFO", "server")
	if err != nil {
		t.Fatalf("INFO server: %v", err)
	}
	if s := string(v.Str); !strings.HasPrefix(s, "# Server\r\n") || strings.Contains(s, "# Clients") {
		t.Fatalf("INFO server: got %q, want only the Server section", s)
	}
}

// TestServerCmdCommands_SlowlogRecords lowers the threshold to 0 so every
// command is logged.
func TestServerCmdCommands_SlowlogRecords(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "SLOWLOG", "RESET"),
		testutil.S("+OK", "CONFIG", "SET", "slowlog-log-slower-than", "0"),
		testutil.S("+PONG", "PING"),
	})
	if got := c.DoS("SLOWLOG", "LEN"); got == ":0" || !strings.HasPrefix(got, ":") {
		t.Fatalf("SLOWLOG LEN: got %s, want a positive integer", got)
	}
	if got := c.DoS("SLOWLOG", "GET", "1"); !strings.HasPrefix(got, "*[*[:") {
		t.Fatalf("SLOWLOG GET 1: got %s, want one entry", got)
	}
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+OK", "CONFIG", "SET", "slowlog-log-slower-than", "10000"),
		testutil.S("+OK", "SLOWLOG", "RESET"),
		testutil.S(":0", "SLOWLOG", "LEN"),
	})
}

// TestServerCmdCommands_BgRewriteAOF runs on DistCache only: on Redis it
// forks a rewrite child that would race with BGSAVE in the shared script.
func TestServerCmdCommands_BgRewriteAOF(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	testutil.RunScript(t, c, []testutil.Step{
		testutil.S("+Background append only file rewriting started", "BGREWRITEAOF"),
	})
	waitInfoField(t, c, "persistence", "aof_rewrite_in_progress:0")
}

// Redis's configRewriteCommand replies without a "Rewriting config file:" prefix.
func TestServerCmdCommands_ConfigRewriteNoFile(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S("-ERR The server is running without a config file", "CONFIG", "REWRITE"),
	})
}

// Redis's ACL LOG RESET clears the log and replies +OK.
func TestServerCmdCommands_ACLLogReset(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S("+OK", "ACL", "LOG", "RESET"),
	})
}

// Redis rejects an unknown ACL subcommand with the generic subcommand error.
func TestServerCmdCommands_ACLUnknownSubcommand(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S("-ERR unknown subcommand 'FOO'. Try ACL HELP.", "ACL", "FOO"),
	})
}
