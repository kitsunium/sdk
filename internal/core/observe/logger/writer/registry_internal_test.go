package writer

import (
	"errors"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// ResetForTest clears the process-wide writer registry back to the empty
// state. Exported from this white-box test file so the external test package
// (writer_test) can isolate registry mutations: the registry is package-global
// and `go test -count=N` reuses the process (package state is NOT re-initialised
// between iterations), so a test that calls Register must reset first or a later
// iteration panics on a duplicate Name. Test-only, and only from a test that
// runs alone: it replaces the table rather than storing into it, which nothing
// may race.
func ResetForTest() {
	//: a fresh, empty table; Lookup then reports empty.
	registry = plugin.Registry[Name, Factory]{}
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
