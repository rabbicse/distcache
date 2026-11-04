package resp

import (
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
)

func newTestReader(s string) *Reader { return NewReader(strings.NewReader(s), 0) }

func TestReadCommand_Multibulk(t *testing.T) {
	r := newTestReader("*2\r\n$3\r\nGET\r\n$1\r\nk\r\n")
	args, err := r.ReadCommand()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{[]byte("GET"), []byte("k")}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("got %q, want %q", args, want)
	}
}

func TestReadCommand_Inline(t *testing.T) {
	r := newTestReader("GET \"a b\" c\r\n")
	args, err := r.ReadCommand()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{[]byte("GET"), []byte("a b"), []byte("c")}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("got %q, want %q", args, want)
	}
}

func TestReadCommand_EmptyAndErrors(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		max     int64
		wantErr string // substring of ProtocolError message; "" means no error
		wantEOF error
	}{
		{name: "empty line", in: "\r\n"},
		{name: "empty multibulk", in: "*0\r\n"},
		{name: "negative multibulk", in: "*-1\r\n"},
		{name: "bulk too large", in: "*1\r\n$5\r\nhello\r\n", max: 4, wantErr: "invalid bulk length"},
		{name: "multibulk too large", in: "*2000000\r\n", wantErr: "invalid multibulk length"},
		{name: "bad multibulk length", in: "*x\r\n", wantErr: "invalid multibulk length"},
		{name: "negative bulk length", in: "*1\r\n$-1\r\n", wantErr: "invalid bulk length"},
		{name: "missing dollar", in: "*1\r\n:3\r\n", wantErr: "expected '$'"},
		{name: "unbalanced quotes", in: "GET \"abc\r\n", wantErr: "unbalanced quotes"},
		{name: "truncated bulk", in: "*2\r\n$3\r\nGE", wantEOF: io.ErrUnexpectedEOF},
		{name: "eof", in: "", wantEOF: io.EOF},
		{name: "truncated header", in: "*2\r\n", wantEOF: io.EOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestReader(tc.in)
			if tc.max > 0 {
				r.MaxBulkLen = tc.max
			}
			args, err := r.ReadCommand()
			switch {
			case tc.wantErr != "":
				var pe *ProtocolError
				if !errors.As(err, &pe) || !strings.Contains(pe.Msg, tc.wantErr) {
					t.Fatalf("got err %v, want ProtocolError containing %q", err, tc.wantErr)
				}
			case tc.wantEOF != nil:
				if !errors.Is(err, tc.wantEOF) {
					t.Fatalf("got err %v, want %v", err, tc.wantEOF)
				}
			default:
				if err != nil || len(args) != 0 {
					t.Fatalf("got %q, %v, want no args, nil", args, err)
				}
			}
		})
	}
}

func TestProtocolError_Error(t *testing.T) {
	if got, want := (&ProtocolError{"x"}).Error(), "Protocol error: x"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadCommand_LongInline(t *testing.T) {
	arg := strings.Repeat("a", 40*1024)
	r := NewReader(strings.NewReader("SET k "+arg+"\r\n"), 4096)
	args, err := r.ReadCommand()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 3 || string(args[2]) != arg {
		t.Errorf("got %d args, want 3 with 40KiB value", len(args))
	}

	r = NewReader(strings.NewReader(strings.Repeat("a", 200*1024)+"\r\n"), 4096)
	_, err = r.ReadCommand()
	var pe *ProtocolError
	if !errors.As(err, &pe) || !strings.Contains(pe.Msg, "too big inline request") {
		t.Errorf("got %v, want too big inline request", err)
	}
}

func TestReader_Pipelined(t *testing.T) {
	r := newTestReader("*1\r\n$4\r\nPING\r\nPING\r\n")
	for i := range 2 {
		args, err := r.ReadCommand()
		if err != nil || len(args) != 1 || string(args[0]) != "PING" {
			t.Fatalf("command %d: got %q, %v", i, args, err)
		}
	}
	if r.Buffered() != 0 {
		t.Errorf("got %d buffered, want 0", r.Buffered())
	}
}

func TestReader_RawAndLine(t *testing.T) {
	r := newTestReader("hello\r\nabcdef")
	line, err := r.ReadLine()
	if err != nil || line != "hello" {
		t.Fatalf("got %q, %v, want hello", line, err)
	}
	raw, err := r.ReadRaw(3)
	if err != nil || string(raw) != "abc" {
		t.Fatalf("got %q, %v, want abc", raw, err)
	}
	b, err := r.Underlying().ReadByte()
	if err != nil || b != 'd' {
		t.Errorf("got %q, %v, want d", b, err)
	}
	if _, err := r.ReadRaw(10); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("got %v, want ErrUnexpectedEOF", err)
	}
}

