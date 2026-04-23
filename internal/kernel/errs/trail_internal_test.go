package errs

import "testing"

func Test_appendTrail_EmptyFirstWrap(t *testing.T) {
	t.Parallel()
	out, trunc := appendTrail(nil, 0x00_01_01_01)
	if len(out) != 1 || out[0] != 0x00_01_01_01 {
		t.Fatalf("expected [0x00010101], got %v", out)
	}
	if trunc {
		t.Fatalf("expected trailTruncated=false, got true")
	}
}

func Test_appendTrail_GrowsUpToCap_NoTruncation(t *testing.T) {
	t.Parallel()
	cause := &Error{}
	for i := 1; i <= 15; i++ {
		out, trunc := appendTrail(cause, Code(i))
		if trunc {
			t.Fatalf("iter %d: truncated prematurely", i)
		}
		cause.trail = out
	}
	if len(cause.trail) != 15 {
		t.Fatalf("expected len 15 after 15 appends, got %d", len(cause.trail))
	}
}

func Test_appendTrail_ExactlyAtCap_NoTruncation(t *testing.T) {
	t.Parallel()
	cause := &Error{}
	for i := 1; i <= 16; i++ {
		out, trunc := appendTrail(cause, Code(i))
		if trunc {
			t.Fatalf("iter %d: should not truncate yet (len==cap is fine)", i)
		}
		cause.trail = out
	}
	if len(cause.trail) != 16 {
		t.Fatalf("expected len 16 after 16 appends, got %d", len(cause.trail))
	}
}

func Test_appendTrail_OverflowTriggersTruncation(t *testing.T) {
	t.Parallel()
	cause := &Error{}
	for i := 1; i <= 16; i++ {
		out, _ := appendTrail(cause, Code(i))
		cause.trail = out
	}
	//: 17th append triggers truncation.
	out, trunc := appendTrail(cause, Code(17))
	cause.trail = out
	cause.trailTruncated = trunc
	if !trunc {
		t.Fatalf("expected trailTruncated=true on 17th append")
	}
	if len(out) != maxTrailLen {
		t.Fatalf("expected len %d after overflow, got %d", maxTrailLen, len(out))
	}
	//: origin (first entry) must be preserved.
	if out[0] != Code(1) {
		t.Fatalf("origin not preserved: out[0]=%d, want 1", out[0])
	}
	//: the newest entry must be present at the end.
	if out[len(out)-1] != Code(17) {
		t.Fatalf("newest not at tail: out[-1]=%d, want 17", out[len(out)-1])
	}
}

func Test_appendTrail_TruncatedFlagMonotonic(t *testing.T) {
	t.Parallel()
	//: pre-truncated cause with a short trail.
	cause := &Error{trail: []Code{0x01, 0x02}, trailTruncated: true}
	out, trunc := appendTrail(cause, 0x03)
	if !trunc {
		t.Fatalf("monotonic flag should propagate (inherited true) even under cap")
	}
	if len(out) != 3 {
		t.Fatalf("expected [1,2,3], got %v", out)
	}
}

func Test_appendTrail_RejectsZeroCode(t *testing.T) {
	t.Parallel()
	cause := &Error{trail: []Code{0x01, 0x02}}
	out, trunc := appendTrail(cause, 0)
	//: zero is silently ignored — trail unchanged.
	if len(out) != 2 || out[0] != 0x01 || out[1] != 0x02 {
		t.Fatalf("expected trail preserved, got %v", out)
	}
	//: flag stays whatever it was on the cause.
	if trunc {
		t.Fatalf("expected inherited false, got true")
	}
	//: empty cause + zero next → nil trail.
	out2, _ := appendTrail(nil, 0)
	if out2 != nil {
		t.Fatalf("expected nil trail, got %v", out2)
	}
}
