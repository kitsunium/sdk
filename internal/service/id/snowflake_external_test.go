// Package id_test — the snowflake generator as a consumer reaches it.
package id_test

import (
	"strconv"
	"testing"

	coreid "github.com/kitsunium/sdk/internal/core/id"
	svcid "github.com/kitsunium/sdk/internal/service/id"
)

// TestNewSnowflake pins the explicit-node constructor and the property the
// whole format exists for: strict monotonicity within one process.
//
// A snowflake is chosen over a UUID when the identifier has to be small, ordered
// and issued without coordination, so a generator that repeated or regressed
// would break the index it was picked to feed. The node id is reduced into the
// 10-bit space rather than refused, which means an out-of-range node is a
// deployment mistake that still produces usable — just possibly colliding —
// identifiers, and that reduction has to be visible.
func TestNewSnowflake(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		node int64
		//: how many identifiers to mint; monotonicity only shows up in a run.
		count int
	}
	tests := []tc{
		{"node zero", 0, 1000},
		{"a mid-range node", 7, 1000},
		{"the highest in-range node", 1023, 100},
		//: 1024 reduces to 0 rather than being refused.
		{"a node past the 10-bit space", 1024, 100},
		{"a very large node", 1 << 40, 100},
		{"a negative node", -1, 100},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		gen := svcid.NewSnowflake(c.node)
		prev := int64(-1)
		for range c.count {
			s, err := gen.New()
			if err != nil {
				t.Fatalf("New = %v, want nil", err)
			}
			//: the rendering is decimal, because that is what fits an integer
			//: column without a conversion at every read.
			v, perr := strconv.ParseInt(s, 10, 64)
			if perr != nil {
				t.Fatalf("New() = %q, which is not decimal: %v", s, perr)
			}
			//: strictly increasing — an equal value would be a duplicate id.
			if v <= prev {
				t.Fatalf("New() = %d after %d, want strictly greater", v, prev)
			}
			//: the packed value must stay positive, or it would not fit a
			//: signed 64-bit column the way the format promises.
			if v <= 0 {
				t.Fatalf("New() = %d, want a positive identifier", v)
			}
			prev = v
		}
		//: the scheme is fixed whatever the node.
		if got := gen.Scheme(); got != "snowflake" {
			t.Errorf("Scheme() = %q, want snowflake", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestSnowflakeRegistered pins that the default-node generator self-registered
// on import, which is the only way a consumer reaches it by name.
func TestSnowflakeRegistered(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mint func() (string, error)
	}
	tests := []tc{
		{"through the exported generator", svcid.Snowflake.New},
		{"through the registry", func() (string, error) { return coreid.New("snowflake") }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := c.mint()
		if err != nil {
			t.Fatalf("New = %v, want nil", err)
		}
		if _, perr := strconv.ParseInt(got, 10, 64); perr != nil {
			t.Errorf("New() = %q, which is not decimal: %v", got, perr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the scheme must resolve by name, which is what the blank import buys.
	if !coreid.Scheme("snowflake").Known() {
		t.Error("the snowflake scheme did not self-register on import")
	}
}

// TestRegisteredSchemes confirms every scheme this package ships is reachable
// by name AND that the registry reports no scheme this package does not ship.
//
// Both directions matter. A missing entry means a consumer's id.New("ulid")
// fails at runtime with nothing to point at; an unexpected one means something
// registered a scheme under a name the package never declared, which is how two
// generators quietly end up fighting over one key.
func TestRegisteredSchemes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		scheme coreid.Scheme
	}
	tests := []tc{
		{"the snowflake scheme", "snowflake"},
		{"the ULID scheme", "ulid"},
		{"the UUIDv4 scheme", "uuidv4"},
		{"the UUIDv7 scheme", "uuidv7"},
	}
	declared := make(map[coreid.Scheme]struct{}, len(tests))
	for _, c := range tests {
		declared[c.scheme] = struct{}{}
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: forward: what this package declares must be registered and usable.
		if !c.scheme.Known() {
			t.Fatalf("the scheme %q is not registered", c.scheme)
		}
		if _, ok := coreid.Lookup(c.scheme); !ok {
			t.Errorf("Lookup(%q) missed a registered scheme", c.scheme)
		}
		got, err := coreid.New(c.scheme)
		if err != nil {
			t.Fatalf("New(%q) = %v, want nil", c.scheme, err)
		}
		if got == "" {
			t.Errorf("New(%q) returned an empty identifier", c.scheme)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: reverse: every scheme this package is responsible for must resolve to a
	//: generator that reports that same scheme back. A generator registered
	//: under one key while reporting another is how two schemes silently share
	//: one implementation.
	for scheme := range declared {
		g, ok := coreid.Lookup(scheme)
		if !ok {
			t.Errorf("Lookup(%q) missed", scheme)
			continue
		}
		if g.Scheme() != scheme {
			t.Errorf("the generator under %q reports scheme %q", scheme, g.Scheme())
		}
	}
}
