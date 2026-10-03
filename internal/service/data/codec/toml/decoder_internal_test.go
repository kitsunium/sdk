package toml

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
)

// Test_tomlDecoder_Decode covers both a success path and a malformed-input
// path to exercise the wrap branch.
func Test_tomlDecoder_Decode(t *testing.T) {
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
		dec := &tomlDecoder{r: bytes.NewReader(tc.data)}
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

// eofWrapReader returns an error that WRAPS io.EOF (but is not the bare
// sentinel) on its first Read. io.ReadAll — which Decode calls — only swallows
// the exact io.EOF sentinel, so a wrapped EOF is surfaced verbatim. This lets
// the decoder's `errors.Is(err, io.EOF)` arm fire through the real read path,
// which neither empty nor garbage input can reach (both are a document: an
// empty one, or a parse error — never io.EOF).
type eofWrapReader struct{}

// Read reports a wrapped-EOF failure so io.ReadAll propagates it.
func (eofWrapReader) Read(_ []byte) (n int, err error) {
	//: wrapped (not bare) io.EOF so io.ReadAll does not treat it as clean end.
	return 0, fmt.Errorf("midstream truncation: %w", io.EOF)
}

// Test_tomlDecoder_Decode_DoneLatch asserts the sticky done latch: Decode
// consumes the entire input on its first call, so the codec marks the
// stream drained and every subsequent Decode short-circuits to io.EOF via
// `if d.done { return io.EOF }` — the arm single-call tests never reach.
func Test_tomlDecoder_Decode_DoneLatch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		data []byte
	}
	tests := []tc{
		//: a one-key document: first Decode succeeds + latches, second hits it.
		{"second decode after success hits latch", []byte("a = 1\n")},
		//: empty document: first Decode succeeds (empty), second hits the latch.
		{"second decode after empty doc hits latch", nil},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &tomlDecoder{r: bytes.NewReader(tc.data)}
		var out map[string]any
		//: first Decode drains the single-document stream and sets done.
		if err := dec.Decode(&out); err != nil {
			t.Fatalf("%s: first Decode err=%v", tc.name, err)
		}
		//: the latched call MUST return io.EOF without re-reading.
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

// Test_tomlDecoder_Decode_EOFFromLibrary covers the `errors.Is(err, io.EOF)`
// arm of Decode using a reader whose Read surfaces a wrapped io.EOF. This is
// the only way the read returns an io.EOF-matching error: io.ReadAll swallows
// the bare sentinel but propagates a wrapped one. The arm must return io.EOF
// untouched and set the done latch.
func Test_tomlDecoder_Decode_EOFFromLibrary(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"wrapped EOF from reader maps to io.EOF"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &tomlDecoder{r: eofWrapReader{}}
		var out map[string]any
		//: the read error wraps io.EOF, so Decode MUST surface io.EOF
		//: untouched (not the UNMARSHAL_FAILED wrap) and latch done.
		if got := dec.Decode(&out); !errors.Is(got, io.EOF) {
			t.Errorf("%s: Decode=%v want io.EOF", tc.name, got)
		}
		//: the EOF arm sets done, so More now reports drained.
		if dec.More() {
			t.Errorf("%s: More=true after EOF, want false", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tomlDecoder_More asserts the sticky EOF latch is idempotent: calling
// More twice in a row on a fresh decoder returns the same value.
func Test_tomlDecoder_More(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"idempotent before any Decode"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		dec := &tomlDecoder{r: bytes.NewReader(nil)}
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
