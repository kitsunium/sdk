package entitlement

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// wrap.go is a COPY of internal/service/entitlement's, because that package is
// a separate Go module and its helpers are unexported. A copy is only honest if
// both sides are pinned to the same behaviour, so these are the same three
// assertions its twin carries — run here against this package's own build.

// ownError is an errs-typed error from a code range this SDK does not own,
// standing in for whatever a caller's ssh.Signer decides to return.
var ownError = errs.Wrap(nil, errs.WrapParams{
	Code:    0x00_03_30_02,
	Reason:  "CALLER_FAILURE",
	Public:  "the caller's own implementation refused",
	Private: "caller: something of its own",
})

// Test_classify pins the two properties every converted call site rests on:
// the cause survives errors.Is, and a nil sentinel does not take the process
// down on the path that is already reporting a failure.
func Test_classify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// sentinel is what the call site names, possibly nothing.
		sentinel *errs.Error
		// wantCode is the code the result must carry.
		wantCode errs.Code
		reason   string
	}{
		{
			name:     "the sentinel's identity is read off it, never restated",
			sentinel: coreent.ErrNoPossession,
			wantCode: coreent.CodeNoPossession,
			reason:   "six call sites naming one sentinel must not be six copies of four strings",
		},
		{
			name:     "a nil sentinel degrades rather than panics",
			sentinel: nil,
			wantCode: errs.CodeInvalidWrapParams,
			reason:   "only a bug here produces one, and a panic on the reporting path is the worst outcome",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classify(tt.sentinel, fs.ErrNotExist, errs.String("stage", "read"))
			//: The stdlib cause must remain matchable, whatever we said about
			//: it: errors.Is(err, fs.ErrNotExist) is the thing a wrapper
			//: destroys most often.
			if !errors.Is(got, fs.ErrNotExist) {
				t.Errorf("classify() = %v, want errors.Is(fs.ErrNotExist) (%s)", got, tt.reason)
			}
			code, ok := errs.CodeOf(got)
			if !ok || code != tt.wantCode {
				t.Errorf("classify() code = %v/%v, want %v (%s)", code, ok, tt.wantCode, tt.reason)
			}
		})
	}
}

// Test_classifyForeign pins the seam where origin-wins is the WRONG rule.
//
// ProvePossession is exported and takes an ssh.Signer and an ssh.PublicKey the
// CALLER supplies. Those are not a deeper layer of this SDK — they are somebody
// else's package, free to return an error from its own code range — so letting
// one win means errors.Is stops finding ErrNoPossession.
func Test_classifyForeign(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// cause is what the caller's implementation handed back.
		cause  error
		reason string
	}{
		{
			name:   "an errs-typed cause does not take over the classification",
			cause:  ownError,
			reason: "a possession failure is a possession failure whoever's signer reported it",
		},
		{
			name:   "a plain cause takes the ordinary path untouched",
			cause:  fs.ErrNotExist,
			reason: "nothing can hijack an identity it does not have, so classify is left alone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classifyForeign(coreent.ErrNoPossession, tt.cause,
				errs.String("stage", "sign_challenge"))
			//: OUR sentinel is the identity, which is the whole point.
			if !errors.Is(got, coreent.ErrNoPossession) {
				t.Errorf("errors.Is(err, ErrNoPossession) = false, want true (%s)", tt.reason)
			}
			code, ok := errs.CodeOf(got)
			if !ok || code != coreent.CodeNoPossession {
				t.Errorf("CodeOf = %v/%v, want %v (%s)", code, ok, coreent.CodeNoPossession, tt.reason)
			}
			//: ...and the caller's error is not lost with it.
			if !errors.Is(got, tt.cause) {
				t.Errorf("errors.Is(err, cause) = false, want true (%s)", tt.reason)
			}
			//: The public sentence stays ours alone.
			if strings.Contains(got.Error(), tt.cause.Error()) {
				t.Errorf("err.Error() = %q, want the cause OUT of it (%s)", got, tt.reason)
			}
		})
	}
}

// Test_annotate pins the guard, which is not defensive.
//
// errs.Wrap has no spelling for "attach a field, decide nothing": zero
// WrapParams over a cause carrying no *errs.Error fails validateDefineArgs and
// returns CodeInvalidWrapParams, "internal wrap failure", with the caller's own
// refusal demoted to a cause nobody prints. Every error reaching this package's
// one annotate call site is currently one of ours, so the guard never fires
// today — it is here because the next caller's might not be, and because the
// twin in internal/service/entitlement reaches it through a PORT.
func Test_annotate(t *testing.T) {
	t.Parallel()

	plain := errors.New("a caller's own refusal")

	tests := []struct {
		name string
		// err is what a caller hands in.
		err error
		// wantSame is whether the result must be the identical error value.
		wantSame bool
		// wantField is whether the annotation must be readable back.
		wantField bool
		reason    string
	}{
		{
			name:      "an SDK error keeps its identity and gains the field",
			err:       coreent.ErrNoLicense,
			wantField: true,
			reason:    "origin wins: code, reason and both messages stay the cause's",
		},
		{
			name:     "a plain error is returned untouched",
			err:      plain,
			wantSame: true,
			reason:   "the alternative is replacing a caller's refusal with INVALID_WRAP_PARAMS",
		},
		{
			name:     "a nil error stays nil",
			err:      nil,
			wantSame: true,
			reason:   "annotating a success would invent a failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := annotate(tt.err, errs.String("subject", "a-subject"))
			//: A same-value input must come back identical, not merely
			//: matchable: INVALID_WRAP_PARAMS keeps the cause and still
			//: destroys what the caller prints.
			if tt.wantSame && got != tt.err {
				t.Fatalf("annotate() = %v, want the identical value back (%s)", got, tt.reason)
			}
			if !tt.wantField {
				return
			}
			if !errors.Is(got, tt.err) {
				t.Errorf("annotate() = %v, want errors.Is(%v) (%s)", got, tt.err, tt.reason)
			}
			if len(errs.FieldsOf(got)) == 0 {
				t.Errorf("annotate() carries no fields (%s)", tt.reason)
			}
		})
	}
}
