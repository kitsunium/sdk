// Package id_test — the TypeID generator as a consumer reaches it.
package id_test

import (
	"strings"
	"testing"

	coreid "github.com/kitsunium/sdk/internal/core/id"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcid "github.com/kitsunium/sdk/internal/service/id"
)

// TestNewTypeID pins the constructor, its refusal, and the shape of what it
// mints. The prefix is the whole value of the scheme, so a generator built from
// an invalid one must fail once — at construction — rather than mint identifiers
// nothing else can parse, one per call (ADR 0031).
func TestNewTypeID(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix string
		//: whether the constructor must hand back a working generator.
		ok bool
	}
	tests := []tc{
		{"a plain type name", "user", true},
		{"a compound type name", "user_account", true},
		{"a single character", "u", true},
		{"the empty prefix is refused", "", false},
		{"an uppercase prefix is refused", "User", false},
		{"a digit is refused", "user2", false},
		{"a leading underscore is refused", "_user", false},
		{"a trailing underscore is refused", "user_", false},
		{"a hyphen is refused", "user-account", false},
		{"an over-long prefix is refused", strings.Repeat("a", 64), false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		gen, err := svcid.NewTypeID(c.prefix)
		if !c.ok {
			if err == nil {
				t.Fatalf("NewTypeID(%q) = nil error, want a refusal", c.prefix)
			}
			if gen != nil {
				t.Errorf("NewTypeID(%q) handed back a generator alongside its refusal", c.prefix)
			}
			if !errs.HasReason(err, "ID_INVALID_PREFIX") {
				t.Errorf("NewTypeID(%q) = %v, want ID_INVALID_PREFIX", c.prefix, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("NewTypeID(%q) = %v, want nil", c.prefix, err)
		}
		got, genErr := gen.New()
		if genErr != nil {
			t.Fatalf("New = %v, want nil", genErr)
		}
		//: prefix, underscore, then exactly 26 base32 characters.
		if len(got) != len(c.prefix)+1+26 {
			t.Fatalf("New() = %q (%d chars), want %d", got, len(got), len(c.prefix)+1+26)
		}
		//: and it must read back as the very type it was built for — that is
		//: the entire premise of carrying the type in the identifier.
		prefix, uuid, parseErr := svcid.ParseTypeID(got)
		if parseErr != nil {
			t.Fatalf("ParseTypeID(%q) = %v, want nil", got, parseErr)
		}
		if prefix != c.prefix {
			t.Errorf("ParseTypeID(%q) prefix = %q, want %q", got, prefix, c.prefix)
		}
		//: the payload is a UUIDv7, so the version nibble is fixed.
		if len(uuid) != 36 || uuid[14] != '7' {
			t.Errorf("ParseTypeID(%q) uuid = %q, want a canonical v7", got, uuid)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTypeID_notRegistered pins a deliberate absence. Every other scheme in this
// package resolves through core/id.New; typeid does not, because there is no
// non-arbitrary prefix the SDK could pick on a caller's behalf. Registering one
// would mean either inventing their domain vocabulary or minting "_01h2…", and
// both are the silent degradation ADR 0031 forbids — so UNKNOWN_SCHEME here is
// the correct answer, not a gap.
func TestTypeID_notRegistered(t *testing.T) {
	t.Parallel()
	if coreid.Scheme("typeid").Known() {
		t.Error("the typeid scheme registered itself; it has no non-arbitrary default prefix")
	}
	got, err := coreid.New("typeid")
	if err == nil {
		t.Fatalf("coreid.New(\"typeid\") = %q, nil; want UNKNOWN_SCHEME", got)
	}
	if !errs.HasReason(err, "UNKNOWN_SCHEME") {
		t.Errorf("coreid.New(\"typeid\") = %v, want UNKNOWN_SCHEME", err)
	}
}

// TestTypeID_roundtrip pins parse and format as exact inverses in BOTH
// directions, against fixtures from the TypeID specification rather than
// against this implementation's own output. A codec checked only against itself
// interoperates with nothing.
func TestTypeID_roundtrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		typeID string
		prefix string
		uuid   string
	}
	tests := []tc{
		{
			"the zero value",
			"prefix_00000000000000000000000000",
			"prefix",
			"00000000-0000-0000-0000-000000000000",
		},
		{
			"one",
			"prefix_00000000000000000000000001",
			"prefix",
			"00000000-0000-0000-0000-000000000001",
		},
		{
			//: 'a' is the tenth symbol — the alphabet skips no digit before it.
			"ten",
			"prefix_0000000000000000000000000a",
			"prefix",
			"00000000-0000-0000-0000-00000000000a",
		},
		{
			//: 'g' is the sixteenth: i, l, o and u are the ones excluded, and
			//: they sit later in the alphabet.
			"sixteen",
			"prefix_0000000000000000000000000g",
			"prefix",
			"00000000-0000-0000-0000-000000000010",
		},
		{
			"thirty-two, the first carry into a second character",
			"prefix_00000000000000000000000010",
			"prefix",
			"00000000-0000-0000-0000-000000000020",
		},
		{
			//: '7' is the largest legal first character: the two pad bits above
			//: it must be zero, so this is 2^128-1 exactly.
			"the largest representable value",
			"prefix_7zzzzzzzzzzzzzzzzzzzzzzzzz",
			"prefix",
			"ffffffff-ffff-ffff-ffff-ffffffffffff",
		},
		{
			//: an underscore inside the prefix is legal, and it is why the
			//: suffix must be split off by WIDTH rather than by separator.
			"a prefix containing an underscore",
			"pre_fix_00000000000000000000000000",
			"pre_fix",
			"00000000-0000-0000-0000-000000000000",
		},
		{
			"the shortest legal prefix",
			"u_00000000000000000000000000",
			"u",
			"00000000-0000-0000-0000-000000000000",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: parse direction — the rendering must yield the documented fields.
		prefix, uuid, err := svcid.ParseTypeID(c.typeID)
		if err != nil {
			t.Fatalf("ParseTypeID(%q) = %v, want nil", c.typeID, err)
		}
		if prefix != c.prefix {
			t.Errorf("ParseTypeID(%q) prefix = %q, want %q", c.typeID, prefix, c.prefix)
		}
		if uuid != c.uuid {
			t.Errorf("ParseTypeID(%q) uuid = %q, want %q", c.typeID, uuid, c.uuid)
		}
		//: format direction — the documented fields must yield the rendering.
		got, fmtErr := svcid.FormatTypeID(c.prefix, c.uuid)
		if fmtErr != nil {
			t.Fatalf("FormatTypeID(%q, %q) = %v, want nil", c.prefix, c.uuid, fmtErr)
		}
		if got != c.typeID {
			t.Errorf("FormatTypeID(%q, %q) = %q, want %q", c.prefix, c.uuid, got, c.typeID)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the example from the TypeID specification, round-tripped rather than
	//: decomposed: whatever it decodes to must re-encode to the same string.
	const spec string = "user_01h2xcejqtf2nbrexx3vqjhp41"
	prefix, uuid, err := svcid.ParseTypeID(spec)
	if err != nil {
		t.Fatalf("ParseTypeID(%q) = %v, want nil", spec, err)
	}
	if prefix != "user" {
		t.Errorf("ParseTypeID(%q) prefix = %q, want %q", spec, prefix, "user")
	}
	again, fmtErr := svcid.FormatTypeID(prefix, uuid)
	if fmtErr != nil {
		t.Fatalf("FormatTypeID = %v, want nil", fmtErr)
	}
	if again != spec {
		t.Errorf("round trip gave %q, want %q", again, spec)
	}
}

// TestParseTypeID_rejects pins the refusals a consumer will actually hit: an
// identifier of the wrong type pasted into the wrong column, a suffix carrying
// the Crockford-excluded letters a human typed by hand, and a bare UUID.
func TestParseTypeID_rejects(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     string
		reason string
	}
	tests := []tc{
		{"the empty string", "", "ID_MALFORMED"},
		{"a bare suffix with no prefix", "00000000000000000000000000", "ID_MALFORMED"},
		{"a prefix with no suffix", "user_", "ID_MALFORMED"},
		{"a truncated suffix", "user_0000000000000000000000000", "ID_MALFORMED"},
		{"an over-long suffix", "user_000000000000000000000000000", "ID_MALFORMED"},
		{"a dashed UUID", "user_0190b3c0-0000-7000-8000-0000", "ID_MALFORMED"},
		//: the pad bits above '7' cannot be set — see the round-trip fixtures.
		{"an over-wide suffix", "user_8zzzzzzzzzzzzzzzzzzzzzzzzz", "ID_MALFORMED"},
		//: i, l, o and u are excluded from Crockford on purpose.
		{"an excluded letter in the suffix", "user_0000000000000000000000000i", "ID_MALFORMED"},
		{"an uppercase suffix", "user_0000000000000000000000000Z", "ID_MALFORMED"},
		{"an empty prefix", "_00000000000000000000000000", "ID_INVALID_PREFIX"},
		{"an uppercase prefix", "User_00000000000000000000000000", "ID_INVALID_PREFIX"},
		{"a prefix ending in an underscore", "user__00000000000000000000000000", "ID_INVALID_PREFIX"},
		{"a prefix containing a digit", "user2_00000000000000000000000000", "ID_INVALID_PREFIX"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, _, err := svcid.ParseTypeID(c.in)
		if err == nil {
			t.Fatalf("ParseTypeID(%q) = nil error, want %s", c.in, c.reason)
		}
		if !errs.HasReason(err, c.reason) {
			t.Errorf("ParseTypeID(%q) = %v, want %s", c.in, err, c.reason)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFormatTypeID_rejects pins the migration entry point's refusals. It exists
// so an existing UUID column can be relabelled, which is exactly where a
// malformed value would otherwise be laundered into a typed identifier.
func TestFormatTypeID_rejects(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix string
		uuid   string
		reason string
	}
	tests := []tc{
		{"an empty prefix", "", "00000000-0000-0000-0000-000000000000", "ID_INVALID_PREFIX"},
		{"an uppercase prefix", "User", "00000000-0000-0000-0000-000000000000", "ID_INVALID_PREFIX"},
		{"an empty uuid", "user", "", "ID_MALFORMED"},
		{"an undashed uuid", "user", "00000000000000000000000000000000", "ID_MALFORMED"},
		{"a non-hex uuid", "user", "0000000g-0000-0000-0000-000000000000", "ID_MALFORMED"},
		{"a misgrouped uuid", "user", "000000-000000-0000-0000-000000000000", "ID_MALFORMED"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := svcid.FormatTypeID(c.prefix, c.uuid)
		if err == nil {
			t.Fatalf("FormatTypeID(%q, %q) = nil error, want %s", c.prefix, c.uuid, c.reason)
		}
		if !errs.HasReason(err, c.reason) {
			t.Errorf("FormatTypeID(%q, %q) = %v, want %s", c.prefix, c.uuid, err, c.reason)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: a v4 payload is accepted on purpose: refusing to label a UUID that
	//: already exists in a caller's database would defeat the one job this
	//: function has. New still only ever mints v7.
	got, err := svcid.FormatTypeID("user", "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d")
	if err != nil {
		t.Fatalf("FormatTypeID with a v4 payload = %v, want nil", err)
	}
	if !strings.HasPrefix(got, "user_") || len(got) != len("user_")+26 {
		t.Errorf("FormatTypeID with a v4 payload = %q, want a canonical rendering", got)
	}
}
