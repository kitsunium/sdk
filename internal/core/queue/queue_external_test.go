package queue_test

import (
	"math"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// usableTimeout is a visibility timeout any of these cases can carry.
const usableTimeout time.Duration = 30 * time.Second

// TestPolicyRefusesTheZerosWhoseTwoReadingsAreOpposites pins ADR 0054 §D5's
// refusing half: a field whose zero could mean either of two contradictory
// things is refused, because whichever the SDK guessed would silently destroy
// the guarantee for half the callers who wrote it.
func TestPolicyRefusesTheZerosWhoseTwoReadingsAreOpposites(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		policy corequeue.PolicyValue
		field  string
	}{
		{"zero visibility timeout", corequeue.PolicyValue{MaxDeliveries: 3}, "VisibilityTimeout"},
		{
			"negative visibility timeout",
			corequeue.PolicyValue{VisibilityTimeout: -time.Second, MaxDeliveries: 3},
			"VisibilityTimeout",
		},
		{"zero max deliveries", corequeue.PolicyValue{VisibilityTimeout: usableTimeout}, "MaxDeliveries"},
		{
			"negative max deliveries",
			corequeue.PolicyValue{VisibilityTimeout: usableTimeout, MaxDeliveries: -1},
			"MaxDeliveries",
		},
		{
			"negative size bound",
			corequeue.PolicyValue{VisibilityTimeout: usableTimeout, MaxDeliveries: 1, MaxMessageBytes: -1},
			"MaxMessageBytes",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := testCase.policy.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want a refusal naming %q", testCase.field)
			}
			if !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
				t.Fatalf("Validate() code = %v, want CodeQueueMisconfigured", err)
			}
		})
	}
}

// TestPolicyAcceptsTheZerosWithOnlyOneReading is the other half of ADR 0031
// in the same struct, and it is asserted rather than described: a zero retry
// delay and a zero size bound each have exactly one sensible meaning, so
// there is nothing here to refuse.
func TestPolicyAcceptsTheZerosWithOnlyOneReading(t *testing.T) {
	t.Parallel()
	policy := corequeue.PolicyValue{VisibilityTimeout: usableTimeout, MaxDeliveries: 1}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a policy whose only zeros are the clamped ones", err)
	}
	normalized := policy.Normalized()
	if normalized.RetryDelay != 0 {
		t.Fatalf("RetryDelay = %v, want 0 — 'as soon as the lease lapses'", normalized.RetryDelay)
	}
	if normalized.MaxMessageBytes != corequeue.DefaultMaxMessageBytes {
		t.Fatalf("MaxMessageBytes = %d, want DefaultMaxMessageBytes %d",
			normalized.MaxMessageBytes, corequeue.DefaultMaxMessageBytes)
	}
}

// TestNormalizedReadsANegativeRetryDelayAsNoDelay pins the clamp rather than
// leaving it to the implementations to each remember.
func TestNormalizedReadsANegativeRetryDelayAsNoDelay(t *testing.T) {
	t.Parallel()
	policy := corequeue.PolicyValue{
		VisibilityTimeout: usableTimeout, MaxDeliveries: 1, RetryDelay: -time.Hour,
	}
	if got := policy.Normalized().RetryDelay; got != 0 {
		t.Fatalf("Normalized().RetryDelay = %v, want 0", got)
	}
}

