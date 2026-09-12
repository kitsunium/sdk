// Package gate — the policy's own suite.
package gate

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// shipped is a policy shaped like a real one: a bare root and three commands
// exempt by name, two capabilities exempt wholesale, and the two commands that
// repair a refused entitlement named as recovery.
func shipped() *PolicyValue {
	return &PolicyValue{
		ExemptExact:      []string{"", "version", "help", "upgrade", "skill"},
		ExemptSubtree:    []string{"completion", "license"},
		RecoveryPaths:    []string{"upgrade", "license create"},
		OnUpdateRequired: UpdateApply,
	}
}

// TestPolicyValue_Exempt pins the distinction one list could not make, and the
// prefix trap that a naive subtree match walks straight into.
func TestPolicyValue_Exempt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   []string
		want   bool
		reason string
	}{
		{
			name: "the bare root shows help", path: nil, want: true,
			reason: "showing help must not require an entitlement",
		},
		{
			name: "an empty slice is the bare root too", path: []string{}, want: true,
			reason: "nil and empty are the same invocation",
		},
		{
			//: The shape a caller coming from a space-joined string produces:
			//: strings.Split("", " ") is [""], not []. It must be the bare root
			//: too, or every such consumer's root invocation is gated.
			name: "a single empty element is the bare root as well",
			path: []string{""}, want: true,
			reason: "strings.Split of an empty path yields one empty element",
		},
		{
			name: "a named exact exemption", path: []string{"version"}, want: true,
			reason: "version carries nothing to protect",
		},
		{
			name: "a gated command", path: []string{"lint"}, want: false,
			reason: "the command that does the work is what the gate is for",
		},
		{
			//: THE case one list cannot express. `skill` bare prints install
			//: guidance; `skill install` does real work.
			name: "an exact exemption does not cover its children",
			path: []string{"skill", "install"}, want: false,
			reason: "ExemptExact matches a WHOLE path, so a child is not exempted with its parent",
		},
		{
			name: "a subtree covers its root", path: []string{"license"}, want: true,
			reason: "the capability is exempt, not just one command under it",
		},
		{
			name: "a subtree covers its children",
			path: []string{"license", "create"}, want: true,
			reason: "repairing a licence is what you do when the licence is what is broken",
		},
		{
			name: "a subtree covers a grandchild",
			path: []string{"license", "key", "rotate"}, want: true,
			reason: "wholesale means wholesale",
		},
		{
			//: The trap. A byte prefix would exempt "licensed" from "license",
			//: which is a gated command silently running unchecked.
			name: "a sibling sharing a prefix is NOT covered",
			path: []string{"licensed"}, want: false,
			reason: "descending is a prefix on a SEPARATOR boundary, never on bytes",
		},
		{
			name: "nor is a longer sibling with a gated child",
			path: []string{"licensedaemon", "start"}, want: false,
			reason: "same boundary rule, one level down",
		},
		{
			name: "a same-named command elsewhere in the tree is not exempt",
			path: []string{"config", "version"}, want: false,
			reason: "matching by name alone would exempt every command called version",
		},
	}
	//: one row per shape the matcher must get right.
	for _, tt := range tests {
		//: each shape is its own subtest, so a failure names the path.
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: the matcher's whole contract is this one answer.
			if got := shipped().Exempt(tt.path); got != tt.want {
				t.Errorf("Exempt(%q) = %v, want %v — %s",
					strings.Join(tt.path, " "), got, tt.want, tt.reason)
			}
		})
	}
}

// TestPolicyValue_ExemptNil pins that a nil policy exempts nothing, which is
// the refusing direction and the one a caller who configured nothing gets.
func TestPolicyValue_ExemptNil(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path []string
	}{
		{name: "the bare root", path: nil},
		{name: "a named command", path: []string{"version"}},
	}
	for _, tt := range tests {
		//: one row per shape a caller might ask a nil policy about.
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var policy *PolicyValue
			//: it must answer, not panic — this is the path that runs when a
			//: caller has configured nothing at all.
			if policy.Exempt(tt.path) {
				t.Errorf("a nil policy exempted %q; nothing is exempt without a policy",
					strings.Join(tt.path, " "))
			}
		})
	}
}

// TestPolicyValue_Validate pins the two faults that are lockouts rather than
// typos, and that every fault is reported in one pass.
func TestPolicyValue_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		policy   *PolicyValue
		wantOK   bool
		wantSubs []string
		reason   string
	}{
		{
			name: "a shipped policy", policy: shipped(), wantOK: true,
			reason: "the shape a real product uses must pass",
		},
		{
			name:   "a nil policy",
			policy: nil, wantSubs: []string{"no policy was supplied"},
			reason: "a gate nobody configured fails the same way as an empty one",
		},
		{
			//: THE lockout. The policy looks complete; the exemption list is
			//: simply missing the one entry that matters.
			name: "a recovery command that is gated",
			policy: &PolicyValue{
				ExemptExact:      []string{"version"},
				RecoveryPaths:    []string{"license create"},
				OnUpdateRequired: UpdateRefuse,
			},
			wantSubs: []string{"recovery path", "could not run the command that repairs it"},
			reason:   "a machine whose entitlement lapsed would have no path back",
		},
		{
			name: "an unset update action",
			policy: &PolicyValue{
				ExemptExact:      []string{"upgrade"},
				RecoveryPaths:    []string{"upgrade"},
				OnUpdateRequired: 0,
			},
			wantSubs: []string{"OnUpdateRequired is unset"},
			reason:   "refusing and upgrading are opposite answers; neither is a guess",
		},
		{
			name:     "no exemption at all",
			policy:   &PolicyValue{OnUpdateRequired: UpdateRefuse},
			wantSubs: []string{"no command is exempt"},
			reason:   "every invocation would need an entitlement, including the ones that repair it",
		},
		{
			//: Joined, because a policy corrected one refusal at a time takes
			//: as many start-ups as it has faults.
			name:   "every fault at once",
			policy: &PolicyValue{RecoveryPaths: []string{"upgrade"}},
			wantSubs: []string{
				"OnUpdateRequired is unset",
				"recovery path",
				"no command is exempt",
			},
			reason: "one start-up reports the whole list",
		},
	}
	for _, tt := range tests {
		//: one row per fault, plus the joined case and the passing one.
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.policy.Validate()
			//: a usable policy returns a genuine nil.
			if tt.wantOK {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil — %s", err, tt.reason)
				}

				return
			}
			//: every other row must refuse.
			if err == nil {
				t.Fatalf("Validate() = nil, want a refusal — %s", tt.reason)
			}
			//: and carry the one code this domain mints, so a caller can
			//: branch on it rather than on the message.
			if !errs.HasCode(err, CodePolicyInvalid) {
				t.Errorf("Validate() error carries no CodePolicyInvalid: %v", err)
			}
			message := errs.PrivateOf(err)
			//: each expected fault must be named; a joined error that reports
			//: only the first is the defect this row exists for.
			for _, want := range tt.wantSubs {
				if !strings.Contains(message, want) {
					t.Errorf("Validate() private detail does not mention %q: %s", want, message)
				}
			}
		})
	}
}
