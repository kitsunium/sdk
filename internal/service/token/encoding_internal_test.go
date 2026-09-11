package token

import (
	"bytes"
	"strings"
	"testing"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestPreAuthEncodeMatchesTheSpecVectors checks PAE against the three worked
// examples in the PASETO specification. PAE is the piece nothing else can
// cross-check: an off-by-one in the length prefix produces signatures that
// verify perfectly against themselves and against nobody else's, so the only
// honest test is one whose expected bytes come from the spec rather than from
// this implementation.
func TestPreAuthEncodeMatchesTheSpecVectors(t *testing.T) {
	t.Parallel()
	for name, vector := range map[string]struct {
		pieces [][]byte
		want   []byte
	}{
		"empty list": {
			nil,
			[]byte("\x00\x00\x00\x00\x00\x00\x00\x00"),
		},
		"one empty piece": {
			[][]byte{{}},
			[]byte("\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"),
		},
		"one four-byte piece": {
			[][]byte{[]byte("test")},
			[]byte("\x01\x00\x00\x00\x00\x00\x00\x00\x04\x00\x00\x00\x00\x00\x00\x00test"),
		},
	} {
		if got := preAuthEncode(vector.pieces...); !bytes.Equal(got, vector.want) {
			t.Errorf("%s: PAE = %x, want %x", name, got, vector.want)
		}
	}
}

// TestPreAuthEncodeIsInjective is the property PAE exists for: no two
// different piece lists encode to the same bytes, so a byte cannot move from
// the payload into the footer without changing the signature.
func TestPreAuthEncodeIsInjective(t *testing.T) {
	t.Parallel()
	moved := preAuthEncode([]byte("ab"), []byte("c"))
	other := preAuthEncode([]byte("a"), []byte("bc"))
	if bytes.Equal(moved, other) {
		t.Fatal("PAE collided on a piece-boundary shift — the encoding is not injective")
	}
}

// TestSplitCompactRefusesBySizeFirst pins the CVE-2025-30204 ordering: the
// length bound is checked before the scan, so a separator flood is refused on
// its size rather than after its separators have been counted.
func TestSplitCompactRefusesBySizeFirst(t *testing.T) {
	t.Parallel()
	flood := strings.Repeat(".", 1<<16)
	if _, err := splitCompact(flood, 128); !errs.HasCode(err, coretoken.CodeTooLarge) {
		t.Fatalf("separator flood: got %v, want TOO_LARGE", err)
	}
	//: inside the size bound, the extra separator is what refuses it.
	if _, err := splitCompact(strings.Repeat(".", 32), 128); !errs.HasCode(err, coretoken.CodeMalformed) {
		t.Fatalf("too many segments: got %v, want MALFORMED", err)
	}
}

// TestSplitCompactViewsTheInput pins that the parts are sub-slices of the
// original string rather than copies — which is what makes the split allocate
// nothing at all.
func TestSplitCompactViewsTheInput(t *testing.T) {
	t.Parallel()
	seg, err := splitCompact("aa.bb.cc", 64)
	if err != nil {
		t.Fatalf("splitCompact: %v", err)
	}
	if seg.count != 3 || seg.part[0] != "aa" || seg.part[1] != "bb" || seg.part[2] != "cc" {
		t.Fatalf("split = %v (%d parts), want three views", seg.part[:seg.count], seg.count)
	}
	one, err := splitCompact("nodots", 64)
	if err != nil || one.count != 1 || one.part[0] != "nodots" {
		t.Fatalf("a separator-free input must yield one segment, got %v / %v", one, err)
	}
}

// TestCheckJSONDepthIsStringAware pins that a bracket inside a claim VALUE is
// data, not nesting — otherwise a legitimate token carrying a JSON string full
// of braces would be refused as an attack.
func TestCheckJSONDepthIsStringAware(t *testing.T) {
	t.Parallel()
	if err := checkJSONDepth([]byte(`{"note":"[[[[[[[[[["}`), 2); err != nil {
		t.Fatalf("brackets inside a string must not count as depth: %v", err)
	}
	if err := checkJSONDepth([]byte(`{"note":"\""}`), 2); err != nil {
		t.Fatalf("an escaped quote must not end the string: %v", err)
	}
	deep := strings.Repeat("[", 20) + strings.Repeat("]", 20)
	if err := checkJSONDepth([]byte(deep), 8); !errs.HasCode(err, coretoken.CodeTooDeep) {
		t.Fatalf("20-deep payload at a bound of 8: got %v, want TOO_DEEP", err)
	}
	//: exactly at the bound is inside it.
	atBound := strings.Repeat("[", 8) + strings.Repeat("]", 8)
	if err := checkJSONDepth([]byte(atBound), 8); err != nil {
		t.Fatalf("a payload exactly at the bound must pass: %v", err)
	}
}

// TestCheckNoDuplicateMembers pins the RFC 8725 §2.6 refusal and its scope:
// only the TOP level is checked, because that is where the JOSE and JWT member
// names live. A duplicate inside an application claim is that application's
// question.
func TestCheckNoDuplicateMembers(t *testing.T) {
	t.Parallel()
	if err := checkNoDuplicateMembers([]byte(`{"a":1,"b":2}`)); err != nil {
		t.Fatalf("distinct members must pass: %v", err)
	}
	if err := checkNoDuplicateMembers([]byte(`{"a":1,"a":2}`)); !errs.HasCode(err, CodeDuplicateMember) {
		t.Fatalf("duplicate members: got %v, want DUPLICATE_MEMBER", err)
	}
	if err := checkNoDuplicateMembers([]byte(`{"a":{"b":1,"b":2}}`)); err != nil {
		t.Fatalf("a nested duplicate is out of scope: %v", err)
	}
	for _, notAnObject := range []string{`[1,2]`, `"text"`, `42`, ``} {
		if err := checkNoDuplicateMembers([]byte(notAnObject)); !errs.HasCode(err, coretoken.CodeMalformed) {
			t.Errorf("%q: got %v, want MALFORMED", notAnObject, err)
		}
	}
}

// TestDecodeSegmentBoundsBeforeAllocating pins that the size check precedes
// the buffer allocation, so a segment claiming to decode to megabytes never
// gets a megabyte.
func TestDecodeSegmentBoundsBeforeAllocating(t *testing.T) {
	t.Parallel()
	big := b64Encode(bytes.Repeat([]byte("x"), 4096))
	if _, err := decodeSegment(big, 64); !errs.HasCode(err, coretoken.CodeTooLarge) {
		t.Fatalf("oversized segment: got %v, want TOO_LARGE", err)
	}
	if _, err := decodeSegment("!!!not-base64!!!", 64); !errs.HasCode(err, coretoken.CodeMalformed) {
		t.Fatalf("non-base64 segment: got %v, want MALFORMED", err)
	}
}

// b64Encode renders raw the way every segment in this package is spelled.
func b64Encode(raw []byte) string {
	return b64.EncodeToString(raw)
}
