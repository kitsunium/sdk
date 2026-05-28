package logger_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger"
)

// TestNewBaseSink verifies the constructor accepts valid identity triples
// and panics on the three documented programmer errors.
func TestNewBaseSink(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		sinkName  string
		class     logger.SinkClass
		schemes   []string
		wantPanic bool
	}
	tests := []tc{
		{"valid local sink", "console", logger.LocalClass, []string{"stderr", "stdout"}, false},
		{"valid unknown class is allowed at construction", "x", logger.UnknownClass, nil, false},
		{"empty schemes is allowed", "x", logger.LocalClass, nil, false},
		{"empty name panics", "", logger.LocalClass, []string{"stdout"}, true},
		{"out-of-range class panics", "x", logger.SinkClass(99), nil, true},
		{"duplicate scheme panics", "x", logger.LocalClass, []string{"stdout", "stdout"}, true},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the constructor must build a frozen identity OR panic with a
	//: deterministic programmer-error message.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: recover into a sentinel so the test can assert the panic shape
		//: without crashing the test process.
		defer func() {
			r := recover()
			//: panic-expected row: recover must observe a non-nil value.
			if tc.wantPanic && r == nil {
				t.Fatalf("NewBaseSink(%q, %v, %v) expected panic; got none", tc.sinkName, tc.class, tc.schemes)
			}
			//: non-panic row: a recover value here signals an unexpected crash.
			if !tc.wantPanic && r != nil {
				t.Fatalf("NewBaseSink(%q, %v, %v) panicked: %v", tc.sinkName, tc.class, tc.schemes, r)
			}
		}()
		b := logger.NewBaseSink(tc.sinkName, tc.class, tc.schemes)
		//: panic-expected row should never reach here — the recover above
		//: handles the assertion.
		if tc.wantPanic {
			return
		}
		//: identity accessors must round-trip the constructor arguments.
		if got := b.Name(); got != tc.sinkName {
			t.Errorf("Name() = %q, want %q", got, tc.sinkName)
		}
		if got := b.Class(); got != tc.class {
			t.Errorf("Class() = %v, want %v", got, tc.class)
		}
		gotSchemes := b.Schemes()
		if len(gotSchemes) != len(tc.schemes) {
			t.Errorf("Schemes() length = %d, want %d", len(gotSchemes), len(tc.schemes))
		}
		for i := range gotSchemes {
			if gotSchemes[i] != tc.schemes[i] {
				t.Errorf("Schemes()[%d] = %q, want %q", i, gotSchemes[i], tc.schemes[i])
			}
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestBaseSink_Name covers BaseSink.Name() — the identity accessor
// returns the constructor argument verbatim.
func TestBaseSink_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"console name", "console"},
		{"long kebab", "my-sink-v2"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; Name() must hand back the constructor argument.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		b := logger.NewBaseSink(tc.want, logger.LocalClass, nil)
		//: the identity getter must equal the constructor name.
		if got := b.Name(); got != tc.want {
			t.Fatalf("Name() = %q, want %q", got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestBaseSink_Class covers BaseSink.Class() — the accessor returns the
// SinkClass declared at construction.
func TestBaseSink_Class(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want logger.SinkClass
	}
	tests := []tc{
		{"local", logger.LocalClass},
		{"remote-batched", logger.RemoteBatchedClass},
		{"unknown is preserved", logger.UnknownClass},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; Class() must hand back the constructor value.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		b := logger.NewBaseSink("x", tc.want, nil)
		//: the identity getter must equal the constructor class.
		if got := b.Class(); got != tc.want {
			t.Fatalf("Class() = %v, want %v", got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestBaseSink_Schemes covers BaseSink.Schemes() — the accessor returns a
// defensive clone of the constructor argument; caller-side mutation never
// bleeds.
func TestBaseSink_Schemes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []string
	}
	tests := []tc{
		{"empty", nil},
		{"single", []string{"stdout"}},
		{"multi", []string{"stderr", "stdout"}},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; Schemes() must defensively clone on every call.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		b := logger.NewBaseSink("x", logger.LocalClass, tc.in)
		got := b.Schemes()
		//: lengths must match.
		if len(got) != len(tc.in) {
			t.Fatalf("Schemes() len = %d, want %d", len(got), len(tc.in))
		}
		//: element-wise equality with the constructor argument.
		for i := range got {
			if got[i] != tc.in[i] {
				t.Errorf("Schemes()[%d] = %q, want %q", i, got[i], tc.in[i])
			}
		}
		//: second call must hand back an independent slice (defensive clone).
		again := b.Schemes()
		if len(again) > 0 && len(got) > 0 && &again[0] == &got[0] {
			t.Errorf("Schemes() did not clone: same backing array")
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestBaseSinkSchemesDefensiveClone verifies the documented contract that
// mutating the caller-side schemes slice does NOT affect Schemes(), and
// vice versa.
func TestBaseSinkSchemesDefensiveClone(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"caller mutation leaves Schemes intact"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the identity triple must be immutable from outside the sink.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		original := []string{"stderr", "stdout"}
		b := logger.NewBaseSink("console", logger.LocalClass, original)
		//: mutate the caller-side slice after construction.
		original[0] = "MUTATED"
		got := b.Schemes()
		//: BaseSink kept its own copy — the first element is the original.
		if got[0] != "stderr" {
			t.Fatalf("constructor clone failed: Schemes()[0] = %q, want %q", got[0], "stderr")
		}
		//: mutate the returned slice and confirm the next call still
		//: observes the immutable identity.
		got[0] = "ALSO_MUTATED"
		again := b.Schemes()
		//: Schemes() defensively clones on every call.
		if again[0] != "stderr" {
			t.Fatalf("Schemes() clone failed: second call[0] = %q, want %q", again[0], "stderr")
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
