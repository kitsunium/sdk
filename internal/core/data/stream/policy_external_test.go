package stream_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/data/stream"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestTheZeroPolicyWorks pins ADR 0231 §6: every zero of PolicyValue has one
// reading, so the zero policy validates and normalises to the defaults.
func TestTheZeroPolicyWorks(t *testing.T) {
	t.Parallel()
	var zero stream.PolicyValue
	if err := zero.Validate(); err != nil {
		t.Fatalf("Validate(zero) = %v, want nil", err)
	}
	got := zero.Normalized()
	want := stream.PolicyValue{
		Partitions:     1,
		MaxRecordBytes: stream.DefaultMaxRecordBytes,
		SegmentBytes:   stream.DefaultSegmentBytes,
	}
	if got != want {
		t.Fatalf("Normalized(zero) = %+v, want %+v", got, want)
	}
}

// TestNormalizedKeepsWhatTheCallerSet shows the clamp touches only zeros.
func TestNormalizedKeepsWhatTheCallerSet(t *testing.T) {
	t.Parallel()
	set := stream.PolicyValue{
		Partitions:     8,
		MaxRecordBytes: 512,
		RetainFor:      time.Hour,
		RetainBytes:    1 << 30,
		MaxBytes:       1 << 20,
		BlockTimeout:   time.Second,
		SegmentBytes:   1 << 16,
		CompactAfter:   time.Minute,
	}
	if err := set.Validate(); err != nil {
		t.Fatalf("Validate = %v, want nil", err)
	}
	if got := set.Normalized(); got != set {
		t.Fatalf("Normalized = %+v, want %+v unchanged", got, set)
	}
}

// TestValidateRefusesByField pins each refusal of ADR 0231 §6 to
// StreamMisconfigured naming the field.
func TestValidateRefusesByField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		policy stream.PolicyValue
		field  string
	}{
		{"negative partitions", stream.PolicyValue{Partitions: -1}, "Partitions"},
		{"negative record bound", stream.PolicyValue{MaxRecordBytes: -1}, "MaxRecordBytes"},
		{"negative age", stream.PolicyValue{RetainFor: -time.Second}, "RetainFor"},
		{"negative retained bytes", stream.PolicyValue{RetainBytes: -1}, "RetainBytes"},
		{"negative ceiling", stream.PolicyValue{MaxBytes: -1}, "MaxBytes"},
		{"negative wait", stream.PolicyValue{BlockTimeout: -time.Second}, "BlockTimeout"},
		{"negative segment", stream.PolicyValue{SegmentBytes: -1}, "SegmentBytes"},
		{"negative compaction age", stream.PolicyValue{CompactAfter: -time.Second}, "CompactAfter"},
		{"wait with no ceiling", stream.PolicyValue{BlockTimeout: time.Second}, "BlockTimeout"},
		{"ceiling below the default record", stream.PolicyValue{MaxBytes: 1024}, "MaxBytes"},
		{"ceiling below the record", stream.PolicyValue{MaxRecordBytes: 4096, MaxBytes: 4095}, "MaxBytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.policy.Validate()
			if !errs.HasCode(err, stream.CodeStreamMisconfigured) {
				t.Fatalf("Validate = %v, want STREAM_MISCONFIGURED", err)
			}
			if got := fieldOf(err); got != tc.field {
				t.Fatalf("field = %q, want %q", got, tc.field)
			}
		})
	}
}

// TestACeilingOfOneRecordIsAccepted shows the MaxBytes floor is the record
// bound itself, not one byte above it.
func TestACeilingOfOneRecordIsAccepted(t *testing.T) {
	t.Parallel()
	p := stream.PolicyValue{MaxRecordBytes: 4096, MaxBytes: 4096}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate = %v, want nil", err)
	}
}

// fieldOf returns the "field" field of a refusal, or "" when it has none.
func fieldOf(err error) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "field" {
			return f.StringValue()
		}
	}
	return ""
}
