package session_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/session"
)

// storeType is the published port under audit.
var storeType = reflect.TypeFor[session.Store]()

// TestRegenerateIsTheOnlyWayToNameASubject is the executable form of this
// domain's central security claim.
//
// Session fixation is prevented here by ABSENCE: the only [session.Store]
// method that accepts a subject is Regenerate, and Regenerate always mints a
// new identifier. A future contributor who adds the convenient-looking
// `SetSubject(ctx, id, subject)` re-opens the hole, and this test is what
// stops them — a comment saying "don't" would not have.
//
// The check is "no method but Regenerate takes a string", which is mechanical
// and exact: Load and Destroy take an ID (a struct), Save takes a
// SessionValue, New takes only a context.
func TestRegenerateIsTheOnlyWayToNameASubject(t *testing.T) {
	t.Parallel()
	for _, tc := range storeMethods() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Regenerate is the one method that MUST take a subject.
			if tc.name == "Regenerate" {
				//: its absence would mean the subject-binding call has moved.
				if !tc.takesString {
					t.Fatal("Regenerate no longer takes a subject — the only subject-binding call has moved")
				}
				return
			}
			//: anything else taking a string is a second way in.
			if tc.takesString {
				t.Fatalf("%s accepts a string: if that is a subject, it is a second way to "+
					"bind a principal without rotating the identifier, which is session fixation",
					tc.name)
			}
		})
	}
}

// methodCase is one Store method reduced to the two facts the fixation guard
// judges, so the subtest closure captures neither a reflect.Method nor a
// reflect.Type.
type methodCase struct {
	name        string
	takesString bool
}

// storeMethods reduces the port's method set to the guard's table.
func storeMethods() []methodCase {
	cases := make([]methodCase, 0, storeType.NumMethod())
	for method := range storeType.Methods() {
		cases = append(cases, methodCase{
			name: method.Name, takesString: methodTakesString(method.Type),
		})
	}
	//: one row per published method.
	return cases
}

// TestStoreMethodSetIsFrozen pins the ADR 0039 rule for this port.
//
// pkg/v1/session aliases Store, so its method set is published. Go interfaces
// are structural: adding a sixth method breaks every downstream implementer at
// compile time, with no deprecation window and no way to find them first. New
// capabilities arrive as SIBLING interfaces reached by type assertion —
// [session.Sweeper] is the first, and io.Closer on the file store is the
// second, which needed no new declaration at all.
func TestStoreMethodSetIsFrozen(t *testing.T) {
	t.Parallel()
	want := []string{"Destroy", "Load", "New", "Regenerate", "Save"}
	got := make([]string, 0, storeType.NumMethod())
	for method := range storeType.Methods() {
		got = append(got, method.Name)
	}
	//: reflect reports interface methods in sorted order.
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Store method set = %v, want %v — a published port is extended by a "+
			"sibling interface (ADR 0039), never by widening", got, want)
	}
}

// TestSessionValueHasNoSubjectMutator is the other half of the fixation guard.
// The port cannot be the only thing checked: a WithSubject on the value type
// would let a caller build the forged session that Save then has to catch at
// runtime, and the point of this design is that the mistake is not writable.
func TestSessionValueHasNoSubjectMutator(t *testing.T) {
	t.Parallel()
	valueType := reflect.TypeFor[session.SessionValue]()
	for method := range valueType.Methods() {
		//: the reader is the one method allowed to mention the subject.
		if !strings.Contains(method.Name, "Subject") || method.Name == "Subject" {
			continue
		}
		t.Errorf("SessionValue.%s mentions Subject: the subject is written by "+
			"Store.Regenerate and by nothing else", method.Name)
	}
	//: and the reader must stay a reader — no arguments, one string out.
	reader, found := valueType.MethodByName("Subject")
	//: its disappearance would break every consumer reading a principal.
	if !found {
		t.Fatal("SessionValue.Subject is gone")
	}
	//: one receiver in, one string out.
	if reader.Type.NumIn() != 1 || reader.Type.NumOut() != 1 {
		t.Errorf("SessionValue.Subject has signature %v, want a pure reader", reader.Type)
	}
}

// TestSweeperIsASeparateInterface pins that the sweep capability lives beside
// Store rather than inside it. Folding it in would be the exact widening ADR
// 0039 forbids, and it would also force every third-party store to implement a
// sweep it may have no way to perform.
func TestSweeperIsASeparateInterface(t *testing.T) {
	t.Parallel()
	sweeperType := reflect.TypeFor[session.Sweeper]()
	//: one method, and it is the one the sibling exists for.
	if sweeperType.NumMethod() != 1 || sweeperType.Method(0).Name != "Sweep" {
		t.Fatalf("Sweeper method set changed: %v", sweeperType)
	}
	//: Store must NOT satisfy Sweeper, or the sibling has been folded in.
	if storeType.Implements(sweeperType) {
		t.Error("Store implements Sweeper: the sibling has been folded into the published port")
	}
}

// methodTakesString reports whether any parameter of an interface method is a
// plain string.
func methodTakesString(signature reflect.Type) bool {
	for param := range signature.Ins() {
		//: Kind, not Type, so a named string type would be caught too.
		if param.Kind() == reflect.String {
			return true
		}
	}
	//: nothing in this signature can carry a subject.
	return false
}
