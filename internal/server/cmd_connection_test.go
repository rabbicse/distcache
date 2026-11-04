// Integration and differential tests for the connection commands
// (PING, ECHO, AUTH, HELLO, CLIENT, SELECT, READONLY/READWRITE, RESET, QUIT).
package server_test

import (
	"strings"
	"testing"

	"github.com/rabbicse/distcache/internal/testutil"
)

const errClusterDisabled = "-ERR This instance has cluster support disabled"

// connectionScript covers every connection command. QUIT is last because it
// closes the connection.
var connectionScript = concat(connectionBasicScript, connectionClientScript, connectionResetScript)

var connectionBasicScript = []testutil.Step{
	testutil.S("+PONG", "PING"),
	testutil.S(`"hi"`, "PING", "hi"),
	testutil.S(arityErr("ping"), "PING", "a", "b"),
	testutil.S(`"hi"`, "ECHO", "hi"),
	testutil.S(`""`, "ECHO", ""),
	testutil.S(arityErr("echo"), "ECHO"),
	testutil.S(arityErr("echo"), "ECHO", "a", "b"),
	testutil.S("-ERR AUTH <password> called without any password configured for the default user. Are you sure your configuration is correct?", "AUTH", "foo"),
	testutil.S("-WRONGPASS invalid username-password pair or user is disabled.", "AUTH", "bob", "x"),
	testutil.S("+OK", "AUTH", "default", "anything"),
	testutil.S(errSyntax, "AUTH", "a", "b", "c"),
	testutil.S(arityErr("auth"), "AUTH"),
	testutil.S("-NOPROTO unsupported protocol version", "HELLO", "4"),
	testutil.S("-NOPROTO unsupported protocol version", "HELLO", "1"),
	testutil.S("-ERR Protocol version is not an integer or out of range", "HELLO", "x"),
	testutil.S("-ERR Syntax error in HELLO option 'FOO'", "HELLO", "2", "FOO"),
	testutil.S("-WRONGPASS invalid username-password pair or user is disabled.", "HELLO", "2", "AUTH", "bob", "x"),
	testutil.S("-ERR Client names cannot contain spaces, newlines or special characters.", "HELLO", "2", "SETNAME", "a b"),
	testutil.S("+OK", "SELECT", "1"),
	testutil.S("+OK", "SET", "k", "v"),
	testutil.S("+OK", "SELECT", "0"),
	testutil.S("(nil)", "GET", "k"),
	testutil.S(errClusterDisabled, "READONLY"),
	testutil.S(errClusterDisabled, "READWRITE"),
	testutil.S(arityErr("readonly"), "READONLY", "x"),
}

var connectionClientScript = []testutil.Step{
	testutil.S("(nil)", "CLIENT", "GETNAME"),
	testutil.S("+OK", "CLIENT", "SETNAME", "foo"),
	testutil.S(`"foo"`, "CLIENT", "GETNAME"),
	testutil.S("-ERR Client names cannot contain spaces, newlines or special characters.", "CLIENT", "SETNAME", "a b"),
	testutil.S("+OK", "CLIENT", "SETNAME", ""),
	testutil.S("(nil)", "CLIENT", "GETNAME"),
	testutil.S(arityErr("client|setname"), "CLIENT", "SETNAME"),
	testutil.S("+OK", "CLIENT", "SETINFO", "lib-name", "mylib"),
	testutil.S("+OK", "CLIENT", "SETINFO", "lib-ver", "1.0"),
	testutil.S("-ERR Unrecognized option 'foo'", "CLIENT", "SETINFO", "foo", "x"),
	testutil.S("-ERR lib-name cannot contain spaces, newlines or special characters.", "CLIENT", "SETINFO", "lib-name", "a b"),
	testutil.S(":0", "CLIENT", "KILL", "ID", "999999"),
	testutil.S("-ERR No such client", "CLIENT", "KILL", "1.2.3.4:5"),
	testutil.S("-ERR client-id should be greater than 0", "CLIENT", "KILL", "ID", "0"),
	testutil.S("+OK", "CLIENT", "PAUSE", "10"),
	testutil.S("+OK", "CLIENT", "PAUSE", "10", "WRITE"),
	testutil.S("+OK", "CLIENT", "PAUSE", "10", "ALL"),
	testutil.S("-ERR CLIENT PAUSE mode must be WRITE or ALL", "CLIENT", "PAUSE", "10", "FOO"),
	testutil.S("-ERR timeout is not an integer or out of range", "CLIENT", "PAUSE", "x"),
	testutil.S("+OK", "CLIENT", "UNPAUSE"),
	testutil.S("+OK", "CLIENT", "REPLY", "ON"),
	testutil.S(errSyntax, "CLIENT", "REPLY", "FOO"),
	testutil.S("+OK", "CLIENT", "NO-EVICT", "on"),
	testutil.S("+OK", "CLIENT", "NO-EVICT", "off"),
	testutil.S(errSyntax, "CLIENT", "NO-EVICT", "maybe"),
	testutil.S("+OK", "CLIENT", "NO-TOUCH", "on"),
	testutil.S("+OK", "CLIENT", "NO-TOUCH", "off"),
	testutil.S(errSyntax, "CLIENT", "NO-TOUCH", "maybe"),
	testutil.S(":0", "CLIENT", "UNBLOCK", "999999"),
	testutil.S(":0", "CLIENT", "UNBLOCK", "999999", "ERROR"),
	testutil.S("-ERR CLIENT UNBLOCK reason should be TIMEOUT or ERROR", "CLIENT", "UNBLOCK", "999999", "FOO"),
	testutil.S(errNotInt, "CLIENT", "UNBLOCK", "x"),
	testutil.S("-ERR unknown subcommand 'FOO'. Try CLIENT HELP.", "CLIENT", "FOO"),
	testutil.S(arityErr("client"), "CLIENT"),
}

