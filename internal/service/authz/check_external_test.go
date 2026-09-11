package authz_test

import (
	"errors"
	"testing"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcauthz "github.com/kitsunium/sdk/internal/service/authz"
)

// fieldValue reads one diagnostic field off a refusal, or "" when absent.
func fieldValue(err error, key string) string {
	//: fields are the log-only side; the test reads them the way an operator would.
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}

// TestOnlyAllowReturnsNil is the closure, stated as a table. Three different
// situations arrive and exactly one of them is a permission.
func TestOnlyAllowReturnsNil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		policy  coreauthz.Policy
		wantNil bool
		outcome string
	}{
		{"allow permits", constant(coreauthz.Allow), true, ""},
		{"deny refuses", constant(coreauthz.Deny), false, "deny"},
		{"abstain refuses", constant(coreauthz.Abstain), false, "abstain"},
		{"a nil policy refuses", nil, false, "unevaluable"},
		{"an error refuses", failing(errors.New("store down")), false, "unevaluable"},
		{"a corrupt decision refuses", constant(coreauthz.Decision(7)), false, "unevaluable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := svcauthz.Check(t.Context(), tc.policy, anyRequest())
			if tc.wantNil {
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
				return
			}
			if !errs.HasCode(err, coreauthz.CodePermissionDenied) {
				t.Fatalf("Check = %v, want PERMISSION_DENIED", err)
			}
			if got := fieldValue(err, "outcome"); got != tc.outcome {
				t.Fatalf("outcome field = %q, want %q", got, tc.outcome)
			}
		})
	}
}

// TestEveryRefusalRendersTheSameSentence is the security property at the exit
// of the domain. The three causes are distinguishable to the operator through
// fields and indistinguishable to the caller through Error() and Public.
func TestEveryRefusalRendersTheSameSentence(t *testing.T) {
	t.Parallel()
	refusals := []error{
		svcauthz.Check(t.Context(), constant(coreauthz.Deny), anyRequest()),
		svcauthz.Check(t.Context(), constant(coreauthz.Abstain), anyRequest()),
		svcauthz.Check(t.Context(), nil, anyRequest()),
		svcauthz.Check(t.Context(), failing(coreauthz.AttributeMissing), anyRequest()),
	}
	wantMessage := refusals[0].Error()
	wantPublic := errs.PublicOf(refusals[0])
	for index, err := range refusals[1:] {
		if got := err.Error(); got != wantMessage {
			t.Errorf("refusal %d renders %q, want %q — the wire form must not vary",
				index+1, got, wantMessage)
		}
		if got := errs.PublicOf(err); got != wantPublic {
			t.Errorf("refusal %d public = %q, want %q", index+1, got, wantPublic)
		}
		if status := errs.HTTPStatusOf(err); status != 403 {
			t.Errorf("refusal %d status = %d, want 403", index+1, status)
		}
	}
}

// TestTheDiagnosisSurvivesInTheFields is the other half: uniform on the wire,
// specific in the log. An operator must be able to tell a missing attribute
// from a plain refusal without the caller being able to.
func TestTheDiagnosisSurvivesInTheFields(t *testing.T) {
	t.Parallel()
	request := coreauthz.NewRequestValue("u-9", "publish", "article")
	err := svcauthz.Check(t.Context(), failing(coreauthz.AttributeMissing), request)
	//: a slice rather than a map: the keys are field NAMES, so an int-keyed
	//: map cannot express them, and a slice reports in a stable order.
	wants := []struct {
		key  string
		want string
	}{
		{"outcome", "unevaluable"},
		{"subject", "u-9"},
		{"action", "publish"},
		{"resource", "article"},
		{"cause_reason", "ATTRIBUTE_MISSING"},
		{"cause_code", "0.2.26.2"},
	}
	//: every field is checked; one miss does not hide the others.
	for _, tc := range wants {
		if got := fieldValue(err, tc.key); got != tc.want {
			t.Errorf("field %q = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestTheCauseDoesNotHijackTheCode is origin-wins, asserted where it matters.
//
// Wrapping the other way round would let an AttributeMissing from a condition
// become the code a framework routes on AND the message the client sees — the
// exact moment a refusal starts explaining which attribute to forge next.
func TestTheCauseDoesNotHijackTheCode(t *testing.T) {
	t.Parallel()
	err := svcauthz.Check(t.Context(), failing(coreauthz.AttributeKindMismatch), anyRequest())
	code, ok := errs.CodeOf(err)
	if !ok || code != coreauthz.CodePermissionDenied {
		t.Fatalf("code = %v (ok=%v), want CodePermissionDenied", code, ok)
	}
	if reason, _ := errs.ReasonOf(err); reason != "PERMISSION_DENIED" {
		t.Fatalf("reason = %q, want PERMISSION_DENIED", reason)
	}
}

// TestAForeignCauseIsStillReported covers a caller-written policy returning an
// ordinary error: it has no code and no reason, so the message is used and the
// code field says so explicitly rather than being empty.
func TestAForeignCauseIsStillReported(t *testing.T) {
	t.Parallel()
	err := svcauthz.Check(t.Context(), failing(errors.New("ldap timeout")), anyRequest())
	if got := fieldValue(err, "cause_reason"); got != "ldap timeout" {
		t.Errorf("cause_reason = %q, want the foreign error's message", got)
	}
	if got := fieldValue(err, "cause_code"); got != "-" {
		t.Errorf("cause_code = %q, want %q for an error with no code", got, "-")
	}
}

// TestAPolicyMisconfigurationCarriesAnExitCode pins the start-up shape: a
// composition that can only refuse should be able to stop a process with
// EX_CONFIG rather than looking like a runtime denial forever.
func TestAPolicyMisconfigurationCarriesAnExitCode(t *testing.T) {
	t.Parallel()
	if got := errs.ExitCodeOf(coreauthz.PolicyMisconfigured); got != 78 {
		t.Fatalf("exit code = %d, want 78 (EX_CONFIG)", got)
	}
}
