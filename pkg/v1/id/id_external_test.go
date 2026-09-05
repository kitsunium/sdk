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
