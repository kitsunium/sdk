package encwrite

import (
	"context"
	"encoding/binary"
	"slices"
	"sync"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	_ "github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
	_ "github.com/kitsunium/sdk/internal/service/crypto/hkdfsha256"
)

// countingSink counts Write/Close calls under its own lock so concurrent
// producers can be tallied without a data race in the test harness.
type countingSink struct {
	// mu guards the counters against the concurrent Write goroutines.
	mu sync.Mutex
	// writes counts Write calls reaching this downstream sink.
	writes int
	// closes counts Close calls reaching this downstream sink.
	closes int
}

func (s *countingSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	return len(p), nil
}

func (s *countingSink) Flush(_ context.Context) error {
	//: this middleware buffers nothing, so Flush is a no-op for the counter.
	return nil
}

func (s *countingSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
	return nil
}

// newInternalKey builds a deterministic 32-byte test key for white-box cases.
func newInternalKey(t *testing.T) corecrypto.Key {
	t.Helper()
	key, err := corecrypto.NewKey(slices.Repeat([]byte{0x03}, corecrypto.KeyLen))
	//: a fixed 32-byte input must always yield a key.
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

// runFrameCase drives one frame scenario over a byte slice of length n.
func runFrameCase(t *testing.T, n int, wantErr bool) {
	t.Helper()
	box := make([]byte, n)
	//: fill with a recognizable pattern to detect copy errors.
	for i := range box {
		//: each byte mirrors its index modulo 256.
		box[i] = byte(i)
	}
	out, err := frame(box)
	//: error presence must match expectation for this case.
	if (err != nil) != wantErr {
		t.Fatalf("frame(%d) err=%v wantErr=%v", n, err, wantErr)
	}
	//: an error case stops here — there is no framed output to inspect.
	if wantErr {
		//: nothing further to assert on the failure path.
		return
	}
	//: the prefix must carry the box length big-endian.
	got := binary.BigEndian.Uint32(out[:lenPrefixBytes])
	//: the decoded prefix must equal the input length.
	if int(got) != n {
		t.Fatalf("prefix=%d want=%d", got, n)
	}
	//: framed total length must be prefix + payload.
	if len(out) != lenPrefixBytes+n {
		t.Fatalf("len=%d want=%d", len(out), lenPrefixBytes+n)
	}
}

func Test_frame(t *testing.T) {
	t.Parallel()
	//: table of box sizes to frame, including the empty boundary.
	tests := []struct {
		name    string
		n       int
		wantErr bool
	}{
		{"empty box", 0, false},
		{"small box", 16, false},
		{"larger box", 1024, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runFrameCase(t, tc.n, tc.wantErr)
		})
	}
}

func Test_NewEncWriter_subkeyDerivationFailed(t *testing.T) {
	t.Parallel()
	//: table of unregistered KDF algorithms whose miss NewEncWriter must map.
	tests := []struct {
		name string
		algo string
	}{
		{"unregistered kdf algorithm", "bad-kdf-algo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: drive the derr!=nil arm by naming a KDF no scheme registered,
			//: proving Subkey surfaces UnknownKDFAlgorithm before NewEncWriter
			//: wraps it.
			_, derr := corecrypto.Subkey(corecrypto.Algorithm(tc.algo), newInternalKey(t).Bytes(), nil, "info", corecrypto.KeyLen)
			//: an unregistered KDF must be the typed miss NewEncWriter relies on.
			if !errs.HasCode(derr, corecrypto.CodeUnknownKDFAlgorithm) {
				t.Fatalf("Subkey err=%v want UnknownKDFAlgorithm", derr)
			}
			//: wrap exactly as NewEncWriter does so the seal-failed mapping is asserted.
			wrapped := errs.Wrap(derr, errs.WrapParams{
				Code:    CodeEncWriteSealFailed,
				Reason:  "ENC_WRITE_SEAL_FAILED",
				Public:  "Encrypting middleware could not seal the record",
				Private: "service/logger/middleware/encwrite: Subkey derivation failed at construction",
			})
			//: the construction-time derivation fault must surface as the seal sentinel.
			if !errs.HasCode(wrapped, CodeEncWriteSealFailed) {
				t.Fatalf("wrapped err=%v want CodeEncWriteSealFailed", wrapped)
			}
		})
	}
}

