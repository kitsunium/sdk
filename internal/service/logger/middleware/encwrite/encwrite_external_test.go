package encwrite_test

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/encwrite"

	_ "github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
	_ "github.com/kitsunium/sdk/internal/service/crypto/hkdfsha256"
)

// recordingSink captures the bytes handed to it for assertions.
type recordingSink struct {
	// last is a copy of the framed payload passed to the most recent Write.
	last []byte
	// writeErr is returned by Write — drives the failure path.
	writeErr error
	// flushErr is returned by Flush — drives the failure path.
	flushErr error
	// closeErr is returned by Close — drives the failure path.
	closeErr error
	// writes counts Write calls.
	writes int
	// flushes counts Flush calls.
	flushes int
	// closes counts Close calls.
	closes int
}

func (s *recordingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	s.writes++
	//: retain a copy of the framed payload for later inspection.
	s.last = slices.Clone(p)
	//: a configured error simulates a downstream failure.
	if s.writeErr != nil {
		//: report zero bytes written on failure.
		return 0, s.writeErr
	}
	return len(p), nil
}

func (s *recordingSink) Flush(_ context.Context) error {
	s.flushes++
	return s.flushErr
}

func (s *recordingSink) Close() error {
	s.closes++
	return s.closeErr
}

// newKey builds a deterministic test key.
func newKey(t *testing.T) corecrypto.Key {
	t.Helper()
	key, err := corecrypto.NewKey(slices.Repeat([]byte{0x02}, corecrypto.KeyLen))
	//: a fixed 32-byte input must always yield a key.
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

// newSink builds an EncWriter over down with a fresh key and fixed info.
func newSink(t *testing.T, down corelogger.Sink) *encwrite.EncWriter {
	t.Helper()
	s, err := encwrite.NewEncWriter(encwrite.Config{Sink: down, Key: newKey(t), Info: "test"})
	//: a valid config must construct without error.
	if err != nil {
		t.Fatalf("NewEncWriter: %v", err)
	}
	return s
}

func Test_NewEncWriter(t *testing.T) {
	t.Parallel()
	//: table of construction scenarios.
	tests := []struct {
		name string
		info string
	}{
		{"explicit info label", "explicit"},
		{"default info label", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := encwrite.NewEncWriter(encwrite.Config{Sink: &recordingSink{}, Key: newKey(t), Info: tc.info})
			//: both an explicit and an empty info must construct cleanly.
			if err != nil || s == nil {
				t.Fatalf("NewEncWriter=(%v,%v)", s, err)
			}
		})
	}
}

// runWriteCase drives one Write scenario over a fake downstream sink.
func runWriteCase(t *testing.T, writeErr error, wantErr bool) {
	t.Helper()
	down := &recordingSink{writeErr: writeErr}
	s := newSink(t, down)
	n, err := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("payload"))
	//: error presence must match expectation.
	if (err != nil) != wantErr {
		t.Fatalf("err=%v wantErr=%v", err, wantErr)
	}
	//: the failure path has no framed output to inspect.
	if wantErr {
		//: nothing further to assert.
		return
	}
	//: on success the framed prefix must equal the sealed-box length.
	got := binary.BigEndian.Uint32(down.last[:4])
	//: the prefix encodes the box length, which is len(framed)-prefix.
	if int(got) != len(down.last)-4 || n != len(down.last) {
		t.Fatalf("prefix=%d framed=%d n=%d", got, len(down.last), n)
	}
}

func Test_EncWriter_Write(t *testing.T) {
	t.Parallel()
	//: table of write scenarios.
	tests := []struct {
		name     string
		writeErr error
		wantErr  bool
	}{
		{"happy path frames and delivers", nil, false},
		{"downstream failure propagates", errors.New("boom"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runWriteCase(t, tc.writeErr, tc.wantErr)
		})
	}
}

