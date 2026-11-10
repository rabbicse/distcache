// This file implements the interactive command console: command-line
// parsing with redis-cli quoting, the deny list for commands that cannot work
// over a stateless HTTP exchange, execution on the browser session's
// in-process client, command-name autocomplete from COMMAND DOCS, and the
// sample-data seed.
package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/rabbicse/distcache/internal/resp"
	"github.com/rabbicse/distcache/internal/server"
)

const (
	maxHistory      = 100
	maxCommandBytes = 64 << 10
	maxSuggestions  = 10
)

// deniedCommands maps a command (or "CLIENT REPLY") to the reason the console
// refuses it. Blocking commands appear here too but are refused only when
// their timeout is zero (see zeroTimeout).
var deniedCommands = map[string]string{
	"SUBSCRIBE":       "subscriptions need a long-lived connection; use the Pub/Sub page",
	"PSUBSCRIBE":      "subscriptions need a long-lived connection; use the Pub/Sub page",
	"SSUBSCRIBE":      "subscriptions need a long-lived connection; use the Pub/Sub page",
	"MONITOR":         "MONITOR streams every command and cannot run in the console",
	"QUIT":            "the console session is managed by the browser; use Logout",
	"RESET":           "RESET would discard the console session state",
	"CLIENT REPLY":    "turning replies off would silence the console",
	"CLIENT TRACKING": "tracking pushes invalidation frames that would misalign replies",
	"HELLO":           "the console manages its own protocol and user; use Logout to change user",
	"AUTH":            "the console session is authenticated by the web login",
	"SYNC":            "replication handshakes need a dedicated connection",
	"PSYNC":           "replication handshakes need a dedicated connection",
	"REPLCONF":        "replication handshakes need a dedicated connection",
	"SHUTDOWN":        "stopping the server from the console is disabled",
	"DEBUG":           "DEBUG can crash or stall the server and is disabled in the console",
	"BLPOP":           "blocks forever with a zero timeout; use a non-zero timeout",
	"BRPOP":           "blocks forever with a zero timeout; use a non-zero timeout",
	"BLMOVE":          "blocks forever with a zero timeout; use a non-zero timeout",
	"BRPOPLPUSH":      "blocks forever with a zero timeout; use a non-zero timeout",
	"BZPOPMIN":        "blocks forever with a zero timeout; use a non-zero timeout",
	"BZPOPMAX":        "blocks forever with a zero timeout; use a non-zero timeout",
	"BLMPOP":          "blocks forever with a zero timeout; use a non-zero timeout",
	"BZMPOP":          "blocks forever with a zero timeout; use a non-zero timeout",
	"XREAD":           "BLOCK 0 waits forever; use a non-zero BLOCK time",
	"XREADGROUP":      "BLOCK 0 waits forever; use a non-zero BLOCK time",
}

// maxBlockSeconds caps blocking timeouts: the session lock is held for the
// whole wait, so a long block would freeze the browser session.
const maxBlockSeconds = 30

// timeoutSeconds extracts, per blocking command, the wait in seconds.
var timeoutSeconds = map[string]func(args []string) (float64, bool){
	"BLPOP":      lastArgSecs,
	"BRPOP":      lastArgSecs,
	"BLMOVE":     lastArgSecs,
	"BRPOPLPUSH": lastArgSecs,
	"BZPOPMIN":   lastArgSecs,
	"BZPOPMAX":   lastArgSecs,
	"BLMPOP":     func(a []string) (float64, bool) { return secs(a, 1, 1) },
	"BZMPOP":     func(a []string) (float64, bool) { return secs(a, 1, 1) },
	"XREAD":      blockSecs,
	"XREADGROUP": blockSecs,
}

func secs(a []string, i int, perUnit float64) (float64, bool) {
	if i >= len(a) {
		return 0, false
	}
	f, err := strconv.ParseFloat(a[i], 64)
	if err != nil {
		return 0, false
	}
	return f / perUnit, true
}

func lastArgSecs(a []string) (float64, bool) { return secs(a, len(a)-1, 1) }

// blockSecs finds BLOCK <ms> among the XREAD/XREADGROUP options. Option
// values (COUNT n, GROUP group consumer) are skipped so names equal to BLOCK
// or STREAMS are not misread; the options end at STREAMS.
func blockSecs(a []string) (float64, bool) {
	for i := 1; i < len(a); {
		switch strings.ToUpper(a[i]) {
		case "STREAMS":
			return 0, false
		case "BLOCK":
			return secs(a, i+1, 1000)
		case "GROUP":
			i += 3
		case "COUNT":
			i += 2
		default:
			i++
		}
	}
	return 0, false
}

