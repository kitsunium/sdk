package id_test

import (
	"errors"
	"testing"

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
	//: a known scheme generates.
	if s, err := id.New(id.UUIDv7Scheme); err != nil || s == "" {
		t.Fatalf("New(uuidv7) got=%q err=%v", s, err)
	}
	//: an unknown scheme surfaces UnknownScheme — assert the exact sentinel,
	//: not merely "some error", so a different failure cannot pass silently.
	_, err := id.New("nope")
	if !errors.Is(err, id.UnknownScheme) {
		t.Fatalf("New(nope) err=%v, want UnknownScheme", err)
	}
}

// TestNewSnowflakeExplicitNode checks the explicit-node constructor works.
func TestNewSnowflakeExplicitNode(t *testing.T) {
	t.Parallel()
	gen := id.NewSnowflake(42)
	s, err := gen.New()
	//: an explicit-node snowflake generates a non-empty decimal id.
	if err != nil || s == "" {
		t.Fatalf("NewSnowflake(42).New() got=%q err=%v", s, err)
	}
}

// TestAvailable confirms the four built-in schemes are listed.
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
	for _, want := range []id.Scheme{id.UUIDv4Scheme, id.UUIDv7Scheme, id.ULIDScheme, id.SnowflakeScheme} {
		if !present[want] {
			t.Errorf("Available()=%v is missing built-in scheme %q", got, want)
		}
	}
}