var connectionResetScript = []testutil.Step{
	testutil.S("+OK", "CLIENT", "SETNAME", "before-reset"),
	testutil.S("+OK", "SELECT", "1"),
	testutil.S("+RESET", "RESET"),
	testutil.S("(nil)", "CLIENT", "GETNAME"),
	testutil.S("(nil)", "GET", "k"),
	testutil.S(arityErr("reset"), "RESET", "x"),
	testutil.S("+OK", "QUIT"),
}

func TestConnectionCommands(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), connectionScript)
}

func TestConnectionCommandsDifferential(t *testing.T) {
	testutil.Differential(t, connectionScript, testutil.KnownDivergences)
}

// TestConnectionCommands_Shapes checks allowlisted replies whose content is
// server-specific (ids, addresses, help text) by their shape only.
func TestConnectionCommands_Shapes(t *testing.T) {
	_, addr := testutil.StartServer(t, nil)
	c := testutil.MustDial(t, addr)
	rec := testutil.RecordCommands(t)
	rec("client")
	rec("hello")
	tests := []struct {
		cmd    []string
		prefix string
	}{
		{[]string{"CLIENT", "ID"}, ":"},
		{[]string{"CLIENT", "INFO"}, `"id=`},
		{[]string{"CLIENT", "LIST"}, `"id=`},
		{[]string{"CLIENT", "LIST", "TYPE", "normal"}, `"id=`},
		{[]string{"CLIENT", "HELP"}, `*[+CLIENT <subcommand>`},
		{[]string{"HELLO", "2"}, `*["server", "redis", "version", `},
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.cmd, " "), func(t *testing.T) {
			if got := c.DoS(tc.cmd...); !strings.HasPrefix(got, tc.prefix) {
				t.Fatalf("got %s, want prefix %s", got, tc.prefix)
			}
		})
	}
	v, err := c.Do("HELLO", "3")
	if err != nil {
		t.Fatalf("HELLO 3: %v", err)
	}
	if v.Type != '%' {
		t.Fatalf("HELLO 3: got %s, want a map", testutil.Render(v, false))
	}
	var keys []string
	for i := 0; i+1 < len(v.Elems); i += 2 {
		keys = append(keys, string(v.Elems[i].Str))
	}
	want := []string{"server", "version", "proto", "id", "mode", "role", "modules"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("HELLO 3 keys: got %v, want %v", keys, want)
	}
	if got := c.DoS("PING"); got != "+PONG" {
		t.Fatalf("PING after HELLO 3: got %s, want +PONG", got)
	}
}

// Redis checks subcommand arity against the full name ("client|getname").
func TestConnectionCommands_ClientGetNameArity(t *testing.T) {
	t.Skip("known bug B8: CLIENT GETNAME x ignores the extra argument (cmd_server.go cmdClient case \"getname\")")
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S(arityErr("client|getname"), "CLIENT", "GETNAME", "x"),
	})
}

// Redis's clientKillCommand rejects an unknown TYPE filter.
func TestConnectionCommands_ClientKillUnknownType(t *testing.T) {
	t.Skip("known bug B9: CLIENT KILL TYPE foo replies :0 instead of an error (cmd_server.go clientKill)")
	_, addr := testutil.StartServer(t, nil)
	testutil.RunScript(t, testutil.MustDial(t, addr), []testutil.Step{
		testutil.S("-ERR Unknown client type 'foo'", "CLIENT", "KILL", "TYPE", "foo"),
	})
}
