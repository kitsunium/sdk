// Package id_test — black-box tests for the process-wide Generator registry.
//
// These tests are NOT parallel with each other: the registry is process-wide by
// design (schemes register at import), so Available() observes every scheme any
// test has published. Running them serially keeps that observation stable.
package id_test

import (
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/core/id"
)

// TestRegister pins the boot-time contract: a generator is handed back for
// singleton binding, a re-registration of the very same value is a no-op, and
// anything the registry cannot honour panics at import rather than failing at
// the first New() in production.
func TestRegister(t *testing.T) {
	type tc struct {
		name      string
		register  func()
		wantPanic bool
	}
	shared := stubGen{name: "reg-idempotent"}
	tests := []tc{
		{
			name:     "a fresh scheme",
			register: func() { id.Register(stubGen{name: "reg-fresh"}) },
		},
		{
			//: re-publishing the identical value is how a package imported
			//: twice through different paths must behave.
			name: "the same generator twice",
			register: func() {
				id.Register(shared)
				id.Register(shared)
			},
		},
		{
			name: "a distinct generator on a taken scheme",
			register: func() {
				id.Register(stubGen{name: "reg-conflict"})
				id.Register(stubGen2{name: "reg-conflict"})
			},
			wantPanic: true,
		},
		{
			//: Scheme("") is documented invalid and Known() hard-codes false
			//: for it; publishing under it would leave Lookup resolving a key
			//: Known() reports as absent.
			name:      "the reserved empty scheme",
			register:  func() { id.Register(stubGen{name: ""}) },
			wantPanic: true,
		},
		{
			name:      "a nil generator",
			register:  func() { id.Register(nil) },
			wantPanic: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			r := recover()
			if c.wantPanic && r == nil {
				t.Errorf("%s did not panic", c.name)
			}
			if !c.wantPanic && r != nil {
				t.Errorf("%s panicked: %v", c.name, r)
			}
		}()
		c.register()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
	//: Register hands the generator back so a caller can bind the singleton.
	if got := id.Register(stubGen{name: "reg-returned"}); got.Scheme() != "reg-returned" {
		t.Errorf("Register returned scheme %q, want reg-returned", got.Scheme())
	}
}

// TestLookup pins that resolution agrees with registration in both directions:
// what was published resolves, and what was refused stays unresolvable.
func TestLookup(t *testing.T) {
	id.Register(stubGen{name: "lookup-present"})

	type tc struct {
		name    string
		scheme  id.Scheme
		wantOK  bool
		wantSch id.Scheme
	}
	tests := []tc{
		{"a registered scheme", "lookup-present", true, "lookup-present"},
		{"a scheme nobody registered", "lookup-absent", false, ""},
		//: the refused empty registration must have left no entry behind.
		{"the reserved empty scheme", "", false, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g, ok := id.Lookup(c.scheme)
		if ok != c.wantOK {
			t.Fatalf("Lookup(%q) ok = %v, want %v", c.scheme, ok, c.wantOK)
		}
		if !ok {
			//: a miss must hand back no generator at all, or a caller checking
			//: only the value would dispatch into a nil interface.
			if g != nil {
				t.Errorf("Lookup(%q) missed but returned %v", c.scheme, g)
			}
			return
		}
		if g.Scheme() != c.wantSch {
			t.Errorf("Lookup(%q) returned scheme %q", c.scheme, g.Scheme())
		}
		//: Known must agree with Lookup — they are the same question.
		if !c.scheme.Known() {
			t.Errorf("Scheme(%q).Known() = false but Lookup resolved it", c.scheme)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestAvailable pins that the listing is complete and sorted. Callers print it
// in help text and diff it in tests, so an unstable order would be a source of
// spurious churn.
func TestAvailable(t *testing.T) {
	type tc struct {
		name   string
		scheme id.Scheme
	}
	tests := []tc{
		{"the first listed scheme", "avail-a"},
		{"a scheme sorting after it", "avail-b"},
		{"a scheme sorting between them", "avail-ab"},
	}
	for _, c := range tests {
		id.Register(stubGen{name: c.scheme})
	}

	got := id.Available()
	//: ascending order is the documented contract.
	if !slices.IsSorted(got) {
		t.Errorf("Available() = %v, want it sorted", got)
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !slices.Contains(got, c.scheme) {
			t.Errorf("Available() = %v, missing %q", got, c.scheme)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
	//: the refused empty scheme must never appear in the listing.
	if slices.Contains(got, "") {
		t.Errorf("Available() = %v, contains the reserved empty scheme", got)
	}
}
