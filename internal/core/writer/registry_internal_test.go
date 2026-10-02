package writer

import (
	"errors"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ResetForTest clears the process-wide writer registry back to the empty
// state. Exported from this white-box test file so the external test package
// (writer_test) can isolate registry mutations: the registry is package-global
// and `go test -count=N` reuses the process (package state is NOT re-initialised
// between iterations), so a test that calls Register must reset first or a later
// iteration panics on a duplicate Name. Test-only.
func ResetForTest() {
	//: store a nil snapshot; loadRegistry then reports empty (Load returns nil).
	registry.Store(nil)
}

// stubFactory is a minimal Factory used by the internal coverage tests.
type stubFactory struct{ id Name }

// : compile-time proof the stub satisfies the port it stands in for.
var _ Factory = (*stubFactory)(nil)

func (f *stubFactory) Name() Name { return f.id }

func (f *stubFactory) Open(_ Config) (sink corelogger.Sink, err error) {
	//: the internal tests never exercise Open's body — return the zero pair.
	return nil, nil
}

func Test_cloneFactoryMap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		src     *map[Name]Factory
		wantLen int
	}
	tests := []tc{
		{"nil source yields a single-entry map", nil, 1},
		//: new(expr) (Go 1.26+) heap-allocates the seed literal in one shot,
		//: avoiding the local-var + &addr form KTN-VAR-NEWEXPR flags.
		{"non-nil source copies plus one", new(map[Name]Factory{"seed": &stubFactory{id: "seed"}}), 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := cloneFactoryMap(c.src, "added", &stubFactory{id: "added"})
		//: the clone must carry exactly the expected cardinality.
		if len(got) != c.wantLen {
			t.Errorf("%s: len=%d want %d", c.name, len(got), c.wantLen)
		}
		//: the inserted entry must always be present.
		if _, ok := got["added"]; !ok {
			t.Errorf("%s: clone missing the inserted entry", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_publishFactory_conflictIsTyped proves a refused publish is an SDK error
// with the duplicate-registration code (rule 2) — matchable with errs.HasCode
// rather than by parsing text — and that the panic text Register renders from
// it still names the Name that collided.
func Test_publishFactory_conflictIsTyped(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	if err := publishFactory("typed", &stubFactory{id: "typed"}); err != nil {
		t.Fatalf("the first publish = %v, want nil", err)
	}
	err := publishFactory("typed", &stubFactory{id: "typed"})
	if !errs.HasCode(err, CodeDuplicateRegistration) || !errors.Is(err, DuplicateRegistration) {
		t.Fatalf("the conflict = %v, want the typed DuplicateRegistration", err)
	}
	text := conflictText(err)
	for _, want := range []string{"[0.2.3.1 DUPLICATE_REGISTRATION]", `registrar="writer.Register"`, `name="typed"`} {
		if !strings.Contains(text, want) {
			t.Errorf("conflictText = %q, want it to say %s", text, want)
		}
	}
}
