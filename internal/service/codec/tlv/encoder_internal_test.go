package tlv

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec/scratch"
)

// scratchProbeRents is how many buffers assertScratchPoolClean rents at once.
// More than one, so a probe that drew a fresh buffer from the factory does
// not stop it from reaching the one an Encode just returned.
const scratchProbeRents int = 4

// failingWriter rejects every Write call with a fixed error, used to
// exercise the streaming encoder's writer-error branch.
type failingWriter struct{}

// Write always returns errFailingWriter. The parameter is referenced via
// the discard-blank assignment so KTN-VAR-DEADREAD does not flag it.
func (failingWriter) Write(p []byte) (n int, err error) {
	//: surface a synthetic failure for the wrap branch.
	if len(p) > 0 {
		//: any non-nil error path is sufficient.
		return 0, errFailingWriter
	}
	//: empty buffer still fails so the encoder cannot pre-empt the branch.
	return 0, errFailingWriter
}

// errFailingWriter is the sentinel returned by failingWriter.Write.
var errFailingWriter = errors.New("writer failed (synthetic)")

// assertScratchPoolClean rents scratchProbeRents buffers from encodeScratch
// and fails when one is not empty, is narrower than scratchInitialCap, or is
// wider than the retain ceiling — the state every Encode, successful or not,
// must leave the pool in. The pool resets on Put and not on Get, so an empty
// rent is the reset working; and a pool that kept a buffer a record grew past
// the ceiling would hand it to the very next rent on the same P, which is
// what the last check catches.
func assertScratchPoolClean(t *testing.T, label string) {
	t.Helper()
	rented := make([]*[]byte, 0, scratchProbeRents)
	for range scratchProbeRents {
		bp := encodeScratch.Get()
		//: a rented buffer starts empty — nothing of the last record survives.
		if len(*bp) != 0 {
			t.Errorf("%s: rented scratch len=%d want 0", label, len(*bp))
		}
		//: it is at least as wide as a fresh one — buffers only ever grow.
		if cap(*bp) < scratchInitialCap {
			t.Errorf("%s: rented scratch cap=%d want >= %d", label, cap(*bp), scratchInitialCap)
		}
		//: and it is never wider than the retain ceiling.
		if cap(*bp) > scratch.MaxRetainedBufBytes {
			t.Errorf("%s: rented scratch cap=%d exceeds the %d retain ceiling", label, cap(*bp), scratch.MaxRetainedBufBytes)
		}
		rented = append(rented, bp)
	}
	//: hand every probe back so the pool keeps serving the other subtests.
	for _, bp := range rented {
		encodeScratch.Put(bp)
	}
}

// Test_tlvEncoder_Encode covers the streaming encoder's success and
// writer-error branches, and the state each leaves the scratch pool in: a
// record that fails halfway hands back a dirty buffer the pool must reset, and
// a record that outgrows the retain ceiling still encodes while its buffer is
// not kept.
func Test_tlvEncoder_Encode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		value   any
		writer  io.Writer
		wantErr bool
	}
	tests := []tc{
		{"int encodes cleanly", int64(7), &bytes.Buffer{}, false},
		{"writer error surfaces", int64(7), failingWriter{}, true},
		{"unsupported type surfaces", make(chan int), &bytes.Buffer{}, true},
		{"failure mid-record surfaces", struct {
			A int
			B chan int
		}{A: 1}, &bytes.Buffer{}, true},
		{"record past the retain ceiling encodes and is not pooled", make([]byte, 2*scratch.MaxRetainedBufBytes), io.Discard, false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := &tlvEncoder{w: tc.writer}
		err := enc.Encode(tc.value)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Encode err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
		//: whatever the outcome, the scratch went back empty and bounded.
		assertScratchPoolClean(t, tc.name)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tlvEncoder_Close exercises the no-op Close wrapper.
func Test_tlvEncoder_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		wantErr bool
	}
	tests := []tc{{"close after encode succeeds", false}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := &tlvEncoder{w: &bytes.Buffer{}}
		err := enc.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Close err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
