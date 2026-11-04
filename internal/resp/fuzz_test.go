package resp

import (
	"bytes"
	"math"
	"reflect"
	"strconv"
	"testing"
)

// encodeValue is a test-only RESP3 encoder for Value, the inverse of
// Reader.ReadValue.
func encodeValue(v Value) []byte {
	var b []byte
	b = append(b, v.Type)
	switch v.Type {
	case '+', '-', '(', ',':
		b = append(b, v.Str...)
		b = append(b, "\r\n"...)
	case ':':
		b = strconv.AppendInt(b, v.Int, 10)
		b = append(b, "\r\n"...)
	case '#':
		if v.Int == 1 {
			b = append(b, 't')
		} else {
			b = append(b, 'f')
		}
		b = append(b, "\r\n"...)
	case '_':
		b = append(b, "\r\n"...)
	case '$', '=', '!':
		if v.Null {
			return append(b, "-1\r\n"...)
		}
		b = strconv.AppendInt(b, int64(len(v.Str)), 10)
		b = append(b, "\r\n"...)
		b = append(b, v.Str...)
		b = append(b, "\r\n"...)
	case '*', '~', '>', '%':
		if v.Null {
			return append(b, "-1\r\n"...)
		}
		n := len(v.Elems)
		if v.Type == '%' {
			n /= 2
		}
		b = strconv.AppendInt(b, int64(n), 10)
		b = append(b, "\r\n"...)
		for _, e := range v.Elems {
			b = append(b, encodeValue(e)...)
		}
	}
	return b
}

// sameValue is reflect.DeepEqual except that NaN doubles compare equal.
func sameValue(a, b Value) bool {
	if len(a.Elems) != len(b.Elems) {
		return false
	}
	for i := range a.Elems {
		if !sameValue(a.Elems[i], b.Elems[i]) {
			return false
		}
	}
	if math.IsNaN(a.Float) && math.IsNaN(b.Float) {
		a.Float, b.Float = 0, 0
	}
	a.Elems, b.Elems = nil, nil
	return reflect.DeepEqual(a, b)
}

func TestEncodeValue_RoundTrip(t *testing.T) {
	for _, s := range valueSeeds {
		v, err := NewReader(bytes.NewReader([]byte(s)), 0).ReadValue()
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		v2, err := NewReader(bytes.NewReader(encodeValue(v)), 0).ReadValue()
		if err != nil || !sameValue(v, v2) {
			t.Errorf("%q: got %+v, %v, want %+v", s, v2, err, v)
		}
	}
}

var valueSeeds = []string{
	"+OK\r\n", "-ERR x\r\n", ":42\r\n", "$3\r\nabc\r\n", "$-1\r\n",
	"*2\r\n:1\r\n+a\r\n", "*-1\r\n", "_\r\n", "#t\r\n", ",1.5\r\n",
	"%1\r\n+k\r\n:1\r\n", "~1\r\n+a\r\n", ">1\r\n+a\r\n",
	"=8\r\ntxt:abcd\r\n", "(123\r\n", "!5\r\nerror\r\n",
}

// TestReadValue_HugeLengthRegression is the fuzz crasher "*99999999999999999991\n0\n":
// ReadValue allocates from an unchecked aggregate or bulk length and panics
// with "makeslice: len out of range" (or exhausts memory). Redis bounds these
// with proto-max-bulk-len and the multibulk limit.
func TestReadValue_HugeLengthRegression(t *testing.T) {
	for _, in := range []string{
		"*99999999999999999991\n0\n", "$99999999999999999\r\n", "*4000000000\r\n",
		"|4000000000\r\n", "%4000000000\r\n", "$3\r\nabcXY", "$3\r\nabc\n\r",
	} {
		if _, err := NewReader(bytes.NewReader([]byte(in)), 0).ReadValue(); err == nil {
			t.Errorf("%q: got nil error, want protocol error", in)
		}
	}
}

func FuzzReadValue(f *testing.F) {
	for _, s := range valueSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := NewReader(bytes.NewReader(data), 0).ReadValue()
		if err != nil {
			return
		}
		v2, err := NewReader(bytes.NewReader(encodeValue(v)), 0).ReadValue()
		if err != nil {
			t.Fatalf("re-read of %q failed: %v", encodeValue(v), err)
		}
		if !sameValue(v, v2) {
			t.Fatalf("round trip mismatch: got %+v, want %+v", v2, v)
		}
	})
}

func FuzzReadCommand(f *testing.F) {
	for _, s := range []string{
		"*2\r\n$3\r\nGET\r\n$1\r\nk\r\n", "GET \"a b\" c\r\n", "\r\n", "*0\r\n",
		"*1\r\n$4\r\nPING\r\n", "*2\r\n$3\r\nGE", "*-1\r\n", "PING\r\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r := NewReader(bytes.NewReader(data), 0)
		r.MaxBulkLen = 1 << 20 // keep fuzz allocations small
		args, err := r.ReadCommand()
		if err != nil || len(args) == 0 {
			return
		}
		got, err := NewReader(bytes.NewReader(AppendCommand(nil, args...)), 0).ReadCommand()
		if err != nil {
			t.Fatalf("re-read failed: %v", err)
		}
		if len(got) != len(args) {
			t.Fatalf("got %d args, want %d", len(got), len(args))
		}
		for i := range args {
			if !bytes.Equal(got[i], args[i]) {
				t.Fatalf("arg %d: got %q, want %q", i, got[i], args[i])
			}
		}
	})
}

// TestReadValue_MaxMultiBulk checks the aggregate limit is the Reader's own
// field, so internal readers can raise it and clients can be held lower.
func TestReadValue_MaxMultiBulk(t *testing.T) {
	in := "*3\r\n:1\r\n:2\r\n:3\r\n"
	r := NewReader(bytes.NewReader([]byte(in)), 0)
	r.MaxMultiBulk = 2
	if _, err := r.ReadValue(); err == nil {
		t.Error("limit 2: got nil error, want protocol error")
	}
	r = NewReader(bytes.NewReader([]byte(in)), 0)
	r.MaxMultiBulk = 3
	if v, err := r.ReadValue(); err != nil || len(v.Elems) != 3 {
		t.Errorf("limit 3: got %d elems, %v; want 3, nil", len(v.Elems), err)
	}
}
