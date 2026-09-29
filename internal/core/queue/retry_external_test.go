package queue_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// TestAGrowingRetryDelayNeedsARealCeiling pins MaxRetryDelay's refusals and
// its one accepted zero (ADR 0151). Zero is the policy every caller had before
// the field existed — a constant delay — and each refusal is a ceiling that is
// not one: negative, unrecordable, above nothing to grow from, or below the
// first step, which would silently shorten every wait the caller asked for.
func TestAGrowingRetryDelayNeedsARealCeiling(t *testing.T) {
	t.Parallel()
	base := corequeue.PolicyValue{VisibilityTimeout: usableTimeout, MaxDeliveries: 5, RetryDelay: time.Second}
	with := func(retry, ceiling time.Duration) corequeue.PolicyValue {
		policy := base
		policy.RetryDelay, policy.MaxRetryDelay = retry, ceiling
		return policy
	}
	cases := []struct {
		name    string
		problem string // empty: accepted
		policy  corequeue.PolicyValue
	}{
		{"no ceiling keeps the delay constant", "", with(time.Second, 0)},
		{"no ceiling and no delay", "", with(0, 0)},
		{"a ceiling above the delay", "", with(time.Second, time.Minute)},
		{"a ceiling equal to the delay", "", with(time.Second, time.Second)},
		{"a ceiling at MaxDeadlineOffset", "", with(time.Second, corequeue.MaxDeadlineOffset)},
		{"a negative ceiling", "negative", with(time.Second, -time.Second)},
		{"a ceiling past MaxDeadlineOffset", "past MaxDeadlineOffset", with(time.Second, corequeue.MaxDeadlineOffset+1)},
		{"a ceiling over a zero delay", "no RetryDelay to grow from", with(0, time.Minute)},
		{"a ceiling over a negative delay", "no RetryDelay to grow from", with(-time.Second, time.Minute)},
		{"a ceiling below the delay", "below RetryDelay", with(time.Minute, time.Second)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := testCase.policy.Validate()
			if testCase.problem == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
				t.Fatalf("Validate() = %v, want CodeQueueMisconfigured", err)
			}
			if got := fieldNamed(err); got != "MaxRetryDelay" {
				t.Errorf("Validate() names field %q, want MaxRetryDelay", got)
			}
			if got := fieldValue(err, "problem"); got != testCase.problem {
				t.Errorf("problem = %q, want %q", got, testCase.problem)
			}
		})
	}
}

// fieldValue returns the value of the field named key, or "".
func fieldValue(err error, key string) string {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}

// TestDoNotRetryMarksTheFailureAndKeepsTheCauseItsOrigin pins the handler
// half of immediate dead-lettering (ADR 0151): the mark is what the consumer
// engine looks for, and it must never displace what a dead letter records.
// An SDK cause keeps its own reason, code and public words — origin wins —
// while a cause that has none of its own, or no cause at all, is recorded as
// NOT_RETRYABLE, whose public words say what happened.
func TestDoNotRetryMarksTheFailureAndKeepsTheCauseItsOrigin(t *testing.T) {
	t.Parallel()
	sdkCause := errs.Define(0x00_02_17_FE, "ORDER_UNDECODABLE", "the order does not decode", "private half")
	cases := []struct {
		cause      error
		name       string
		wantReason string
		wantPublic string
		wantCode   errs.Code
	}{
		{sdkCause, "an SDK cause", "ORDER_UNDECODABLE", "the order does not decode", 0x00_02_17_FE},
		{errors.New("unexpected end of JSON input"), "a stdlib cause", "NOT_RETRYABLE", //nolint:err113 // a foreign error is the case
			corequeue.NotRetryable.Public(), corequeue.CodeNotRetryable},
		{nil, "no cause", "NOT_RETRYABLE", corequeue.NotRetryable.Public(), corequeue.CodeNotRetryable},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			marked := corequeue.DoNotRetry(testCase.cause)
			if !errs.HasCode(marked, corequeue.CodeNotRetryable) {
				t.Fatalf("DoNotRetry(%v) does not carry CodeNotRetryable: %v", testCase.cause, marked)
			}
			if reason, _ := errs.ReasonOf(marked); reason != testCase.wantReason {
				t.Errorf("reason = %q, want %q", reason, testCase.wantReason)
			}
			if public := errs.PublicOf(marked); public != testCase.wantPublic {
				t.Errorf("public = %q, want %q", public, testCase.wantPublic)
			}
			if code, _ := errs.CodeOf(marked); code != testCase.wantCode {
				t.Errorf("code = %v, want %v", code, testCase.wantCode)
			}
			if testCase.cause != nil && !errors.Is(marked, testCase.cause) {
				t.Errorf("errors.Is(DoNotRetry(cause), cause) = false; the cause must stay in the chain")
			}
		})
	}
	//: a failure nobody marked is not one — the engine would retry it.
	if errs.HasCode(sdkCause, corequeue.CodeNotRetryable) {
		t.Fatal("an unmarked cause carries CodeNotRetryable")
	}
	//: and the bare sentinel is its own mark.
	if !errs.HasCode(corequeue.NotRetryable, corequeue.CodeNotRetryable) {
		t.Fatal("NotRetryable does not carry its own code")
	}
}

// TestTheQueueSiblingsKeepTheirMethodCounts pins the shapes ADR 0039 freezes
// once published: Broker at four methods, and each capability sibling at the
// count it shipped with. A method added to any of them breaks every downstream
// implementation at compile time; a new capability is a new sibling.
func TestTheQueueSiblingsKeepTheirMethodCounts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		typ  reflect.Type
		want int
	}{
		{reflect.TypeFor[corequeue.Broker](), 4},
		{reflect.TypeFor[corequeue.DeadLetterReader](), 1},
		{reflect.TypeFor[corequeue.LeaseExtender](), 1},
		{reflect.TypeFor[corequeue.Waker](), 1},
		{reflect.TypeFor[corequeue.Rejecter](), 1},
		{reflect.TypeFor[corequeue.DeadLetterManager](), 2},
	}
	for _, testCase := range cases {
		if got := testCase.typ.NumMethod(); got != testCase.want {
			t.Errorf("%v has %d methods, want %d — a published port grows by siblings (ADR 0039)",
				testCase.typ, got, testCase.want)
		}
	}
}
