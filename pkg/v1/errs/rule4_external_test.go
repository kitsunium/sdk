package errs_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// TestNewRefusesABadReasonWithinRule4 is #469 as a consumer meets it: New
// handed a malformed 204-character reason returns an error whose Public is a
// fixed literal of at most 120 runes, carrying none of the caller's data, and
// whose Private is not empty — it names the offending reason, clipped.
func TestNewRefusesABadReasonWithinRule4(t *testing.T) {
	t.Parallel()
	reason := "BAD-" + strings.Repeat("X", 200)
	err := errs.New(appCode, reason, "fine public", "fine private")
	if code, _ := errs.CodeOf(err); code != errs.Pack(0, 0, 0, 2) {
		t.Fatalf("CodeOf = %s, want the INVALID_REASON meta-code 0.0.0.2", code)
	}
	public, private := errs.PublicOf(err), errs.PrivateOf(err)
	if n := utf8.RuneCountInString(public); n > 120 {
		t.Errorf("Public is %d runes, want at most 120", n)
	}
	if strings.Contains(public, "BAD-") {
		t.Errorf("Public %q echoes the caller's reason", public)
	}
	if !strings.Contains(private, "BAD-XXXX") || strings.Contains(private, strings.Repeat("X", 100)) {
		t.Errorf("Private = %q, want the reason named and clipped", private)
	}
}
