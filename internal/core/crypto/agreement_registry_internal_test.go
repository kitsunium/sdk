package crypto

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// stubAgreement is a comparable in-package Agreement for white-box registry tests.
type stubAgreement struct{ name Algorithm }

// : compile-time proof the stub satisfies the Agreement port.
var _ Agreement = (*stubAgreement)(nil)

func (s stubAgreement) Algorithm() Algorithm { return s.name }

func (stubAgreement) GenerateKey() (pub, priv []byte, err error) { return nil, nil, nil }

func (stubAgreement) Shared(_, _ []byte) (secret []byte, err error) { return nil, nil }

func Test_cloneAgreementMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		src     map[Algorithm]Agreement
		insert  Algorithm
		wantLen int
	}{
		{"nil source yields a singleton map", nil, "a", 1},
		{"populated source is copied plus one", map[Algorithm]Agreement{"x": stubAgreement{"x"}}, "y", 2},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a nil source must stay a nil pointer; a populated one is addressed.
			var srcPtr *map[Algorithm]Agreement
			if c.src != nil {
				srcPtr = &c.src
			}
			got := cloneAgreementMap(srcPtr, c.insert, stubAgreement{c.insert})
			//: the clone carries every source entry plus the inserted one.
			if len(got) != c.wantLen {
				t.Errorf("len=%d want %d", len(got), c.wantLen)
			}
			//: the inserted entry must resolve in the freshly cloned map.
			if _, ok := got[c.insert]; !ok {
				t.Errorf("inserted %q missing from clone", c.insert)
			}
		})
	}
}

func Test_publishAgreement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		algo      Algorithm
		republish bool
	}{
		//: process-unique names so parallel rows never collide on the global.
		{"first publish under a free name succeeds", "puba-free", false},
		{"idempotent re-publish of the same scheme is a no-op", "puba-idem", true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the first publish of a free name must succeed.
			if err := publishAgreement(c.algo, stubAgreement{c.algo}); err != nil {
				t.Fatalf("first publishAgreement(%q): %v", c.algo, err)
			}
			//: non-republish rows stop here.
			if !c.republish {
				return
			}
			//: re-publishing the SAME scheme value is an idempotent no-op, no error.
			if err := publishAgreement(c.algo, stubAgreement{c.algo}); err != nil {
				t.Errorf("idempotent re-publishAgreement(%q): %v", c.algo, err)
			}
		})
	}
}

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
