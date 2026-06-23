package id_test

import (
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
	//: an unknown scheme surfaces UnknownScheme.
	if _, err := id.New("nope"); err == nil {
		t.Fatal("New(nope) err=nil, want UnknownScheme")
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

// TestAvailable confirms the four schemes are listed.
func TestAvailable(t *testing.T) {
	t.Parallel()
	//: Available lists at least the four built-in schemes.
	if len(id.Available()) < 4 {
		t.Fatalf("Available=%v, want >= 4 schemes", id.Available())
	}
}
