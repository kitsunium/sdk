package crypto

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_neutralizeAgreementCause is the V21 white-box guard: a scheme *errs.Error
// is collapsed to the opaque agreementSchemeFault so errs.Wrap cannot inherit
// its origin, while a plain stdlib cause passes through untouched.
func Test_neutralizeAgreementCause(t *testing.T) {
	t.Parallel()
	//: a scheme typed error that would win origin under errs.Wrap.
	schemeErr := errs.Wrap(stubFault{}, errs.WrapParams{
		Code:    CodeInvalidKey,
		Reason:  "INVALID_KEY",
		Public:  "leaky public",
		Private: "leaky private",
	})
	tests := []struct {
		name       string
		cause      error
		wantOpaque bool
	}{
		{"scheme *errs.Error collapses to opaque fault", schemeErr, true},
		{"plain stdlib cause passes through", stubFault{}, false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := neutralizeAgreementCause(c.cause)
			//: an *errs.Error cause must NOT survive — it would leak under wrap.
			if _, isSDK := errs.CodeOf(got); isSDK {
				t.Errorf("neutralizeAgreementCause returned an *errs.Error cause: %v", got)
			}
			//: the opaque path replaces the cause with the redacted marker.
			if c.wantOpaque && got.Error() != "agreement scheme fault (cause withheld of key bytes)" {
				t.Errorf("opaque marker mismatch: %q", got.Error())
			}
			//: the pass-through path keeps the original stdlib cause intact.
			if !c.wantOpaque && got != c.cause {
				t.Errorf("stdlib cause was altered: %v", got)
			}
		})
	}
}

// stubFault is a bare stdlib error for the neutralize white-box test; it avoids
// errs.Define so the AST audit never treats a fixture as a real sentinel.
type stubFault struct{}

func (stubFault) Error() string { return "stub fault" }

// Test_agreementSchemeFault_Error asserts the opaque fault renders a fixed
// redacted marker so no scheme diagnostic or key material rides out through the
// wrap chain (finding V21).
func Test_agreementSchemeFault_Error(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"renders the fixed redacted marker", "agreement scheme fault (cause withheld of key bytes)"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the marker must be constant regardless of the original scheme error.
			if got := (agreementSchemeFault{}).Error(); got != c.want {
				t.Errorf("agreementSchemeFault.Error()=%q want %q", got, c.want)
			}
		})
	}
}
