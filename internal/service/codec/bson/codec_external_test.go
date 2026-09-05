// Package bson_test — the codec as a consumer sees it: one registered singleton
// reachable by name, MIME type and file extension.
package bson_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/bson"
)

// doc is a BSON document fixture.
type doc struct {
	Name    string `bson:"name"`
	Replica int    `bson:"replica"`
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
		{"by format name", func() (codec.Codec, bool) { return codec.Lookup("bson") }},
		{"by MIME type", func() (codec.Codec, bool) { return codec.LookupMIME("application/bson") }},
		{"by file extension", func() (codec.Codec, bool) { return codec.LookupExt(".bson") }},
	}
	want := bson.New()
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
	if bson.New() != want {
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
		in   doc
	}
	tests := []tc{
		{"an ordinary document", doc{Name: "kitsunium", Replica: 3}},
		{"a zero document", doc{}},
		{"a name with non-ASCII bytes", doc{Name: "kitsuné", Replica: 1}},
		{"a negative count", doc{Name: "kitsunium", Replica: -1}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		codc := bson.New()
		data, err := codc.Marshal(c.in)
		if err != nil {
			t.Fatalf("Marshal(%+v) = %v, want nil", c.in, err)
		}
		var got doc
		if uerr := codc.Unmarshal(data, &got); uerr != nil {
			t.Fatalf("Unmarshal = %v, want nil", uerr)
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
