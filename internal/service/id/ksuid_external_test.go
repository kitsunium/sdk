// Package id_test — the KSUID generator as a consumer reaches it.
package id_test

import (
	"strings"
	"testing"
	"time"

	coreid "github.com/kitsunium/sdk/internal/core/id"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcid "github.com/kitsunium/sdk/internal/service/id"
)

// base62Alphabet is the KSUID symbol set, restated here rather than imported so
// the test fails if the production constant is reordered. The order is the
// contract: it is ascending ASCII, which is what makes a fixed-width rendering
// sort chronologically as a plain string.
const base62Alphabet string = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// TestKSUID pins that blank-importing the package registers the scheme and that
// what comes out is a usable KSUID. The registration is the part a consumer
// cannot verify any other way: nothing in their code names ksuidGen.
func TestKSUID(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mint func() (string, error)
	}
	tests := []tc{
		{"through the exported generator", svcid.KSUID.New},
		{"through the registry", func() (string, error) { return coreid.New("ksuid") }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := c.mint()
		if err != nil {
			t.Fatalf("New = %v, want nil", err)
		}
		//: 27 base62 characters is the canonical rendering.
		if len(got) != 27 {
			t.Fatalf("New() = %q (%d chars), want 27", got, len(got))
		}
		for _, r := range got {
			if !strings.ContainsRune(base62Alphabet, r) {
				t.Errorf("New() = %q contains %q, outside the base62 alphabet", got, r)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the scheme must resolve by name, which is what the blank import buys.
	if !coreid.Scheme("ksuid").Known() {
		t.Error("the ksuid scheme did not self-register on import")
	}
}

// TestParseKSUID pins that a freshly minted identifier reads back. Recovering
// the creation time from the identifier alone is the reason a consumer chooses
// a KSUID over a random one, so a generator whose output its own parser rejects
// would have lost the point of the format.
func TestParseKSUID(t *testing.T) {
	t.Parallel()
	//: bracket the mint so the decoded instant can be checked against a real
	//: window rather than against the same clock reading that produced it.
	before := time.Now().Add(-2 * time.Second)
	got, err := svcid.KSUID.New()
	if err != nil {
		t.Fatalf("New = %v, want nil", err)
	}
	after := time.Now().Add(2 * time.Second)

	issued, payload, parseErr := svcid.ParseKSUID(got)
	if parseErr != nil {
		t.Fatalf("ParseKSUID(%q) = %v, want nil", got, parseErr)
	}
	if issued.Before(before) || issued.After(after) {
		t.Errorf("ParseKSUID(%q) issued at %s, outside [%s, %s]", got, issued, before, after)
	}
	//: an all-zero payload would mean the entropy never reached the buffer.
	if payload == ([16]byte{}) {
		t.Errorf("ParseKSUID(%q) returned an all-zero payload", got)
	}
	//: the decoded instant must be UTC so two callers in different zones
	//: compare the same value.
	if issued.Location() != time.UTC {
		t.Errorf("ParseKSUID(%q) returned %s, want UTC", got, issued.Location())
	}
}

// TestParseKSUID_rejects pins the refusals a consumer will actually hit —
// truncated identifiers, ones that picked up surrounding whitespace, and
// renderings copied from another format.
func TestParseKSUID_rejects(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	tests := []tc{
		{"the empty string", ""},
		{"a truncated identifier", "0ujtsYcgvSTl8PAuAdqWYSMnLO"},
		{"trailing whitespace", "0ujtsYcgvSTl8PAuAdqWYSMnLOv "},
		{"leading whitespace", " ujtsYcgvSTl8PAuAdqWYSMnLOv"},
		{"a dashed UUID", "0190b3c0-0000-7000-8000-000000000000"},
		{"a hyphen in the payload", "0ujtsYcgvSTl8PAuAdqWYSMnL-v"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, _, err := svcid.ParseKSUID(c.in)
		if err == nil {
			t.Fatalf("ParseKSUID(%q) = nil error, want ID_MALFORMED", c.in)
		}
		if !errs.HasReason(err, "ID_MALFORMED") {
			t.Errorf("ParseKSUID(%q) = %v, want ID_MALFORMED", c.in, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