// TestPolicyRefusesADeadlineOffsetPastTheCeiling pins MaxDeadlineOffset from
// both sides. Above it a deadline set today may not be representable as int64
// Unix nanoseconds — math.MaxInt64 certainly is not — and the durable broker
// would strand the message behind a name it cannot read back; at it, the
// policy is honoured, and a negative retry delay is still "no delay" rather
// than a refusal.
//
// Seen failing: with Validate's two ceiling checks removed, the refusing cases
// printed
//
//	Validate() = <nil>, want a refusal naming "VisibilityTimeout"
//	Validate() = <nil>, want a refusal naming "RetryDelay"
func TestPolicyRefusesADeadlineOffsetPastTheCeiling(t *testing.T) {
	t.Parallel()
	const past time.Duration = corequeue.MaxDeadlineOffset + time.Nanosecond
	cases := []struct {
		name   string
		policy corequeue.PolicyValue
		field  string // empty: the policy is accepted
	}{
		{"visibility timeout at the ceiling", corequeue.PolicyValue{
			VisibilityTimeout: corequeue.MaxDeadlineOffset, MaxDeliveries: 1,
		}, ""},
		{"visibility timeout one nanosecond past it", corequeue.PolicyValue{
			VisibilityTimeout: past, MaxDeliveries: 1,
		}, "VisibilityTimeout"},
		{"visibility timeout of math.MaxInt64", corequeue.PolicyValue{
			VisibilityTimeout: math.MaxInt64, MaxDeliveries: 1,
		}, "VisibilityTimeout"},
		{"retry delay at the ceiling", corequeue.PolicyValue{
			VisibilityTimeout: usableTimeout, RetryDelay: corequeue.MaxDeadlineOffset, MaxDeliveries: 1,
		}, ""},
		{"retry delay one nanosecond past it", corequeue.PolicyValue{
			VisibilityTimeout: usableTimeout, RetryDelay: past, MaxDeliveries: 1,
		}, "RetryDelay"},
		{"retry delay of math.MaxInt64", corequeue.PolicyValue{
			VisibilityTimeout: usableTimeout, RetryDelay: math.MaxInt64, MaxDeliveries: 1,
		}, "RetryDelay"},
		{"retry delay of math.MinInt64, which is still no delay", corequeue.PolicyValue{
			VisibilityTimeout: usableTimeout, RetryDelay: math.MinInt64, MaxDeliveries: 1,
		}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := testCase.policy.Validate()
			if testCase.field == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				if got := testCase.policy.Normalized().RetryDelay; got < 0 {
					t.Fatalf("Normalized().RetryDelay = %v, want a negative delay read as 0", got)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = <nil>, want a refusal naming %q", testCase.field)
			}
			if !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
				t.Fatalf("Validate() code = %v, want CodeQueueMisconfigured", err)
			}
			if got := fieldNamed(err); got != testCase.field {
				t.Fatalf("Validate() names field %q, want %q", got, testCase.field)
			}
		})
	}
}

// fieldNamed returns the "field" field a refusal carries.
func fieldNamed(err error) string {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == "field" {
			return field.StringValue()
		}
	}
	return ""
}

// TestMaxDeliveriesOfOneIsALegitimatePolicy guards against somebody "fixing"
// the off-by-one that is not one: Deliveries counts from 1, so a maximum of 1
// means one attempt and then the dead-letter store, which is a real
// configuration a caller may want.
func TestMaxDeliveriesOfOneIsALegitimatePolicy(t *testing.T) {
	t.Parallel()
	policy := corequeue.PolicyValue{VisibilityTimeout: usableTimeout, MaxDeliveries: 1}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil — one attempt then the DLQ is a policy", err)
	}
}

// TestEverySentinelCarriesAWireSafePublic enforces CLAUDE.md rule 4 on this
// package's own sentinels, and — the part that matters for a queue — that no
// Public mentions a payload.
func TestEverySentinelCarriesAWireSafePublic(t *testing.T) {
	t.Parallel()
	sentinels := []*errs.Error{
		corequeue.QueueMisconfigured, corequeue.MessageTooLarge, corequeue.UnknownReceipt,
		corequeue.LeaseExpired, corequeue.InvalidBatchSize,
	}
	for _, sentinel := range sentinels {
		public := errs.PublicOf(sentinel)
		if public == "" {
			t.Fatalf("%v has an empty Public", sentinel)
		}
		if len([]rune(public)) > 120 {
			t.Fatalf("%v Public is %d runes, want <= 120", sentinel, len([]rune(public)))
		}
		for _, char := range public {
			if char == '\n' || char == '\r' {
				t.Fatalf("%v Public contains a line break", sentinel)
			}
		}
	}
}

// TestTheFiveCodesAreDistinctAndInTheOwnedRange pins the ADR 0005 allocation:
// five codes, all in 0.2.23.*, none repeated.
func TestTheFiveCodesAreDistinctAndInTheOwnedRange(t *testing.T) {
	t.Parallel()
	const prefix errs.Code = 0x00_02_17_00
	codes := []errs.Code{
		corequeue.CodeQueueMisconfigured, corequeue.CodeMessageTooLarge,
		corequeue.CodeUnknownReceipt, corequeue.CodeLeaseExpired, corequeue.CodeInvalidBatchSize,
	}
	seen := map[errs.Code]bool{}
	for _, code := range codes {
		if code&0xFF_FF_FF_00 != prefix {
			t.Fatalf("code %#x is outside the 0.2.23.* range this package owns", uint32(code))
		}
		if seen[code] {
			t.Fatalf("code %#x is declared twice", uint32(code))
		}
		seen[code] = true
	}
}