// denyReason reports why the console refuses args, if it does.
func denyReason(args []string) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	name := strings.ToUpper(args[0])
	if name == "CLIENT" && len(args) > 1 {
		name += " " + strings.ToUpper(args[1])
	}
	reason, ok := deniedCommands[name]
	if !ok {
		return "", false
	}
	extract, blocking := timeoutSeconds[name]
	if !blocking {
		return reason, true
	}
	t, known := extract(args)
	switch {
	case !known:
		return "", false // malformed; the handler rejects it
	case t == 0:
		return reason, true
	case t > maxBlockSeconds:
		return fmt.Sprintf("a wait over %d seconds would freeze the console session", maxBlockSeconds), true
	}
	return "", false
}

// parseCommandLine splits line with redis-cli quoting: double quotes allow
// \n \r \t \b \a \xHH and backslash escapes, single quotes allow only \', and
// a closing quote must be followed by whitespace or the end of the line.
func parseCommandLine(line string) ([]string, error) {
	var args []string
	for i := 0; ; {
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			return args, nil
		}
		arg, next, err := scanArg(line, i)
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		i = next
	}
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// scanArg reads one argument starting at line[i] and returns it with the
// index just past it.
func scanArg(line string, i int) (string, int, error) {
	var sb strings.Builder
	var quote byte // 0, '"' or '\''
	for ; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == '"' && c == '\\' && i+1 < len(line):
			i = doubleQuoteEscape(&sb, line, i+1)
		case quote == '\'' && c == '\\' && i+1 < len(line) && line[i+1] == '\'':
			sb.WriteByte('\'')
			i++
		case quote != 0 && c == quote:
			if i+1 < len(line) && !isSpace(line[i+1]) {
				return "", 0, errors.New("closing quote must be followed by a space")
			}
			return sb.String(), i + 1, nil
		case quote != 0:
			sb.WriteByte(c)
		case isSpace(c):
			return sb.String(), i, nil
		case c == '"' || c == '\'':
			quote = c
		default:
			sb.WriteByte(c)
		}
	}
	if quote != 0 {
		return "", 0, errors.New("unbalanced quotes in command line")
	}
	return sb.String(), i, nil
}

// doubleQuoteEscape decodes the escape whose character is at line[i] and
// returns the index of its last byte.
func doubleQuoteEscape(sb *strings.Builder, line string, i int) int {
	if line[i] == 'x' && i+2 < len(line) && isHex(line[i+1]) && isHex(line[i+2]) {
		n, _ := strconv.ParseUint(line[i+1:i+3], 16, 8) // both digits verified as hex
		sb.WriteByte(byte(n))
		return i + 2
	}
	switch line[i] {
	case 'n':
		sb.WriteByte('\n')
	case 'r':
		sb.WriteByte('\r')
	case 't':
		sb.WriteByte('\t')
	case 'b':
		sb.WriteByte('\b')
	case 'a':
		sb.WriteByte('\a')
	default:
		sb.WriteByte(line[i])
	}
	return i
}

func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

// replyEntry is the data for the reply.html fragment: the echoed command and
// its rendered reply.
type replyEntry struct {
	Cmd   string
	Reply replyView
	// Hist is the (redacted) line recorded in history, empty when the line
	// was not recorded; console.js mirrors it into its client-side history.
	Hist string
	// InMulti and Queued describe an open transaction on the session.
	InMulti bool
	Queued  int
}

func errorEntry(cmd, msg string) replyEntry {
	return replyEntry{Cmd: cmd, Reply: replyView{Kind: "error", Text: msg}}
}

// writeEntry renders one reply fragment.
// It annotates the entry with the session's transaction state.
func (c *Console) writeEntry(w http.ResponseWriter, s *session, e replyEntry) {
	s.mu.Lock()
	e.InMulti, e.Queued = s.inMulti, s.queued
	s.mu.Unlock()
	c.renderFragment(w, "reply", e)
}