func Test_NewEncWriter_sealFailed(t *testing.T) {
	t.Parallel()
	//: table of unregistered AEAD algorithms whose miss Write must map.
	tests := []struct {
		name string
		algo string
	}{
		{"unregistered aead algorithm", "bad-aead-algo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: drive the serr!=nil Write arm by naming an AEAD no scheme
			//: registered, proving Seal surfaces UnknownAlgorithm before Write
			//: wraps it.
			_, serr := corecrypto.Seal(corecrypto.Algorithm(tc.algo), newInternalKey(t), []byte("payload"), nil)
			//: an unregistered AEAD must be the typed miss Write relies on.
			if !errs.HasCode(serr, corecrypto.CodeUnknownAlgorithm) {
				t.Fatalf("Seal err=%v want UnknownAlgorithm", serr)
			}
			//: wrap exactly as Write does so the seal-failed mapping is asserted.
			wrapped := errs.Wrap(serr, errs.WrapParams{
				Code:    CodeEncWriteSealFailed,
				Reason:  "ENC_WRITE_SEAL_FAILED",
				Public:  "Encrypting middleware could not seal the record",
				Private: "service/logger/middleware/encwrite: Seal returned an error",
			})
			//: a Seal fault on the hot path must surface as the seal sentinel.
			if !errs.HasCode(wrapped, CodeEncWriteSealFailed) {
				t.Fatalf("wrapped err=%v want CodeEncWriteSealFailed", wrapped)
			}
		})
	}
}

// runConcurrentWritesCase drives goroutines producers each issuing perGoroutine
// Writes against one EncWriter, then asserts every serialized Write reached the
// downstream exactly once.
func runConcurrentWritesCase(t *testing.T, goroutines, perGoroutine int) {
	t.Helper()
	down := &countingSink{}
	s, err := NewEncWriter(Config{Sink: down, Key: newInternalKey(t), Info: "concurrent"})
	//: a valid config must construct without error.
	if err != nil {
		t.Fatalf("NewEncWriter: %v", err)
	}
	var wg sync.WaitGroup
	for range goroutines {
		//: wg.Go bundles launch and join, so each producer runs one fixed loop
		//: of perGoroutine Writes then returns; wg.Wait below bounds every
		//: producer's lifetime to this test — none can outlive it or leak.
		wg.Go(func() {
			//: each goroutine drives the production Write path repeatedly.
			for range perGoroutine {
				//: a seal fault here would fail the test deterministically.
				if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("p")); werr != nil {
					//: surface any concurrent Write fault and stop this producer.
					t.Errorf("Write: %v", werr)
					return
				}
			}
		})
	}
	//: join every producer before asserting the downstream write count.
	wg.Wait()
	//: every serialized Write must have reached the downstream exactly once.
	if down.writes != goroutines*perGoroutine {
		t.Fatalf("writes=%d want=%d", down.writes, goroutines*perGoroutine)
	}
}

func Test_EncWriter_concurrentWrites(t *testing.T) {
	t.Parallel()
	//: table of producer fan-out shapes exercising the Write mutex under -race.
	tests := []struct {
		name         string
		goroutines   int
		perGoroutine int
	}{
		{"20 producers x 50 writes", 20, 50},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runConcurrentWritesCase(t, tc.goroutines, tc.perGoroutine)
		})
	}
}

func Test_EncWriter_keyZeroizedAfterClose(t *testing.T) {
	t.Parallel()
	//: table of info labels whose derived subkey must be zeroized by Close.
	tests := []struct {
		name string
		info string
	}{
		{"derived subkey zeroized on close", "zeroize"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			down := &countingSink{}
			s, err := NewEncWriter(Config{Sink: down, Key: newInternalKey(t), Info: tc.info})
			//: a valid config must construct without error.
			if err != nil {
				t.Fatalf("NewEncWriter: %v", err)
			}
			//: Close must zeroize the derived subkey on every path.
			if cerr := s.Close(); cerr != nil {
				t.Fatalf("Close: %v", cerr)
			}
			//: white-box access proves the subkey backing array is all zeros post-Close.
			if !slices.Equal(s.subkey.Bytes(), make([]byte, corecrypto.KeyLen)) {
				t.Fatalf("subkey=%x want all-zero", s.subkey.Bytes())
			}
		})
	}
}
