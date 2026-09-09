package id_test

import (
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/id"
)

// TestHelpers exercises every named helper + New dispatch.
func TestHelpers(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		gen  func() (string, error)
	}
	tests := []tc{
		{"UUIDv4", id.UUIDv4},
		{"UUIDv7", id.UUIDv7},
		{"ULID", id.ULID},
		{"Snowflake", id.Snowflake},
		{"NanoID", id.NanoID},
		{"KSUID", id.KSUID},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := c.gen()
		//: every helper returns a non-empty id with no error.
		if err != nil || got == "" {
			t.Fatalf("%s: got=%q err=%v", c.name, got, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); runCase(t, c) })
	}
}

// TestNewDispatch checks New routes by scheme and rejects unknowns.
func TestNewDispatch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		scheme id.Scheme
		wantOK bool
	}
	tests := []tc{
		{"uuidv4", id.UUIDv4Scheme, true},
		{"uuidv7", id.UUIDv7Scheme, true},
		{"ulid", id.ULIDScheme, true},
		{"snowflake", id.SnowflakeScheme, true},
		{"nanoid", id.NanoIDScheme, true},
		{"ksuid", id.KSUIDScheme, true},
		//: typeid is deliberately absent from the registry — a TypeID needs a
		//: prefix, and no prefix would be a defensible default. This case is
		//: the assertion that the absence is intended, not an oversight.
		{"typeid, which has no default prefix", id.TypeIDScheme, false},
		{"an unregistered scheme", "nope", false},
		{"an empty scheme", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := id.New(c.scheme)
		if !c.wantOK {
			//: assert the exact sentinel, not merely "some error", so a
			//: different failure cannot pass silently.
			if !errors.Is(err, id.UnknownScheme) {
				t.Fatalf("New(%q) err = %v, want UnknownScheme", c.scheme, err)
			}
			if got != "" {
				t.Errorf("New(%q) returned %q alongside the error", c.scheme, got)
			}
			return
		}
		if err != nil || got == "" {
			t.Fatalf("New(%q) got = %q err = %v", c.scheme, got, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestNewSnowflakeExplicitNode checks the explicit-node constructor works.
func TestNewSnowflakeExplicitNode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		node int64
	}
	tests := []tc{
		{"node zero", 0},
		{"a mid-range node", 42},
		{"the highest 10-bit node", 1023},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		gen := id.NewSnowflake(c.node)
		first, err := gen.New()
		if err != nil || first == "" {
			t.Fatalf("NewSnowflake(%d).New() got = %q err = %v", c.node, first, err)
		}
		//: two ids from one generator must differ, or the node bits would be
		//: the only thing distinguishing concurrent producers.
		second, err := gen.New()
		if err != nil {
			t.Fatalf("second New(): %v", err)
		}
		if first == second {
			t.Errorf("two ids from node %d are identical: %q", c.node, first)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAvailable confirms the six registered built-in schemes are listed.
func TestAvailable(t *testing.T) {
	t.Parallel()
	got := id.Available()
	//: index the registry listing so membership is O(1) per assertion.
	present := make(map[id.Scheme]bool, len(got))
	for _, s := range got {
		present[s] = true
	}
	//: assert membership, not just a count — a count alone passes even when a
	//: built-in scheme is missing and an unrelated one took its place.
	for _, want := range []id.Scheme{
		id.UUIDv4Scheme, id.UUIDv7Scheme, id.ULIDScheme,
		id.SnowflakeScheme, id.NanoIDScheme, id.KSUIDScheme,
	} {
		if !present[want] {
			t.Errorf("Available()=%v is missing built-in scheme %q", got, want)
		}
	}
	//: and typeid must NOT be there — see TestNewDispatch.
	if present[id.TypeIDScheme] {
		t.Errorf("Available()=%v lists typeid, which has no non-arbitrary default prefix", got)
	}
}

// TestConfiguredConstructors checks the two schemes that take configuration and
// the refusals that go with them. Both REFUSE rather than substitute a default,
// because the knob they take is the caller's whole intent (ADR 0031): a
// zero-length NanoID identifies nothing, and a TypeID without a type is a
// UUIDv7 with extra characters.
func TestConfiguredConstructors(t *testing.T) {
	t.Parallel()
	//: NanoID with an explicit length.
	short, err := id.NewNanoID(10)
	if err != nil {
		t.Fatalf("NewNanoID(10) = %v, want nil", err)
	}
	got, genErr := short.New()
	if genErr != nil || len(got) != 10 {
		t.Errorf("NewNanoID(10).New() = %q, %v; want 10 characters and nil", got, genErr)
	}
	//: and the refusal, matched on the exported sentinel.
	if _, err = id.NewNanoID(0); !errors.Is(err, id.InvalidSize) {
		t.Errorf("NewNanoID(0) = %v, want InvalidSize", err)
	}

	//: TypeID with a prefix.
	users, err := id.NewTypeID("user")
	if err != nil {
		t.Fatalf("NewTypeID(\"user\") = %v, want nil", err)
	}
	minted, mintErr := users.New()
	if mintErr != nil {
		t.Fatalf("New = %v, want nil", mintErr)
	}
	if len(minted) != len("user_")+26 {
		t.Errorf("New() = %q (%d chars), want %d", minted, len(minted), len("user_")+26)
	}
	//: and the refusal for a prefix that is not a usable type name.
	for _, bad := range []string{"", "User", "user_", "_user", "user1"} {
		if _, err = id.NewTypeID(bad); !errors.Is(err, id.InvalidPrefix) {
			t.Errorf("NewTypeID(%q) = %v, want InvalidPrefix", bad, err)
		}
	}
}

// TestDecodableSchemes pins the two schemes that read back. Being able to
// recover WHEN a KSUID was issued and WHAT a TypeID names is the reason a
// consumer reaches for either over a random identifier, so the round trip is
// the contract, not an implementation detail.
func TestDecodableSchemes(t *testing.T) {
	t.Parallel()
	//: KSUID: mint, then recover the issue time from the string alone.
	before := time.Now().Add(-2 * time.Second)
	ksuid, err := id.KSUID()
	if err != nil {
		t.Fatalf("KSUID() = %v, want nil", err)
	}
	issued, payload, parseErr := id.ParseKSUID(ksuid)
	if parseErr != nil {
		t.Fatalf("ParseKSUID(%q) = %v, want nil", ksuid, parseErr)
	}
	if issued.Before(before) || issued.After(time.Now().Add(2*time.Second)) {
		t.Errorf("ParseKSUID(%q) issued at %s, outside the minting window", ksuid, issued)
	}
	if payload == ([16]byte{}) {
		t.Errorf("ParseKSUID(%q) returned an all-zero payload", ksuid)
	}
	if _, _, err = id.ParseKSUID("not-a-ksuid"); !errors.Is(err, id.Malformed) {
		t.Errorf("ParseKSUID(\"not-a-ksuid\") = %v, want Malformed", err)
	}

	//: TypeID: format and parse must be exact inverses in both directions.
	const (
		wantPrefix string = "user"
		wantUUID   string = "0190b3c0-1234-7abc-8def-000000000001"
	)
	formatted, fmtErr := id.FormatTypeID(wantPrefix, wantUUID)
	if fmtErr != nil {
		t.Fatalf("FormatTypeID = %v, want nil", fmtErr)
	}
	gotPrefix, gotUUID, tidErr := id.ParseTypeID(formatted)
	if tidErr != nil {
		t.Fatalf("ParseTypeID(%q) = %v, want nil", formatted, tidErr)
	}
	if gotPrefix != wantPrefix || gotUUID != wantUUID {
		t.Errorf("round trip gave (%q, %q), want (%q, %q)", gotPrefix, gotUUID, wantPrefix, wantUUID)
	}
	//: and a freshly minted TypeID must read back as the type it was built for.
	users, err := id.NewTypeID("account")
	if err != nil {
		t.Fatalf("NewTypeID = %v, want nil", err)
	}
	minted, mintErr := users.New()
	if mintErr != nil {
		t.Fatalf("New = %v, want nil", mintErr)
	}
	if gotPrefix, _, err = id.ParseTypeID(minted); err != nil || gotPrefix != "account" {
		t.Errorf("ParseTypeID(%q) = %q, %v; want \"account\" and nil", minted, gotPrefix, err)
	}
	if _, _, err = id.ParseTypeID("user_not-a-typeid"); !errors.Is(err, id.Malformed) {
		t.Errorf("ParseTypeID with a bad suffix = %v, want Malformed", err)
	}
}