func TestReadValue_AllTypes(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		typ   byte
		str   string
		i     int64
		f     float64
		null  bool
		elems int
	}{
		{name: "simple", in: "+OK\r\n", typ: '+', str: "OK"},
		{name: "error", in: "-ERR x\r\n", typ: '-', str: "ERR x"},
		{name: "integer", in: ":42\r\n", typ: ':', i: 42},
		{name: "negative integer", in: ":-7\r\n", typ: ':', i: -7},
		{name: "bulk", in: "$3\r\nabc\r\n", typ: '$', str: "abc"},
		{name: "empty bulk", in: "$0\r\n\r\n", typ: '$'},
		{name: "null bulk", in: "$-1\r\n", typ: '$', null: true},
		{name: "array", in: "*2\r\n:1\r\n+a\r\n", typ: '*', elems: 2},
		{name: "null array", in: "*-1\r\n", typ: '*', null: true},
		{name: "null", in: "_\r\n", typ: '_', null: true},
		{name: "bool true", in: "#t\r\n", typ: '#', i: 1},
		{name: "bool false", in: "#f\r\n", typ: '#'},
		{name: "double", in: ",1.5\r\n", typ: ',', str: "1.5", f: 1.5},
		{name: "double inf", in: ",inf\r\n", typ: ',', str: "inf", f: math.Inf(1)},
		{name: "double -inf", in: ",-inf\r\n", typ: ',', str: "-inf", f: math.Inf(-1)},
		{name: "map", in: "%1\r\n+k\r\n:1\r\n", typ: '%', elems: 2},
		{name: "set", in: "~1\r\n+a\r\n", typ: '~', elems: 1},
		{name: "push", in: ">1\r\n+a\r\n", typ: '>', elems: 1},
		{name: "verbatim", in: "=8\r\ntxt:abcd\r\n", typ: '=', str: "txt:abcd"},
		{name: "big number", in: "(123\r\n", typ: '(', str: "123"},
		{name: "bulk error", in: "!5\r\nerror\r\n", typ: '!', str: "error"},
		{name: "attribute skipped", in: "|1\r\n+k\r\n+v\r\n:5\r\n", typ: ':', i: 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := newTestReader(tc.in).ReadValue()
			if err != nil {
				t.Fatal(err)
			}
			if v.Type != tc.typ || string(v.Str) != tc.str || v.Int != tc.i ||
				v.Float != tc.f || v.Null != tc.null || len(v.Elems) != tc.elems {
				t.Errorf("got %+v, want type %c str %q int %d float %v null %v elems %d",
					v, tc.typ, tc.str, tc.i, tc.f, tc.null, tc.elems)
			}
		})
	}
}

func TestReadValue_Errors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error // sentinel, or nil to require *ProtocolError
	}{
		{"unknown marker", "?x\r\n", ErrUnexpected},
		{"bad integer", ":abc\r\n", nil},
		{"bad double", ",abc\r\n", nil},
		{"bad bulk length", "$x\r\n", nil},
		{"bad aggregate length", "*x\r\n", nil},
		{"truncated bulk", "$5\r\nab", io.ErrUnexpectedEOF},
		{"truncated array", "*2\r\n:1\r\n", io.EOF},
		{"eof", "", io.EOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newTestReader(tc.in).ReadValue()
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Errorf("got %v, want %v", err, tc.want)
				}
				return
			}
			var pe *ProtocolError
			if !errors.As(err, &pe) {
				t.Errorf("got %v, want *ProtocolError", err)
			}
		})
	}
}

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   []string
		wantOK bool
	}{
		{"plain", "a b  c", []string{"a", "b", "c"}, true},
		{"empty", "   ", nil, true},
		{"double quotes", `GET "a b" c`, []string{"GET", "a b", "c"}, true},
		{"single quotes", `SET 'a b' c`, []string{"SET", "a b", "c"}, true},
		{"hex escape", `"\x41\x42"`, []string{"AB"}, true},
		{"newline escapes", `"a\nb\r\t\b\a"`, []string{"a\nb\r\t\b\a"}, true},
		{"unknown escape", `"\q"`, []string{"q"}, true},
		{"escaped double quote", `"a\"b"`, []string{`a"b`}, true},
		{"escaped single quote", `'a\'b'`, []string{"a'b"}, true},
		{"empty quoted", `"" x`, []string{"", "x"}, true},
		{"unbalanced double", `"abc`, nil, false},
		{"unbalanced single", `'abc`, nil, false},
		{"closing quote followed by text", `"a"b`, nil, false},
		{"closing single quote followed by text", `'a'b`, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SplitArgs(tc.in)
			if ok != tc.wantOK || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, %v, want %q, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestParseFloat(t *testing.T) {
	for _, s := range []string{"inf", "+inf", "Inf", "+Inf", "INF", "+INF"} {
		if f, err := ParseFloat(s); err != nil || !math.IsInf(f, 1) {
			t.Errorf("%s: got %v, %v, want +Inf", s, f, err)
		}
	}
	for _, s := range []string{"-inf", "-Inf", "-INF"} {
		if f, err := ParseFloat(s); err != nil || !math.IsInf(f, -1) {
			t.Errorf("%s: got %v, %v, want -Inf", s, f, err)
		}
	}
	if f, err := ParseFloat("2.5"); err != nil || f != 2.5 {
		t.Errorf("got %v, %v, want 2.5", f, err)
	}
	if _, err := ParseFloat("x"); err == nil {
		t.Error("got nil error, want failure")
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true}, {"123", 123, true}, {"-5", -5, true},
		{"", 0, false}, {"-", 0, false}, {"1a", 0, false}, {"123456789012345678901", 0, false},
	}
	for _, tc := range tests {
		got, ok := parseInt([]byte(tc.in))
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q: got %d, %v, want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
