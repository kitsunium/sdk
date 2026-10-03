package keytree

import (
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// Test_KeyTree_withMasterBytes is the V66 regression: DeriveKey must wipe the
// master-key clone Key.Bytes mints rather than abandon it live to the GC. The
// withMasterBytes seam clears the clone the instant fn returns, so the buffer
// fn observed reads back all-zero afterwards. Before the fix this seam did not
// exist and the clone leaked uncleared on every derivation.
func Test_KeyTree_withMasterBytes(t *testing.T) {
	t.Parallel()
	//: table-driven cases keep arms isolated; seed is the per-case key pattern
	cases := []struct {
		name string
		seed byte
	}{
		{name: "low-pattern", seed: 1},
		{name: "high-pattern", seed: 200},
	}
	check := func(t *testing.T, seed byte) {
		t.Helper()
		raw := make([]byte, corecrypto.KeyLen)
		//: fill with a recognizable, non-zero pattern so a missed clear is visible
		for i := range raw {
			raw[i] = seed + byte(i)
		}
		master, err := corecrypto.NewKey(raw)
		//: the fixture master key must construct cleanly
		if err != nil {
			//: a broken fixture aborts the test
			t.Fatalf("NewKey: %v", err)
		}
		//: any algo works — the seam clears regardless of derivation outcome
		tree := NewKeyTree(corecrypto.Algorithm("hkdf-sha256"), master)
		var captured []byte
		//: capture the exact clone the seam handed the callback
		tree.withMasterBytes(func(b []byte) {
			captured = b
		})
		//: the clone must be fully wiped once the seam returns — no live secret
		for i, v := range captured {
			//: any surviving non-zero byte means the master clone leaked
			if v != 0 {
				t.Fatalf("master clone byte %d = %d, want 0 (clone leaked uncleared)", i, v)
			}
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.seed)
		})
	}
}

// Test_encodePath asserts the length-prefixed encoding is injective.
func Test_encodePath(t *testing.T) {
	t.Parallel()
	//: table-driven cases keep arms isolated; want is the single discriminator
	cases := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{name: "collision-guard", a: []string{"a/b", "c"}, b: []string{"a", "b/c"}, want: false},
		{name: "same-path", a: []string{"x", "y"}, b: []string{"x", "y"}, want: true},
		{name: "empty-vs-empty-seg", a: []string{""}, b: nil, want: false},
	}
	check := func(t *testing.T, a, b []string, want bool) {
		t.Helper()
		//: equal encodings must occur only for equal canonical paths
		if got := encodePath(a) == encodePath(b); got != want {
			t.Fatalf("encodePath equality = %v want %v", got, want)
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.a, tc.b, tc.want)
		})
	}
}
