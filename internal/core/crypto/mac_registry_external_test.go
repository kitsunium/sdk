package crypto_test

import (
	"hash"
	"hash/fnv"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakeMAC is a comparable MAC whose ops return fixed bytes, so the
// registry/dispatch tests need no real cryptography.
type fakeMAC struct {
	name crypto.Algorithm
}

// : compile-time proof the fake satisfies the MAC port.
var _ crypto.MAC = (*fakeMAC)(nil)

func (f fakeMAC) Algorithm() crypto.Algorithm { return f.name }

func (fakeMAC) Tag(_ crypto.Key, message []byte) []byte { return append([]byte("tag:"), message...) }

func (fakeMAC) Verify(_ crypto.Key, _, _ []byte) bool { return true }

func (fakeMAC) New(_ crypto.Key) hash.Hash { return fnv.New64a() }

// distinctMAC is a SECOND MAC type claiming the same Algorithm as a fakeMAC, to
// exercise the distinct-duplicate conflict (a different dynamic type).
type distinctMAC struct{}

func (distinctMAC) Algorithm() crypto.Algorithm { return "mac-dup" }

func (distinctMAC) Tag(_ crypto.Key, _ []byte) []byte { return nil }

func (distinctMAC) Verify(_ crypto.Key, _, _ []byte) bool { return false }

func (distinctMAC) New(_ crypto.Key) hash.Hash { return fnv.New64a() }

func TestRegisterMAC(t *testing.T) {
	type tc struct {
		name string
		arg  fakeMAC
	}
	tests := []tc{
		{"first MAC registers", fakeMAC{name: "fm-1"}},
		{"second slot, distinct MAC", fakeMAC{name: "fm-2"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := crypto.RegisterMAC(c.arg)
		//: RegisterMAC hands back the MAC and it resolves immediately.
		if got.Algorithm() != c.arg.name {
			t.Errorf("RegisterMAC returned %q want %q", got.Algorithm(), c.arg.name)
		}
		if _, ok := crypto.LookupMAC(c.arg.name); !ok {
			t.Errorf("LookupMAC(%q) failed after RegisterMAC", c.arg.name)
		}
	}
	for _, c := range tests {
		//: sequential — mutates the process-wide MAC registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterMACPanics(t *testing.T) {
	type tc struct {
		name string
		run  func()
	}
	tests := []tc{
		{"nil MAC panics", func() { crypto.RegisterMAC(nil) }},
		{
			"distinct duplicate panics",
			func() {
				crypto.RegisterMAC(fakeMAC{name: "mac-dup"})
				//: a DISTINCT MAC under the same name is the hard conflict.
				crypto.RegisterMAC(distinctMAC{})
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			//: a missing panic means RegisterMAC failed to guard the case.
			if r := recover(); r == nil {
				t.Errorf("%s: expected panic, got none", c.name)
			}
		}()
		c.run()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestRegisterMACIdempotent(t *testing.T) {
	type tc struct {
		name string
		algo crypto.Algorithm
	}
	tests := []tc{
		{"same instance re-registers cleanly", "mac-idem-1"},
		{"second name re-registers cleanly", "mac-idem-2"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := fakeMAC{name: c.algo}
		crypto.RegisterMAC(m)
		//: re-registering the SAME instance is a no-op, never a panic.
		crypto.RegisterMAC(m)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestLookupMAC(t *testing.T) {
	crypto.RegisterMAC(fakeMAC{name: "lk-m"})
	type tc struct {
		name   string
		in     crypto.Algorithm
		wantOK bool
	}
	tests := []tc{
		{"registered MAC resolves", "lk-m", true},
		{"unregistered misses", "absent-zzz-m", false},
		{"empty algorithm misses", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: LookupMAC reports presence by Algorithm.
		if _, ok := crypto.LookupMAC(c.in); ok != c.wantOK {
			t.Errorf("%s: LookupMAC(%q)=%v want %v", c.name, c.in, ok, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestAvailableMACs(t *testing.T) {
	type tc struct {
		name string
		seed crypto.Algorithm
	}
	tests := []tc{
		{"first seeded MAC is listed", "av-m-a"},
		{"second seeded MAC is listed", "av-m-b"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		crypto.RegisterMAC(fakeMAC{name: c.seed})
		//: AvailableMACs must include every registered algorithm.
		if !slices.Contains(crypto.AvailableMACs(), c.seed) {
			t.Errorf("%s: AvailableMACs missing %q", c.name, c.seed)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestMACTag(t *testing.T) {
	crypto.RegisterMAC(fakeMAC{name: "tag-m"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"registered MAC produces a tag", "tag-m", false},
		{"unregistered algorithm errors", "ghost-m", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		tag, err := crypto.MACTag(c.alg, crypto.Key{}, []byte("x"))
		//: the failure arm surfaces UnknownMACAlgorithm + a nil tag.
		if c.wantErr {
			if tag != nil || !errs.HasCode(err, crypto.CodeUnknownMACAlgorithm) {
				t.Errorf("%s: MACTag=(%v,%v) want (nil,UnknownMACAlgorithm)", c.name, tag, err)
			}
			return
		}
		//: the success arm returns the scheme's tag bytes.
		if err != nil || string(tag) != "tag:x" {
			t.Errorf("%s: MACTag=(%q,%v) want (tag:x,nil)", c.name, tag, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestMACVerify(t *testing.T) {
	crypto.RegisterMAC(fakeMAC{name: "vfy-m"})
	type tc struct {
		name    string
		alg     crypto.Algorithm
		wantOK  bool
		wantErr bool
	}
	tests := []tc{
		{"registered MAC reports validity", "vfy-m", true, false},
		{"unregistered algorithm errors", "ghost-m", false, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ok, err := crypto.MACVerify(c.alg, crypto.Key{}, []byte("x"), []byte("t"))
		//: the failure arm distinguishes "scheme missing" via a sentinel, ok=false.
		if c.wantErr {
			if ok || !errs.HasCode(err, crypto.CodeUnknownMACAlgorithm) {
				t.Errorf("%s: MACVerify=(%v,%v) want (false,UnknownMACAlgorithm)", c.name, ok, err)
			}
			return
		}
		//: a registered scheme reports validity with no error channel.
		if err != nil || ok != c.wantOK {
			t.Errorf("%s: MACVerify=(%v,%v) want (%v,nil)", c.name, ok, err, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
