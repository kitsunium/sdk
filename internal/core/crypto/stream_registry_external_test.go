package crypto_test

import (
	"bytes"
	"io"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// nopWriteCloser adapts a Writer to a WriteCloser with a no-op Close.
type nopWriteCloser struct{ w io.Writer }

func (n nopWriteCloser) Write(p []byte) (int, error) { return n.w.Write(p) }

func (nopWriteCloser) Close() error { return nil }

// fakeStreamSealer is a comparable StreamSealer that passes bytes straight
// through, so the registry/dispatch tests need no real cipher.
type fakeStreamSealer struct {
	name crypto.Algorithm
}

// : compile-time proof the fake satisfies the StreamSealer port.
var _ crypto.StreamSealer = (*fakeStreamSealer)(nil)

func (f fakeStreamSealer) Algorithm() crypto.Algorithm { return f.name }

func (fakeStreamSealer) Writer(_ crypto.Key, dst io.Writer, _ []byte) (io.WriteCloser, error) {
	return nopWriteCloser{dst}, nil
}

func (fakeStreamSealer) Reader(_ crypto.Key, src io.Reader, _ []byte) (io.Reader, error) {
	return src, nil
}

// distinctStreamSealer is a SECOND StreamSealer type claiming the same Algorithm
// as a fakeStreamSealer, to exercise the distinct-duplicate conflict.
type distinctStreamSealer struct{}

func (distinctStreamSealer) Algorithm() crypto.Algorithm { return "stream-dup" }

func (distinctStreamSealer) Writer(_ crypto.Key, _ io.Writer, _ []byte) (io.WriteCloser, error) {
	return nil, nil
}

func (distinctStreamSealer) Reader(_ crypto.Key, _ io.Reader, _ []byte) (io.Reader, error) {
	return nil, nil
}

func TestRegisterStreamSealer(t *testing.T) {
	type tc struct {
		name string
		arg  fakeStreamSealer
	}
	tests := []tc{
		{"first sealer registers", fakeStreamSealer{name: "fs-1"}},
		{"second slot, distinct sealer", fakeStreamSealer{name: "fs-2"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := crypto.RegisterStreamSealer(c.arg)
		//: RegisterStreamSealer hands back the sealer and it resolves immediately.
		if got.Algorithm() != c.arg.name {
			t.Errorf("RegisterStreamSealer returned %q want %q", got.Algorithm(), c.arg.name)
		}
		if _, ok := crypto.LookupStreamSealer(c.arg.name); !ok {
			t.Errorf("LookupStreamSealer(%q) failed after RegisterStreamSealer", c.arg.name)
		}
	}
	for _, c := range tests {
		//: sequential — mutates the process-wide stream-sealer registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterStreamSealerPanics(t *testing.T) {
	type tc struct {
		name string
		run  func()
	}
	tests := []tc{
		{"nil sealer panics", func() { crypto.RegisterStreamSealer(nil) }},
		{
			"distinct duplicate panics",
			func() {
				crypto.RegisterStreamSealer(fakeStreamSealer{name: "stream-dup"})
				//: a DISTINCT sealer under the same name is the hard conflict.
				crypto.RegisterStreamSealer(distinctStreamSealer{})
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			//: a missing panic means RegisterStreamSealer failed to guard the case.
			if r := recover(); r == nil {
				t.Errorf("%s: expected panic, got none", c.name)
			}
		}()
		c.run()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterStreamSealerIdempotent(t *testing.T) {
	type tc struct {
		name string
		algo crypto.Algorithm
	}
	tests := []tc{
		{"same instance re-registers cleanly", "stream-idem-1"},
		{"second name re-registers cleanly", "stream-idem-2"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := fakeStreamSealer{name: c.algo}
		crypto.RegisterStreamSealer(s)
		//: re-registering the SAME instance is a no-op, never a panic.
		crypto.RegisterStreamSealer(s)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestLookupStreamSealer(t *testing.T) {
	crypto.RegisterStreamSealer(fakeStreamSealer{name: "lk-s"})
	type tc struct {
		name   string
		in     crypto.Algorithm
		wantOK bool
	}
	tests := []tc{
		{"registered sealer resolves", "lk-s", true},
		{"unregistered misses", "absent-zzz-s", false},
		{"empty algorithm misses", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: LookupStreamSealer reports presence by Algorithm.
		if _, ok := crypto.LookupStreamSealer(c.in); ok != c.wantOK {
			t.Errorf("%s: LookupStreamSealer(%q)=%v want %v", c.name, c.in, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestAvailableStreamSealers(t *testing.T) {
	type tc struct {
		name string
		seed crypto.Algorithm
	}
	tests := []tc{
		{"first seeded sealer is listed", "av-s-a"},
		{"second seeded sealer is listed", "av-s-b"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		crypto.RegisterStreamSealer(fakeStreamSealer{name: c.seed})
		//: AvailableStreamSealers must include every registered algorithm.
		if !slices.Contains(crypto.AvailableStreamSealers(), c.seed) {
			t.Errorf("%s: AvailableStreamSealers missing %q", c.name, c.seed)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestSealStream(t *testing.T) {
	crypto.RegisterStreamSealer(fakeStreamSealer{name: "seal-s"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"registered sealer builds a writer", "seal-s", false},
		{"unregistered algorithm errors", "ghost-s", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var buf bytes.Buffer
		w, err := crypto.SealStream(c.alg, crypto.Key{}, &buf, nil)
		//: the failure arm reuses UnknownAlgorithm (streaming extends AEAD).
		if c.wantErr {
			if w != nil || !errs.HasCode(err, crypto.CodeUnknownAlgorithm) {
				t.Errorf("%s: SealStream=(%v,%v) want (nil,UnknownAlgorithm)", c.name, w, err)
			}
			return
		}
		//: the success arm returns a usable sealing writer.
		if err != nil {
			t.Fatalf("%s: SealStream err=%v", c.name, err)
		}
		if _, werr := w.Write([]byte("payload")); werr != nil {
			t.Errorf("%s: sealed write err=%v", c.name, werr)
		}
		if cerr := w.Close(); cerr != nil {
			t.Errorf("%s: sealed close err=%v", c.name, cerr)
		}
		if buf.String() != "payload" {
			t.Errorf("%s: sealed buf=%q want payload", c.name, buf.String())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestOpenStream(t *testing.T) {
	crypto.RegisterStreamSealer(fakeStreamSealer{name: "open-s"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"registered sealer builds a reader", "open-s", false},
		{"unregistered algorithm errors", "ghost-s", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r, err := crypto.OpenStream(c.alg, crypto.Key{}, bytes.NewReader([]byte("payload")), nil)
		//: the failure arm reuses UnknownAlgorithm (streaming extends AEAD).
		if c.wantErr {
			if r != nil || !errs.HasCode(err, crypto.CodeUnknownAlgorithm) {
				t.Errorf("%s: OpenStream=(%v,%v) want (nil,UnknownAlgorithm)", c.name, r, err)
			}
			return
		}
		//: the success arm returns a usable opening reader.
		if err != nil {
			t.Fatalf("%s: OpenStream err=%v", c.name, err)
		}
		got, rerr := io.ReadAll(r)
		if rerr != nil || string(got) != "payload" {
			t.Errorf("%s: opened read=(%q,%v) want (payload,nil)", c.name, got, rerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
