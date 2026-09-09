// Package id_test — the NanoID generator as a consumer reaches it.
package id_test

import (
	"strings"
	"testing"

	coreid "github.com/kitsunium/sdk/internal/core/id"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcid "github.com/kitsunium/sdk/internal/service/id"
)

// nanoIDAlphabet is the URL-safe symbol set, restated here rather than imported
// so the test fails if the production constant is quietly widened. Every symbol
// survives a URL, a filename and a shell word unescaped, which is the property
// a consumer picks the format for.
const nanoIDAlphabet string = "_-0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// TestNanoID pins that blank-importing the package registers the scheme and
// that what comes out is a usable NanoID. The registration is the part a
// consumer cannot verify any other way: nothing in their code names nanoIDGen.
func TestNanoID(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mint func() (string, error)
	}
	tests := []tc{
		{"through the exported generator", svcid.NanoID.New},
		{"through the registry", func() (string, error) { return coreid.New("nanoid") }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := c.mint()
		if err != nil {
			t.Fatalf("New = %v, want nil", err)
		}
		//: 21 characters is the canonical default length.
		if len(got) != 21 {
			t.Fatalf("New() = %q (%d chars), want 21", got, len(got))
		}
		for _, r := range got {
			if !strings.ContainsRune(nanoIDAlphabet, r) {
				t.Errorf("New() = %q contains %q, outside the URL-safe alphabet", got, r)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the scheme must resolve by name, which is what the blank import buys.
	if !coreid.Scheme("nanoid").Known() {
		t.Error("the nanoid scheme did not self-register on import")
	}
}

// TestNewNanoID pins the explicit-length constructor and its refusal. A
// consumer who asks for a zero-length identifier must be told, not handed a
// generator that returns "" from every call (ADR 0031).
func TestNewNanoID(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		size int
		//: whether the constructor must hand back a working generator.
		ok bool
	}
	tests := []tc{
		{"a short identifier", 8, true},
		{"a long identifier", 64, true},
		{"a single character", 1, true},
		{"zero is refused", 0, false},
		{"a negative length is refused", -3, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		gen, err := svcid.NewNanoID(c.size)
		if !c.ok {
			if err == nil {
				t.Fatalf("NewNanoID(%d) = nil error, want a refusal", c.size)
			}
			if !errs.HasReason(err, "ID_INVALID_SIZE") {
				t.Errorf("NewNanoID(%d) = %v, want ID_INVALID_SIZE", c.size, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("NewNanoID(%d) = %v, want nil", c.size, err)
		}
		got, genErr := gen.New()
		if genErr != nil {
			t.Fatalf("New = %v, want nil", genErr)
		}
		if len(got) != c.size {
			t.Errorf("New() = %q (%d chars), want %d", got, len(got), c.size)
		}
		//: an explicitly-built generator stays out of the global registry, so
		//: it cannot shadow the default-size singleton for other consumers.
		if gen.Scheme() != "nanoid" {
			t.Errorf("Scheme() = %q, want %q", gen.Scheme(), "nanoid")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
