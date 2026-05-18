package errs

import "testing"

func Test_appendTrail_EmptyFirstWrap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		next      Code
		wantLen   int
		wantFirst Code
		wantTrunc bool
	}
	tests := []tc{
		{"empty cause + single non-zero next", 0x00_01_01_01, 1, 0x00_01_01_01, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		out, trunc := appendTrail(nil, c.next)
		//: trail length must match the expected single-entry result.
		if len(out) != c.wantLen || out[0] != c.wantFirst {
			t.Fatalf("expected [%#x], got %v", c.wantFirst, out)
		}
		//: zero existing trail + first wrap should never set the truncation flag.
		if trunc != c.wantTrunc {
			t.Fatalf("expected trailTruncated=%v, got %v", c.wantTrunc, trunc)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_appendTrail_GrowsUpToCap_NoTruncation(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		appends int
		wantLen int
	}
	tests := []tc{
		{"15 successive appends stay under the cap", 15, 15},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cause := &Error{}
		//: drive successive appends and assert no truncation occurs below cap.
		for i := 1; i <= c.appends; i++ {
			out, trunc := appendTrail(cause, Code(i))
			if trunc {
				t.Fatalf("iter %d: truncated prematurely", i)
			}
			cause.trail = out
		}
		//: final length must equal the requested number of appends.
		if len(cause.trail) != c.wantLen {
			t.Fatalf("expected len %d after %d appends, got %d", c.wantLen, c.appends, len(cause.trail))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_appendTrail_ExactlyAtCap_NoTruncation(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		appends int
		wantLen int
	}
	tests := []tc{
		{"exactly maxTrailLen appends do not trigger truncation", maxTrailLen, maxTrailLen},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cause := &Error{}
		//: drive appends up to the cap; truncation is only legal on overflow.
		for i := 1; i <= c.appends; i++ {
			out, trunc := appendTrail(cause, Code(i))
			if trunc {
				t.Fatalf("iter %d: should not truncate yet (len==cap is fine)", i)
			}
			cause.trail = out
		}
		//: trail must occupy exactly the cap after the requested appends.
		if len(cause.trail) != c.wantLen {
			t.Fatalf("expected len %d after %d appends, got %d", c.wantLen, c.appends, len(cause.trail))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_appendTrail_OverflowTriggersTruncation(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		preAppends  int
		overflowNew Code
		wantOrigin  Code
		wantNewest  Code
		wantLen     int
	}
	tests := []tc{
		{"17th append triggers the overflow path", maxTrailLen, Code(17), Code(1), Code(17), maxTrailLen},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cause := &Error{}
		//: pre-fill the cause's trail up to the cap.
		for i := 1; i <= c.preAppends; i++ {
			out, _ := appendTrail(cause, Code(i))
			cause.trail = out
		}
		//: the next append must trigger truncation.
		out, trunc := appendTrail(cause, c.overflowNew)
		cause.trail = out
		cause.trailTruncated = trunc
		if !trunc {
			t.Fatalf("expected trailTruncated=true on overflow append")
		}
		//: truncated trail must still occupy exactly the cap.
		if len(out) != c.wantLen {
			t.Fatalf("expected len %d after overflow, got %d", c.wantLen, len(out))
		}
		//: origin (first entry) must be preserved across truncation.
		if out[0] != c.wantOrigin {
			t.Fatalf("origin not preserved: out[0]=%d, want %d", out[0], c.wantOrigin)
		}
		//: newest entry must be present at the tail.
		if out[len(out)-1] != c.wantNewest {
			t.Fatalf("newest not at tail: out[-1]=%d, want %d", out[len(out)-1], c.wantNewest)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_appendTrail_TruncatedFlagMonotonic(t *testing.T) {
	t.Parallel()
	type tc struct {
		name           string
		existing       []Code
		inheritedTrunc bool
		next           Code
		wantTrunc      bool
		wantTrail      []Code
	}
	tests := []tc{
		{
			name:           "inherited true propagates even when under cap",
			existing:       []Code{0x01, 0x02},
			inheritedTrunc: true,
			next:           0x03,
			wantTrunc:      true,
			wantTrail:      []Code{0x01, 0x02, 0x03},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cause := &Error{trail: c.existing, trailTruncated: c.inheritedTrunc}
		out, trunc := appendTrail(cause, c.next)
		//: monotonic flag — inherited truncation must not silently reset.
		if trunc != c.wantTrunc {
			t.Fatalf("monotonic flag should propagate (inherited %v), got %v", c.inheritedTrunc, trunc)
		}
		//: the trail content itself must match the expected entries.
		if len(out) != len(c.wantTrail) {
			t.Fatalf("expected %v, got %v", c.wantTrail, out)
		}
		for i, want := range c.wantTrail {
			if out[i] != want {
				t.Fatalf("idx %d: got %v, want %v", i, out[i], want)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_appendTrail_RejectsZeroCode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		existing  []Code
		next      Code
		wantTrail []Code
		wantTrunc bool
		wantNil   bool
	}
	tests := []tc{
		{
			name:      "non-empty cause + zero next preserves trail",
			existing:  []Code{0x01, 0x02},
			next:      0,
			wantTrail: []Code{0x01, 0x02},
			wantTrunc: false,
		},
		{
			name:    "empty cause + zero next produces nil trail",
			next:    0,
			wantNil: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var cause *Error
		//: only build a cause when the case carries an existing trail.
		if c.existing != nil {
			cause = &Error{trail: c.existing}
		}
		out, trunc := appendTrail(cause, c.next)
		//: nil-trail expectation short-circuits the equality check below.
		if c.wantNil {
			if out != nil {
				t.Fatalf("expected nil trail, got %v", out)
			}
			return
		}
		//: zero next must leave the trail bit-identical to the existing one.
		if len(out) != len(c.wantTrail) {
			t.Fatalf("expected trail preserved %v, got %v", c.wantTrail, out)
		}
		for i, want := range c.wantTrail {
			if out[i] != want {
				t.Fatalf("idx %d: got %v, want %v", i, out[i], want)
			}
		}
		//: the truncation flag must stay whatever was inherited.
		if trunc != c.wantTrunc {
			t.Fatalf("expected inherited %v, got %v", c.wantTrunc, trunc)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