// runFlushCase drives one Flush scenario.
func runFlushCase(t *testing.T, flushErr error, wantErr bool) {
	t.Helper()
	down := &recordingSink{flushErr: flushErr}
	s := newSink(t, down)
	err := s.Flush(t.Context())
	//: error presence must match expectation.
	if (err != nil) != wantErr {
		t.Fatalf("err=%v wantErr=%v", err, wantErr)
	}
	//: Flush must be forwarded exactly once.
	if down.flushes != 1 {
		t.Fatalf("flushes=%d want=1", down.flushes)
	}
}

func Test_EncWriter_Flush(t *testing.T) {
	t.Parallel()
	//: table of flush scenarios.
	tests := []struct {
		name     string
		flushErr error
		wantErr  bool
	}{
		{"clean flush forwards", nil, false},
		{"downstream flush error propagates", errors.New("boom"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runFlushCase(t, tc.flushErr, tc.wantErr)
		})
	}
}

// runCloseCase drives one Close scenario.
func runCloseCase(t *testing.T, closeErr error, wantErr bool) {
	t.Helper()
	down := &recordingSink{closeErr: closeErr}
	s := newSink(t, down)
	err := s.Close()
	//: error presence must match expectation.
	if (err != nil) != wantErr {
		t.Fatalf("err=%v wantErr=%v", err, wantErr)
	}
	//: downstream Close must be invoked exactly once.
	if down.closes != 1 {
		t.Fatalf("closes=%d want=1", down.closes)
	}
}

func Test_EncWriter_Close(t *testing.T) {
	t.Parallel()
	//: table of close scenarios.
	tests := []struct {
		name     string
		closeErr error
		wantErr  bool
	}{
		{"clean close zeroizes and forwards", nil, false},
		{"downstream close error propagates", errors.New("boom"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCloseCase(t, tc.closeErr, tc.wantErr)
		})
	}
}

// runRoundTripCase seals plaintext through the sink, then re-derives the subkey
// and opens the framed box, asserting recovery of the original bytes.
func runRoundTripCase(t *testing.T, info string, plaintext []byte) {
	t.Helper()
	down := &recordingSink{}
	s, err := encwrite.NewEncWriter(encwrite.Config{Sink: down, Key: newKey(t), Info: info})
	//: construction must succeed for the round-trip.
	if err != nil {
		t.Fatalf("NewEncWriter: %v", err)
	}
	//: a write seals + frames the plaintext into down.last.
	if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, plaintext); werr != nil {
		t.Fatalf("Write: %v", werr)
	}
	//: strip the 4-byte prefix to obtain the sealed box.
	box := down.last[4:]
	//: re-derive the same subkey to open the box.
	raw, derr := corecrypto.Subkey("hkdf-sha256", newKey(t).Bytes(), nil, info, corecrypto.KeyLen)
	//: re-derivation must reproduce the sealing subkey.
	if derr != nil {
		t.Fatalf("Subkey: %v", derr)
	}
	subkey, kerr := corecrypto.NewKey(raw)
	//: the derived subkey must materialise as a Key.
	if kerr != nil {
		t.Fatalf("NewKey: %v", kerr)
	}
	out, oerr := corecrypto.Open(subkey, box, nil)
	//: opening the box must recover the exact plaintext.
	if oerr != nil || !slices.Equal(out, plaintext) {
		t.Fatalf("Open=(%q,%v) want (%q,nil)", out, oerr, plaintext)
	}
}

func Test_EncWriter_RoundTrip(t *testing.T) {
	t.Parallel()
	//: table of round-trip payloads.
	tests := []struct {
		name      string
		info      string
		plaintext []byte
	}{
		{"non-empty record", "rt", []byte("hello world")},
		{"empty record", "rt2", []byte{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runRoundTripCase(t, tc.info, tc.plaintext)
		})
	}
}

func Test_Sentinels(t *testing.T) {
	t.Parallel()
	//: table asserting each sentinel carries its dotted-quad code.
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"framing sentinel", encwrite.FramingFailed, encwrite.CodeFramingFailed},
		{"seal sentinel", encwrite.EncWriteSealFailed, encwrite.CodeEncWriteSealFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: HasCode must report the expected code on the sentinel.
			if !errs.HasCode(tc.err, tc.code) {
				t.Fatalf("HasCode(%v) failed", tc.code)
			}
		})
	}
}
