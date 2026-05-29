package writer_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestRegister(t *testing.T) {
	//: clean slate so this test survives `go test -count=N` (the package-global
	//: registry persists across iterations).
	writer.ResetForTest()
	tests := []struct {
		name string
		arg  *fakeFactory
	}{
		{"first factory registers", &fakeFactory{id: "reg-1", sink: stubSink{}}},
		{"second slot, distinct factory", &fakeFactory{id: "reg-2", sink: stubSink{}}},
	}
	runCase := func(t *testing.T, ff *fakeFactory) {
		t.Helper()
		writer.Register(ff)
		//: the factory must be resolvable immediately after Register.
		if _, ok := writer.Lookup(ff.id); !ok {
			t.Errorf("Lookup(%q) failed after Register", ff.id)
		}
	}
	for _, tc := range tests {
		//: sequential — Register mutates the process-wide registry.
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc.arg) })
	}
}

func TestRegisterPanics(t *testing.T) {
	//: clean slate so the duplicate case panics on its OWN second registration.
	writer.ResetForTest()
	tests := []struct {
		name string
		run  func()
	}{
		{"nil factory panics", func() { writer.Register(nil) }},
		{
			"duplicate Name panics",
			func() {
				dup := &fakeFactory{id: "dup-ext", sink: stubSink{}}
				writer.Register(dup)
				//: a DISTINCT factory under the same Name is the hard conflict.
				writer.Register(&fakeFactory{id: "dup-ext", sink: stubSink{}})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				//: a missing panic means Register failed to guard the case.
				if r := recover(); r == nil {
					t.Errorf("%s: expected panic, got none", tc.name)
				}
			}()
			tc.run()
		})
	}
}

func TestLookup(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     writer.Name
		wantOK bool
	}
	tests := []tc{
		{"empty misses", writer.Name(""), false},
		{"unregistered misses", writer.Name("absent-zzz"), false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: an empty or absent Name must report a clean miss.
		if _, ok := writer.Lookup(c.in); ok != c.wantOK {
			t.Errorf("%s: ok=%v want %v", c.name, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestOpen(t *testing.T) {
	//: sequential — seeds the process-wide registry with two fakes.
	writer.ResetForTest()
	sentinel := errors.New("factory boom")
	writer.Register(&fakeFactory{id: "open-ok", sink: stubSink{}})
	writer.Register(&fakeFactory{id: "open-err", err: sentinel})
	type tc struct {
		name     string
		in       writer.Name
		wantErr  error
		wantCode errs.Code
		wantNil  bool
	}
	tests := []tc{
		{"resolved factory builds a sink", "open-ok", nil, 0, false},
		{"factory error propagates verbatim", "open-err", sentinel, 0, true},
		{"unknown name returns WriterUnknownName", "open-absent", writer.WriterUnknownName, writer.CodeWriterUnknownName, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := writer.Open(c.in, nil)
		//: a nil-sink expectation must hold (failure arms hand back nil).
		if (sink == nil) != c.wantNil {
			t.Errorf("%s: sink nil=%v want %v", c.name, sink == nil, c.wantNil)
		}
		//: the happy path must not error.
		if c.wantErr == nil {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			return
		}
		//: the failure path must surface the expected cause (errors.Is matches
		//: both the raw sentinel and, for *errs.Error, semantic equality).
		if !errors.Is(err, c.wantErr) {
			t.Errorf("%s: err=%v want %v", c.name, err, c.wantErr)
		}
		//: a non-zero wantCode demands dotted-quad introspectability.
		if c.wantCode != 0 && !errs.HasCode(err, c.wantCode) {
			t.Errorf("%s: HasCode(%v)=false, err=%v", c.name, c.wantCode, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestAvailable(t *testing.T) {
	//: sequential — each row asserts the global registry after a clean seed.
	type tc struct {
		name    string
		seed    []writer.Name
		wantLen int
		wantNil bool
	}
	tests := []tc{
		{"empty registry returns nil", nil, 0, true},
		{"two factories listed sorted ascending", []writer.Name{"av-b", "av-a"}, 2, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		writer.ResetForTest()
		//: seed the registry with the row's factories before listing.
		for _, id := range c.seed {
			writer.Register(&fakeFactory{id: id, sink: stubSink{}})
		}
		got := writer.Available()
		//: the empty arm must return the documented nil slice.
		if c.wantNil {
			if got != nil {
				t.Errorf("%s: Available()=%v want nil", c.name, got)
			}
			return
		}
		//: the populated arm must carry every seeded factory.
		if len(got) != c.wantLen {
			t.Fatalf("%s: len=%d want %d (%v)", c.name, len(got), c.wantLen, got)
		}
		//: Available must return the names in ascending sorted order.
		if got[0] != writer.Name("av-a") || got[1] != writer.Name("av-b") {
			t.Errorf("%s: Available()=%v want [av-a av-b] sorted", c.name, got)
		}
	}
	for _, c := range tests {
		//: sequential — Available reads the process-wide registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