// handleExec runs the command in form field "cmd" on the browser session.
func (c *Console) handleExec(w http.ResponseWriter, r *http.Request) {
	s := sessionOf(r)
	r.Body = http.MaxBytesReader(w, r.Body, maxCommandBytes)
	if err := r.ParseForm(); err != nil {
		c.writeEntry(w, s, errorEntry("", "command too large"))
		return
	}
	line := strings.TrimSpace(r.PostFormValue("cmd"))
	if line == "" {
		return
	}
	args, err := parseCommandLine(line)
	if err != nil {
		c.writeEntry(w, s, errorEntry(line, "parse error: "+err.Error()))
		return
	}
	if len(args) == 0 {
		return
	}
	shown := redactLine(args, line)
	if reason, bad := denyReason(args); bad {
		c.writeEntry(w, s, errorEntry(shown, fmt.Sprintf("%s is not allowed in the console: %s", strings.ToUpper(args[0]), reason)))
		return
	}
	if s.db == nil {
		c.writeEntry(w, s, errorEntry(shown, "console has no server attached"))
		return
	}
	keys := c.srv.CommandKeyCount(args)
	s.mu.Lock()
	v, err := s.db.Exec(args...)
	s.pushHistory(shown)
	s.trackMulti(args[0], v, err)
	s.mu.Unlock()
	c.auditCommand(r, s, args[0], keys)
	switch {
	case errors.Is(err, server.ErrWouldBlock):
		c.writeEntry(w, s, errorEntry(shown, "command is not allowed in the console: it would block forever"))
	case err != nil:
		c.log.Warn("web cmd exec", "err", err)
		c.writeEntry(w, s, errorEntry(shown, "internal error running the command"))
	default:
		c.writeEntry(w, s, replyEntry{Cmd: shown, Reply: viewOf(v), Hist: shown})
	}
}

// redactLine returns line as shown and stored: commands that carry
// credentials keep only their name.
func redactLine(args []string, line string) string {
	name := strings.ToUpper(args[0])
	switch name {
	case "AUTH", "HELLO":
		return name + " ...(redacted)"
	case "CONFIG":
		if len(args) > 2 && strings.EqualFold(args[1], "SET") {
			for _, k := range args[2:] {
				switch strings.ToLower(k) {
				case "requirepass", "masterauth", "masteruser":
					return "CONFIG SET ...(redacted)"
				}
			}
		}
	}
	return line
}

// trackMulti follows the session's transaction from the replies. The caller
// holds s.mu.
func (s *session) trackMulti(name string, v resp.Value, err error) {
	if err != nil {
		return
	}
	switch strings.ToUpper(name) {
	case "MULTI":
		if v.Type != '-' {
			s.inMulti, s.queued = true, 0
		}
	case "EXEC", "DISCARD":
		if s.inMulti {
			s.inMulti, s.queued = false, 0
		}
	default:
		if s.inMulti && v.Type == '+' && string(v.Str) == "QUEUED" {
			s.queued++
		}
	}
}

// auditCommand logs the command name and key count, never the arguments.
func (c *Console) auditCommand(r *http.Request, s *session, name string, keys int) {
	if canon := c.srv.CommandName(name); canon != "" {
		name = strings.ToUpper(canon)
	} else if !strings.HasPrefix(name, "SAMPLE-") {
		name = "unknown" // never log arbitrary first tokens such as pasted secrets
	}
	c.log.Info("web cmd", "user", s.user, "ip", clientIP(r, c.trusted).String(), "cmd", name, "keys", keys)
}

// pushHistory records line as the newest history entry. The caller holds
// s.mu.
func (s *session) pushHistory(line string) {
	s.history = slices.Insert(s.history, 0, line)
	if len(s.history) > maxHistory {
		s.history = s.history[:maxHistory]
	}
}

// historyCopy returns the session history, newest first.
func (s *session) historyCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.history)
}

// handleConsolePage renders the console page with samples and history.
func (c *Console) handleConsolePage(w http.ResponseWriter, r *http.Request) {
	p := page{Title: "Console", Active: "console", Insecure: c.insecure(), Samples: sampleGroups}
	if s := sessionOf(r); s != nil {
		p.CSRF, p.HXHeaders, p.History = s.csrf, hxHeaders(s.csrf), s.historyCopy()
	}
	c.render(w, http.StatusOK, "layout.html", p)
}

// handleSeed creates the sample keys, refusing when any sample:* key exists
// or a transaction is open (the seed would be queued, not run).
func (c *Console) handleSeed(w http.ResponseWriter, r *http.Request) {
	const label = "Load sample data"
	s := sessionOf(r)
	if s.db == nil {
		c.writeEntry(w, s, errorEntry(label, "console has no server attached"))
		return
	}
	s.mu.Lock()
	var entry replyEntry
	if s.inMulti {
		entry = errorEntry("", "a transaction is open on this session; run EXEC or DISCARD before loading sample data")
	} else {
		entry = seedSamples(s.db)
	}
	s.mu.Unlock()
	if entry.Reply.Kind != "error" {
		c.auditCommand(r, s, "SAMPLE-SEED", len(seedScript))
	}
	entry.Cmd = label
	c.writeEntry(w, s, entry)
}

