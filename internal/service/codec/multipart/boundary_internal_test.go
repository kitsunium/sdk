package multipart

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestValidateBoundary pins the RFC 2046 shape check, including the two rules
// a naive charset test misses: the 70-character ceiling and the ban on a
// trailing space (which is otherwise a legal bchar).
func TestValidateBoundary(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		in    string
		valid bool
	}
	tests := []tc{
		{"simple", "abc123", true},
		{"full bchar set", "Aa0'()+_,-./:=?", true},
		{"space inside is legal", "sdk boundary", true},
		{"exactly 70 characters", strings.Repeat("x", 70), true},
		{"empty", "", false},
		{"71 characters", strings.Repeat("x", 71), false},
		{"trailing space", "trailing ", false},
		{"character outside the set", "bad\tchar", false},
		{"non-ascii", "boundé", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		err := validateBoundary(tc.in)
		if tc.valid && err != nil {
			t.Errorf("%s: unexpected refusal %v", tc.name, err)
		}
		if !tc.valid && !errs.HasReason(err, "BOUNDARY_INVALID") {
			t.Errorf("%s: expected BOUNDARY_INVALID, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestFirstDelimiterLine covers the scan: a preamble is walked past, a body
// with no delimiter reports not-found, and the sniff window bounds the scan so
// a hostile preamble cannot make it O(len(body)).
func TestFirstDelimiterLine(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		in    string
		want  string
		found bool
	}
	tests := []tc{
		{"first line is the delimiter", "--abc\r\nrest", "abc", true},
		{"lf-only line ending", "--abc\nrest", "abc", true},
		{"no trailing newline", "--abc", "abc", true},
		{"preamble is skipped", "ignore me\r\n--abc\r\n", "abc", true},
		{"no delimiter", "nothing here\r\nnor here\r\n", "", false},
		{"delimiter past the sniff window", strings.Repeat("p\n", boundarySniffWindow) + "--abc\r\n", "", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, found := firstDelimiterLine([]byte(tc.in))
		if found != tc.found {
			t.Fatalf("%s: found=%v want %v", tc.name, found, tc.found)
		}
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestBoundaryRecovery covers the three recovery branches Boundary walks: the
// closing delimiter confirms the candidate, a zero-part body needs the
// trailing "--" stripped first, and a truncated body still hands over a
// structurally valid candidate so mime/multipart can report the truncation.
func TestBoundaryRecovery(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want string
	}
	tests := []tc{
		{"confirmed by the closing delimiter", "--abc\r\nbody\r\n--abc--\r\n", "abc"},
		{"zero-part body strips the closing marker", "--abc--\r\n", "abc"},
		{"boundary ending in two hyphens", "--ab--\r\nbody\r\n--ab----\r\n", "ab--"},
		{"truncated body keeps the candidate", "--abc\r\nbody", "abc"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := Boundary([]byte(tc.in))
		if err != nil {
			t.Fatalf("%s: Boundary err=%v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestBoundaryRefusals covers the two ways recovery fails.
func TestBoundaryRefusals(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	tests := []tc{
		{"no delimiter line", "plain prose"},
		{"delimiter line is not a usable boundary", "--\r\nbody\r\n"},
		{"delimiter carries a control character", "--ab\tcd\r\n"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := Boundary([]byte(tc.in))
		if !errs.HasReason(err, "BOUNDARY_INVALID") {
			t.Errorf("%s: expected BOUNDARY_INVALID, got %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestContentTypeRefusal pins the propagation: an unrecoverable delimiter
// surfaces the same typed refusal Boundary produced, not a bare header string.
func TestContentTypeRefusal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	tests := []tc{{"no delimiter", "not a multipart body"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := ContentType([]byte(tc.in))
		if !errs.HasReason(err, "BOUNDARY_INVALID") {
			t.Errorf("%s: expected BOUNDARY_INVALID, got %v", tc.name, err)
		}
		if got != "" {
			t.Errorf("%s: header %q returned alongside an error", tc.name, got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
