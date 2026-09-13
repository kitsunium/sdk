package entitlement

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// probeNow is the instant every roster case in this file is evaluated at.
var probeNow = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

// fieldOf reads one structured field back off an error, or "" when the error
// does not carry it.
//
// Every assertion below that used to be readable out of err.Error() reads it
// here instead: the public sentence is the wire-safe half and deliberately
// names no account, no uuid and no deadline.
func fieldOf(err error, key string) string {
	//: Scan the fields origin-wins concatenated onto this error.
	for _, field := range errs.FieldsOf(err) {
		//: The first match wins; no call site here repeats a key.
		if field.Key() == key {
			return field.StringValue()
		}
	}
	//: Absent, which every caller reports as a failed assertion.
	return ""
}

// TestNoParticularReachesThePublicSentence pins BOTH directions of the split
// on every refusal this file can raise: the particular must be absent from
// err.Error() and present in the fields.
//
// Both halves are load-bearing. Leaking the identifier is the defect this
// change exists to close — a refusal that names the account it refused
// confirms that account to whoever presented it, one probe at a time. Losing
// it is the defect nobody notices until an operator is staring at a refusal
// that says nothing they can act on. A test asserting only the first half
// passes on an implementation that drops the fact entirely.
func TestNoParticularReachesThePublicSentence(t *testing.T) {
	t.Parallel()

	expired := probeNow.Add(-time.Hour)

	tests := []struct {
		name string
		// raise produces the refusal under test.
		raise func() error
		// particular is the identifier that must NOT be in the sentence.
		particular string
		// key is the field that must carry it instead.
		key    string
		reason string
	}{
		{
			name: "an account the roster does not list",
			raise: func() error {
				roster := &RosterValue{CIAccounts: map[string]CIEntitlementValue{"7": {}}}
				_, err := roster.CIEntitlementFor("4242", probeNow)

				return err
			},
			particular: "4242",
			key:        "account_id",
			reason:     "the account id was in the sentence as \"account %s\" until this change",
		},
		{
			name: "an account whose seat has expired",
			raise: func() error {
				roster := &RosterValue{CIAccounts: map[string]CIEntitlementValue{"4242": {ExpiresAt: expired}}}
				_, err := roster.CIEntitlementFor("4242", probeNow)

				return err
			},
			particular: "4242",
			key:        "account_id",
			reason:     "the expiry refusal named the account too",
		},
		{
			name: "the deadline an expired seat closed at",
			raise: func() error {
				roster := &RosterValue{CIAccounts: map[string]CIEntitlementValue{"4242": {ExpiresAt: expired}}}
				_, err := roster.CIEntitlementFor("4242", probeNow)

				return err
			},
			particular: "2026-09-13T11:00:00Z",
			key:        "expired_at",
			reason:     "a refusal that dates the licence describes it to the party being refused",
		},
		{
			name: "a subject absent from the roster",
			raise: func() error {
				roster := &RosterValue{Subjects: map[string]SubjectValue{"other": {Fingerprint: "SHA256:x"}}}
				_, err := roster.SubjectFor("6f1c8a2e-0000-4000-8000-000000000001")

				return err
			},
			particular: "6f1c8a2e-0000-4000-8000-000000000001",
			key:        "subject",
			reason:     "a revocation quoting the uuid back confirms it to whoever presented it",
		},
		{
			name: "a subject listed with no fingerprint",
			raise: func() error {
				roster := &RosterValue{Subjects: map[string]SubjectValue{"6f1c8a2e-0000-4000-8000-000000000001": {}}}
				_, err := roster.SubjectFor("6f1c8a2e-0000-4000-8000-000000000001")

				return err
			},
			particular: "6f1c8a2e-0000-4000-8000-000000000001",
			key:        "subject",
			reason:     "the broken-publisher case reports as a revocation and must not leak either",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.raise()
			//: A refusal is the whole point; a nil here would make both
			//: assertions below vacuously true.
			if err == nil {
				t.Fatalf("want a refusal, got nil (%s)", tt.reason)
			}
			//: Direction one — out of the sentence anyone may see.
			if strings.Contains(err.Error(), tt.particular) {
				t.Errorf("err.Error() = %q, want %q OUT of it (%s)", err, tt.particular, tt.reason)
			}
			//: Direction two — still reachable by the operator who is owed it.
			if got := fieldOf(err, tt.key); got != tt.particular {
				t.Errorf("field %q = %q, want %q (%s)", tt.key, got, tt.particular, tt.reason)
			}
		})
	}
}