// seedSamples runs the seed script on db, whose lock the caller holds.
func seedSamples(db *server.Session) replyEntry {
	exists, err := sampleKeysExist(db)
	if err != nil {
		return errorEntry("", "could not check for existing sample keys: "+err.Error())
	}
	if exists {
		return errorEntry("", "sample:* keys already exist; delete them before loading the sample data again")
	}
	for _, args := range seedScript {
		v, err := db.Exec(args...)
		if err != nil {
			return errorEntry("", "seed failed: "+err.Error())
		}
		if v.Type == '-' {
			return errorEntry("", fmt.Sprintf("seed failed at %s: %s", args[0], v.Str))
		}
	}
	return replyEntry{Reply: replyView{Kind: "status", Text: fmt.Sprintf("Loaded %d sample keys (prefix %s)", len(seedScript), sampleKeyPrefix)}}
}

// sampleKeysExist walks SCAN until it finds a sample:* key or the cursor
// returns to zero.
func sampleKeysExist(db *server.Session) (bool, error) {
	cursor := "0"
	for {
		v, err := db.Exec("SCAN", cursor, "MATCH", sampleKeyPrefix+"*", "COUNT", "1000")
		if err != nil {
			return false, err
		}
		if v.Type != '*' || len(v.Elems) != 2 {
			return false, errors.New("unexpected SCAN reply")
		}
		if len(v.Elems[1].Elems) > 0 {
			return true, nil
		}
		cursor = string(v.Elems[0].Str)
		if cursor == "0" {
			return false, nil
		}
	}
}

// cmdDoc is one command name with its one-line summary.
type cmdDoc struct {
	Name    string // upper case
	Summary string
}

// docsCache holds the command list built once from COMMAND DOCS.
type docsCache struct {
	once sync.Once
	list []cmdDoc // sorted by Name; written once under once
}

// commandDocs returns the cached, sorted command list.
func (c *Console) commandDocs() []cmdDoc {
	c.docs.once.Do(func() { c.docs.list = loadDocs(c.srv) })
	return c.docs.list
}

// loadDocs queries COMMAND DOCS on a short-lived session. Container
// subcommands ("client|id") are skipped.
func loadDocs(srv *server.Server) []cmdDoc {
	if srv == nil {
		return nil
	}
	ss := srv.NewSession()
	defer ss.Close()
	v, err := ss.Exec("COMMAND", "DOCS")
	if err != nil || len(v.Elems)%2 != 0 {
		return nil
	}
	docs := make([]cmdDoc, 0, len(v.Elems)/2)
	for i := 0; i+1 < len(v.Elems); i += 2 {
		name := string(v.Elems[i].Str)
		if strings.Contains(name, "|") {
			continue
		}
		docs = append(docs, cmdDoc{Name: strings.ToUpper(name), Summary: docSummary(v.Elems[i+1])})
	}
	slices.SortFunc(docs, func(a, b cmdDoc) int { return strings.Compare(a.Name, b.Name) })
	return docs
}

// docSummary extracts the "summary" field from one COMMAND DOCS entry, which
// is a flat key/value list (an array in RESP2, a map in RESP3).
func docSummary(v resp.Value) string {
	for i := 0; i+1 < len(v.Elems); i += 2 {
		if string(v.Elems[i].Str) == "summary" {
			return string(v.Elems[i+1].Str)
		}
	}
	return ""
}

// completion is the data for the complete.html fragment.
type completion struct {
	Exact   string // command whose hint is shown
	Hint    string
	Matches []cmdDoc
}

// handleComplete suggests command names for the first word of q (or of the
// input field "cmd", which is what htmx sends from the console input) and
// shows the syntax hint of the best match.
func (c *Console) handleComplete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		q = r.URL.Query().Get("cmd")
	}
	c.renderFragment(w, "complete", completeFor(c.commandDocs(), q))
}

func completeFor(docs []cmdDoc, q string) completion {
	q = strings.TrimLeftFunc(q, unicode.IsSpace)
	word, _, typedArgs := strings.Cut(q, " ") // arguments are not completed
	word = strings.ToUpper(word)
	if word == "" {
		return completion{}
	}
	var out completion
	for _, d := range docs {
		if d.Name == word {
			out.Exact, out.Hint = d.Name, d.Summary
		}
		if !typedArgs && strings.HasPrefix(d.Name, word) && len(out.Matches) < maxSuggestions {
			out.Matches = append(out.Matches, d)
		}
	}
	if out.Exact == "" && len(out.Matches) > 0 {
		out.Exact, out.Hint = out.Matches[0].Name, out.Matches[0].Summary
	}
	return out
}

// renderFragment executes a fragment template without the page layout.
func (c *Console) renderFragment(w http.ResponseWriter, name string, data any) {
	var sb strings.Builder
	if err := c.tmpl.ExecuteTemplate(&sb, name, data); err != nil {
		c.log.Error("render fragment", "name", name, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(sb.String())) // client write errors are not actionable
}
