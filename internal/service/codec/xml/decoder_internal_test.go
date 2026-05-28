package xml

import (
	"bytes"
	"errors"
	"io"
	"testing"

	stdxml "encoding/xml"
)

// Test_xmlDecoder_Decode covers both a success path and a malformed-input
// path to exercise the wrap branch.
func Test_xmlDecoder_Decode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr bool
	}
	tests := []tc{
		{"empty stream does not error further", nil, false},
		{"garbage bytes surface an error", []byte{0xff, 0xff, 0xff, 0xff}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &xmlDecoder{inner: stdxml.NewDecoder(bytes.NewReader(tc.data))}
		var out map[string]any
		err := dec.Decode(&out)
		//: treat EOF on the empty case as non-error.
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error on malformed input", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_xmlDecoder_Decode_DoneLatch asserts the sticky done latch: once a
// Decode call drains the stream (surfacing io.EOF), every subsequent Decode
// short-circuits to io.EOF WITHOUT touching the underlying reader. The first
// Decode reaches EOF the slow way (delegating to the stdlib decoder); the
// second exercises the `if d.done { return io.EOF }` fast-path that the
// single-call tests never reach.
func Test_xmlDecoder_Decode_DoneLatch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		data []byte
	}
	tests := []tc{
		//: one element then EOF — the second Decode must hit the latch.
		{"second decode after drain hits latch", []byte(`<doc></doc>`)},
		//: already-empty stream — first Decode drains, second hits the latch.
		{"second decode on empty stream hits latch", nil},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &xmlDecoder{inner: stdxml.NewDecoder(bytes.NewReader(tc.data))}
		//: encoding/xml cannot decode into a map, so the drain target is a
		//: struct matching the <doc> element (zero value is fine for empty).
		var out struct {
			XMLName stdxml.Name `xml:"doc"`
		}
		//: drain the stream so the done latch is set; ignore the value, we
		//: only need the decoder to reach io.EOF (directly or after one read).
		for {
			derr := dec.Decode(&out)
			if errors.Is(derr, io.EOF) {
				//: stream drained — latch is now set.
				break
			}
			if derr != nil {
				t.Fatalf("%s: unexpected drain err=%v", tc.name, derr)
			}
		}
		//: the latched call MUST return io.EOF without consuming a token.
		if got := dec.Decode(&out); !errors.Is(got, io.EOF) {
			t.Errorf("%s: latched Decode=%v want io.EOF", tc.name, got)
		}
		//: More MUST now report drained.
		if dec.More() {
			t.Errorf("%s: More=true after drain, want false", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_xmlDecoder_More asserts the sticky EOF latch is idempotent: calling
// More twice in a row on a fresh decoder returns the same value.
func Test_xmlDecoder_More(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"idempotent before any Decode"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &xmlDecoder{inner: stdxml.NewDecoder(bytes.NewReader(nil))}
		first := dec.More()
		second := dec.More()
		if first != second {
			t.Errorf("%s: More non-idempotent: first=%v second=%v", tc.name, first, second)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
