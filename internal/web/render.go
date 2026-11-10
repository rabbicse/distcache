// This file converts RESP replies into a view model for the reply.html
// fragment: kind-tagged text, hex for binary payloads, and size caps so one
// large reply cannot flood the browser.
package web

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/rabbicse/distcache/internal/resp"
)

const (
	maxReplyBytes = 64 << 10 // longest bulk payload shown in full
	maxReplyItems = 1000     // most array/map elements shown
)

// replyView is the display form of one RESP value.
type replyView struct {
	// Kind is one of status, error, int, bulk, nil, array, map, double, bool
	// or verbatim.
	Kind string
	// Text is the rendered scalar; for Binary values it is space-separated hex.
	Text string
	// Binary marks payloads that are not printable text.
	Binary bool
	// Items are the elements of an array or map (maps alternate key, value).
	Items []replyView
}

// Pairs returns a map reply's items as key/value pairs for templates.
func (v replyView) Pairs() [][2]replyView {
	pairs := make([][2]replyView, 0, len(v.Items)/2)
	for i := 0; i+1 < len(v.Items); i += 2 {
		pairs = append(pairs, [2]replyView{v.Items[i], v.Items[i+1]})
	}
	return pairs
}

// viewOf converts a RESP value into its view model.
func viewOf(v resp.Value) replyView {
	switch v.Type {
	case '+':
		return replyView{Kind: "status", Text: string(v.Str)}
	case '-', '!':
		return replyView{Kind: "error", Text: string(v.Str)}
	case ':':
		return replyView{Kind: "int", Text: strconv.FormatInt(v.Int, 10)}
	case '(':
		return replyView{Kind: "int", Text: string(v.Str)}
	case ',':
		return doubleView(v)
	case '#':
		return replyView{Kind: "bool", Text: strconv.FormatBool(v.Int == 1)}
	case '_':
		return nilView()
	case '$':
		if v.Null {
			return nilView()
		}
		return bytesView("bulk", v.Str)
	case '=':
		if v.Null {
			return nilView()
		}
		// Verbatim strings carry a three-letter format and a colon.
		body := v.Str
		if len(body) >= 4 && body[3] == ':' {
			body = body[4:]
		}
		return bytesView("verbatim", body)
	case '*', '~', '>', '%':
		return aggregateView(v)
	}
	return replyView{Kind: "error", Text: fmt.Sprintf("unsupported reply type %q", v.Type)}
}

func nilView() replyView { return replyView{Kind: "nil", Text: "(nil)"} }

func doubleView(v resp.Value) replyView {
	text := string(v.Str)
	if text == "" {
		text = strconv.FormatFloat(v.Float, 'g', -1, 64)
	}
	return replyView{Kind: "double", Text: text}
}

func aggregateView(v resp.Value) replyView {
	if v.Null {
		return nilView()
	}
	kind := "array"
	if v.Type == '%' {
		kind = "map"
	}
	out := replyView{Kind: kind}
	n := len(v.Elems)
	if n > maxReplyItems {
		n = maxReplyItems
	}
	out.Items = make([]replyView, 0, n+1)
	for _, e := range v.Elems[:n] {
		out.Items = append(out.Items, viewOf(e))
	}
	if more := len(v.Elems) - n; more > 0 {
		out.Items = append(out.Items, replyView{Kind: "status", Text: fmt.Sprintf("... %d more elements not shown", more)})
	}
	return out
}

// bytesView renders a byte payload as text, or as hex when it is not
// printable, truncating long payloads.
func bytesView(kind string, b []byte) replyView {
	extra := 0
	if len(b) > maxReplyBytes {
		extra = len(b) - maxReplyBytes
		b = b[:maxReplyBytes]
	}
	out := replyView{Kind: kind}
	if isPrintable(b, extra > 0) {
		out.Text = string(b)
	} else {
		out.Binary = true
		out.Text = fmt.Sprintf("% x", b)
	}
	if extra > 0 {
		out.Text += fmt.Sprintf(" ... (%d more bytes)", extra)
	}
	return out
}

// isPrintable reports whether b is valid UTF-8 without control characters
// other than tab and newlines. A truncated payload may end mid-rune, so up to
// three trailing bytes are ignored when cut is true.
func isPrintable(b []byte, cut bool) bool {
	if cut {
		for i := 0; i < 3 && len(b) > 0 && !utf8.Valid(b); i++ {
			b = b[:len(b)-1]
		}
	}
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0x7f {
			return false
		}
	}
	return true
}