// TestCIEntitlementForRefusalsStayDistinguishable pins that the four ways one
// sentinel is reached remain tellable apart.
//
// They all render the same sentence now, which is the point — and it is also
// the risk. "No roster at all", "no account id", "this account has no seat"
// and "the seat has expired" are four different things to fix and one string
// to read, so the condition field is the only thing left that separates them.
// The same merge in a sibling package printed three unrelated CI failures
// identically until a field was added back (PR #200, ciContext).
func TestCIEntitlementForRefusalsStayDistinguishable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// raise produces the refusal under test.
		raise func() error
		// wantCondition is the exact condition field expected.
		wantCondition string
	}{
		{
			name: "no roster at all",
			raise: func() error {
				var roster *RosterValue
				_, err := roster.CIEntitlementFor("4242", probeNow)

				return err
			},
			wantCondition: "no roster to check against",
		},
		{
			name: "no account id to look up",
			raise: func() error {
				roster := &RosterValue{CIAccounts: map[string]CIEntitlementValue{"4242": {}}}
				_, err := roster.CIEntitlementFor("", probeNow)

				return err
			},
			wantCondition: "no account id to match",
		},
		{
			name: "an account the roster does not list",
			raise: func() error {
				roster := &RosterValue{CIAccounts: map[string]CIEntitlementValue{"7": {}}}
				_, err := roster.CIEntitlementFor("4242", probeNow)

				return err
			},
			wantCondition: "no entry for this account id",
		},
		{
			name: "a seat whose term has closed",
			raise: func() error {
				roster := &RosterValue{CIAccounts: map[string]CIEntitlementValue{
					"4242": {ExpiresAt: probeNow.Add(-time.Hour)},
				}}
				_, err := roster.CIEntitlementFor("4242", probeNow)

				return err
			},
			wantCondition: "the account's CI seat has expired",
		},
	}

	//: Collecting the sentences proves the merge rather than assuming it.
	sentences := make(map[string]struct{}, len(tests))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.raise()
			//: Every one of the four is a refusal.
			if !errors.Is(err, ErrCINotEntitled) {
				t.Fatalf("error = %v, want ErrCINotEntitled", err)
			}
			//: The stage names the operation for a log query.
			if got := fieldOf(err, "stage"); got != "ci_entitlement" {
				t.Errorf("stage = %q, want %q", got, "ci_entitlement")
			}
			//: And the condition names which of the four happened.
			if got := fieldOf(err, "condition"); got != tt.wantCondition {
				t.Errorf("condition = %q, want %q", got, tt.wantCondition)
			}
			sentences[err.Error()] = struct{}{}
		})
	}
	//: The byte-identical sentence is the deliberate half of the design; if
	//: this ever splits, a refusal has started describing the roster again.
	if len(sentences) != 1 {
		t.Errorf("the four refusals rendered %d distinct sentences, want 1: %v", len(sentences), sentences)
	}
}

// TestSubjectForSeparatesAWithdrawalFromABrokenPublisher pins the field that
// tells the two apart.
//
// Both report ErrRevoked and must keep doing so — a subject the roster does
// not vouch for is not entitled either way, and the doc comment on SubjectFor
// says why the merge is deliberate. But a roster that LISTS a subject with an
// empty fingerprint is a publisher that shipped a broken document, and telling
// that operator "your entitlement has been revoked" sends them to the vendor
// for something only the vendor's own build can have caused.
func TestSubjectForSeparatesAWithdrawalFromABrokenPublisher(t *testing.T) {
	t.Parallel()

	const uuid string = "6f1c8a2e-0000-4000-8000-000000000001"

	tests := []struct {
		name string
		// subjects is the roster's subject table.
		subjects map[string]SubjectValue
		// wantCondition is the exact condition field expected.
		wantCondition string
		reason        string
	}{
		{
			name:          "absent from the table",
			subjects:      map[string]SubjectValue{"other": {Fingerprint: "SHA256:x"}},
			wantCondition: "no entry for this uuid",
			reason:        "a withdrawal and a never-approved enrolment look identical here",
		},
		{
			name:          "listed with an empty fingerprint",
			subjects:      map[string]SubjectValue{uuid: {}},
			wantCondition: "the entry carries no fingerprint",
			reason:        "that is a broken publisher, not a withdrawal",
		},
		{
			name:          "a nil subject table",
			subjects:      nil,
			wantCondition: "no entry for this uuid",
			reason:        "a roster with no subjects at all vouches for nobody",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			roster := &RosterValue{Subjects: tt.subjects}

			_, err := roster.SubjectFor(uuid)

			//: One sentinel covers both, deliberately.
			if !errors.Is(err, ErrRevoked) {
				t.Fatalf("SubjectFor() error = %v, want ErrRevoked (%s)", err, tt.reason)
			}
			//: And the field is what separates them.
			if got := fieldOf(err, "condition"); got != tt.wantCondition {
				t.Errorf("condition = %q, want %q (%s)", got, tt.wantCondition, tt.reason)
			}
			//: The stage names the operation for a log query.
			if got := fieldOf(err, "stage"); got != "subject_lookup" {
				t.Errorf("stage = %q, want %q (%s)", got, "subject_lookup", tt.reason)
			}
		})
	}
}

