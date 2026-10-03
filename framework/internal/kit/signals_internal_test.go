package kit

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// Every signal kit ends one of its own steps with is an SDK error carrying its
// own code and reason (rule 2) — and is still told apart from the others by
// errors.Is, which is all its catcher asks. errs.New answers a malformed
// declaration with a validation error rather than a panic, so without this a
// typo in a reason would ship a signal with the wrong code and nothing would
// say so.
func TestKitSignalsAreTyped(t *testing.T) {
	signals := []struct {
		name   string
		err    error
		code   errs.Code
		reason string
	}{
		{"errErased", errErased, CodeSealErased, "SEAL_ERASED"},
		{"errNotCurrent", errNotCurrent, CodeSealKeyMoved, "SEAL_KEY_MOVED"},
		{"errReseal", errReseal, CodeResealInPlace, "RESEAL_IN_PLACE"},
		{"errRestMoved", errRestMoved, CodeResealMoved, "RESEAL_MOVED"},
		{"errNotInPlace", errNotInPlace, CodeWorkflowNotInPlace, "WORKFLOW_NOT_IN_PLACE"},
		{"errTransactionPanicked", errTransactionPanicked, CodeTransactionPanic, "TRANSACTION_PANICKED"},
		{"errHeldPanicked", errHeldPanicked, CodeTransactionPanic, "TRANSACTION_HELD_PANICKED"},
	}
	for _, s := range signals {
		if code, _ := errs.CodeOf(s.err); code != s.code {
			t.Errorf("%s carries %v, want %v", s.name, code, s.code)
		}
		if reason, _ := errs.ReasonOf(s.err); reason != s.reason {
			t.Errorf("%s carries the reason %q, want %q", s.name, reason, s.reason)
		}
		for _, other := range signals {
			if other.name != s.name && errors.Is(s.err, other.err) {
				t.Errorf("errors.Is(%s, %s) = true: a catcher would take one for the other", s.name, other.name)
			}
		}
	}
}
