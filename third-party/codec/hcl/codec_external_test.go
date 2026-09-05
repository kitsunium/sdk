// Package hcl_test — the codec as a consumer sees it: one registered singleton
// reachable by name, MIME type and file extension.
package hcl_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/third-party/codec/hcl"
)

// cfg is a top-level HCL document fixture (gohcl needs a tagged struct).
type cfg struct {
	Name    string `hcl:"name"`
	Replica int    `hcl:"replica"`
}

// TestNew pins that the package registers itself on import and that every
// documented key resolves to the SAME singleton. Three keys resolving to three
// codecs would still pass a per-key lookup test while doubling the work a
// content-negotiating caller does.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		lookup func() (codec.Codec, bool)
	}
	tests := []tc{
		{"by format name", func() (codec.Codec, bool) { return codec.Lookup("hcl") }},
		{"by MIME type", func() (codec.Codec, bool) { return codec.LookupMIME("application/hcl") }},
		{"by file extension", func() (codec.Codec, bool) { return codec.LookupExt(".hcl") }},
	}
	want := hcl.New()
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := c.lookup()
		if !ok {
			t.Fatalf("lookup %s missed — the codec did not self-register", c.name)
		}
		//: the same singleton, not merely an equivalent codec.
		if got != want {
			t.Errorf("lookup %s resolved a different instance", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: New is stateless, so repeated calls must not mint new instances.
	if hcl.New() != want {
		t.Error("New() returned a different instance on a second call")
	}
}

// TestRoundTrip is the consumer-level contract: what this codec writes, it can
// read back. Marshal and Unmarshal are tested individually inside the package;
// what only shows up here is that the two agree.
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   cfg
	}
	tests := []tc{
		{"an ordinary document", cfg{Name: "kitsunium", Replica: 3}},
		{"a zero document", cfg{}},
		{"a name needing quoting", cfg{Name: `a "quoted" name`, Replica: 1}},
		{"a negative count", cfg{Name: "kitsunium", Replica: -1}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		codc := hcl.New()
		data, err := codc.Marshal(c.in)
		if err != nil {
			t.Fatalf("Marshal(%+v) = %v, want nil", c.in, err)
		}
		var got cfg
		if uerr := codc.Unmarshal(data, &got); uerr != nil {
			t.Fatalf("Unmarshal(%q) = %v, want nil", data, uerr)
		}
		if got != c.in {
			t.Errorf("the round trip turned %+v into %+v", c.in, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