// TestSubjectForIsTotalOnANilRoster pins that the device lookup refuses a nil
// roster instead of dereferencing it.
//
// CIEntitlementFor has guarded this since it was written, and says why in a
// comment; SubjectFor panicked. Nothing inside this repository reaches it —
// every internal caller checks the error that accompanies a nil roster — but
// Roster is aliased into pkg/v1/entitlement, so a consumer holding a nil
// roster got a panic from one method of the pair and a refusal from the other.
//
// Observed before the guard:
//
//	--- FAIL: TestSubjectForIsTotalOnANilRoster
//	    panic: runtime error: invalid memory address or nil pointer dereference
//	    [recovered]
func TestSubjectForIsTotalOnANilRoster(t *testing.T) {
	t.Parallel()

	const uuid string = "6f1c8a2e-0000-4000-8000-000000000001"

	var roster *RosterValue

	got, err := roster.SubjectFor(uuid)

	//: Fail closed: no roster vouches for nobody, never for everybody.
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("SubjectFor() error = %v, want ErrRevoked", err)
	}
	//: The empty record is what every caller compares a fingerprint against,
	//: and a non-empty one here would match a key nobody published.
	if got != (SubjectValue{}) {
		t.Errorf("SubjectFor() = %+v, want the zero SubjectValue", got)
	}
	//: Distinguishable from a subject the roster simply omits — the remedy
	//: for one is enrolment and for the other is a caller's own bug.
	if cond := fieldOf(err, "condition"); cond != "no roster to check against" {
		t.Errorf("condition = %q, want %q", cond, "no roster to check against")
	}
	//: And still no uuid in the sentence.
	if strings.Contains(err.Error(), uuid) {
		t.Errorf("err.Error() = %q, want the uuid OUT of it", err)
	}
}

// TestRefusalsKeepTheirSentinelIdentity pins the half a caller's exit-code
// table reads, across the wrappers this repository actually applies.
//
// The conversion replaced fmt.Errorf("%w: …", sentinel) with
// errs.Wrap(sentinel, errs.WrapParams{}, …). Both put the sentinel where
// errors.Is finds it, and the second additionally makes Code, Reason, Public,
// Private and the exit status survive — but only as long as nothing above
// re-wraps in a way that lets origin-wins pick a different origin. The
// errors.Join row is the shape PR #200's 130-scenario probe never fed in: an
// errs-typed error from ANOTHER range underneath, with an identity to hijack.
func TestRefusalsKeepTheirSentinelIdentity(t *testing.T) {
	t.Parallel()

	// elsewhere stands in for a consumer's own error, from a range this
	// package does not own. 0.3.64.* is the ownership audit's own fixture
	// range and belongs to no production package; the audit skips _test.go
	// files anyway, so nothing here can squat on a real allocation.
	elsewhere := errs.Define(0x00_03_40_01, "SOMEBODY_ELSES",
		"somebody else's failure", "test: an error from outside this domain")

	roster := &RosterValue{
		CIAccounts: map[string]CIEntitlementValue{"7": {}},
		Subjects:   map[string]SubjectValue{"other": {Fingerprint: "SHA256:x"}},
	}

	tests := []struct {
		name string
		// raise produces the refusal under test.
		raise func() error
		// want is the sentinel the refusal must keep answering to.
		want *errs.Error
		// wantCode is the dotted-quad a caller's table dispatches on.
		wantCode errs.Code
	}{
		{
			name: "an unentitled CI account",
			raise: func() error {
				_, err := roster.CIEntitlementFor("4242", probeNow)

				return err
			},
			want:     ErrCINotEntitled,
			wantCode: CodeCINotEntitled,
		},
		{
			name: "a subject the roster does not vouch for",
			raise: func() error {
				_, err := roster.SubjectFor("6f1c8a2e-0000-4000-8000-000000000001")

				return err
			},
			want:     ErrRevoked,
			wantCode: CodeRevoked,
		},
	}

	wrappers := []struct {
		name string
		// wrap is one shape a caller applies above this package.
		wrap func(error) error
	}{
		{name: "raw", wrap: func(err error) error { return err }},
		{
			name: "annotated with a field",
			wrap: func(err error) error {
				return errs.Wrap(err, errs.WrapParams{}, errs.String("ci_refusal", "the seat was refused too"))
			},
		},
		{
			name: "joined under a consumer's own errs-typed error",
			wrap: func(err error) error {
				return errs.Wrap(errors.Join(err, elsewhere), errs.WrapParams{})
			},
		},
	}

	for _, tt := range tests {
		for _, w := range wrappers {
			t.Run(tt.name+"/"+w.name, func(t *testing.T) {
				t.Parallel()

				err := w.wrap(tt.raise())

				//: The sentinel is what every dispatch downstream matches on.
				if !errors.Is(err, tt.want) {
					t.Errorf("errors.Is(err, %v) = false, want true — err = %v", tt.want, err)
				}
				code, ok := errs.CodeOf(err)
				//: The code is what a caller's exit table indexes by.
				if !ok || code != tt.wantCode {
					t.Errorf("CodeOf() = %s/%v, want %s/true", code, ok, tt.wantCode)
				}
				//: The wire-safe sentence is the sentinel's own, unchanged by
				//: any of the three wrappers.
				if got := errs.PublicOf(err); got != errs.PublicOf(tt.want) {
					t.Errorf("PublicOf() = %q, want %q", got, errs.PublicOf(tt.want))
				}
			})
		}
	}
}
