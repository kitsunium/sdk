package keytree_test

import (
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/keytree"
)

// hkdf is the registered HKDF-SHA256 algorithm key used across the tests.
const hkdf corecrypto.Algorithm = "hkdf-sha256"

// master32 returns a deterministic 32-byte master Key for derivation tests.
func master32(tb testing.TB) corecrypto.Key {
	tb.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	//: fill with a recognizable, non-zero pattern
	for i := range raw {
		raw[i] = byte(i + 7)
	}
	key, err := corecrypto.NewKey(raw)
	//: the fixture master key must construct cleanly
	if err != nil {
		//: a broken fixture aborts the test
		tb.Fatalf("NewKey: %v", err)
	}
	return key
}

// deriveBytes derives a node's key bytes, failing the test on any error.
func deriveBytes(tb testing.TB, node keytree.KeyTree) string {
	tb.Helper()
	k, err := node.DeriveKey()
	//: derivation along a registered algo must succeed
	if err != nil {
		//: a derivation fault aborts the test
		tb.Fatalf("derive: %v", err)
	}
	//: the derived key must be KeyLen bytes
	if len(k.Bytes()) != corecrypto.KeyLen {
		//: a wrong-length key aborts the test
		tb.Fatalf("len = %d want %d", len(k.Bytes()), corecrypto.KeyLen)
	}
	return string(k.Bytes())
}

// walk descends root through each segment in order.
func walk(root keytree.KeyTree, segs []string) keytree.KeyTree {
	//: descend the tree one segment at a time
	for _, s := range segs {
		root = root.Child(s)
	}
	return root
}

// Test_NewKeyTree asserts the root derives a KeyLen key deterministically.
func Test_NewKeyTree(t *testing.T) {
	t.Parallel()
	//: table-driven cases keep arms isolated
	cases := []struct {
		name string
		algo corecrypto.Algorithm
	}{
		{name: "hkdf", algo: hkdf},
	}
	check := func(t *testing.T, algo corecrypto.Algorithm) {
		t.Helper()
		a := deriveBytes(t, keytree.NewKeyTree(algo, master32(t)))
		b := deriveBytes(t, keytree.NewKeyTree(algo, master32(t)))
		//: derivation must be deterministic for the same master and path
		if a != b {
			//: a non-deterministic root is a contract violation
			t.Fatalf("non-deterministic root derivation")
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.algo)
		})
	}
}

// Test_KeyTree_Child asserts injective paths derive distinct keys and Child is
// pure (re-walking the same path re-derives the same key).
func Test_KeyTree_Child(t *testing.T) {
	t.Parallel()
	//: table-driven cases keep arms isolated; equal is the single discriminator
	cases := []struct {
		name  string
		left  []string
		right []string
		equal bool
	}{
		{name: "injective-collision-guard", left: []string{"a/b", "c"}, right: []string{"a", "b/c"}, equal: false},
		{name: "pure-receiver", left: []string{"a/b", "c"}, right: []string{"a/b", "c"}, equal: true},
	}
	check := func(t *testing.T, left, right []string, equal bool) {
		t.Helper()
		root := keytree.NewKeyTree(hkdf, master32(t))
		l := deriveBytes(t, walk(root, left))
		r := deriveBytes(t, walk(root, right))
		//: distinct canonical paths derive distinct keys; equal paths match
		if (l == r) != equal {
			//: a collision or spurious mismatch is a contract violation
			t.Fatalf("path equality = %v want %v", l == r, equal)
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.left, tc.right, tc.equal)
		})
	}
}

// Test_KeyTree_DeriveKey asserts re-derivation is path-stable and that an
// unregistered algorithm surfaces the core UnknownKDFAlgorithm sentinel.
func Test_KeyTree_DeriveKey(t *testing.T) {
	t.Parallel()
	//: table-driven cases keep arms isolated; wantErr is the single discriminator
	cases := []struct {
		name    string
		algo    corecrypto.Algorithm
		path    []string
		wantErr bool
	}{
		{name: "two-segment", algo: hkdf, path: []string{"svc", "db"}, wantErr: false},
		{name: "root", algo: hkdf, path: nil, wantErr: false},
		{name: "unregistered-algo", algo: "no-such-kdf", path: []string{"x"}, wantErr: true},
	}
	check := func(t *testing.T, algo corecrypto.Algorithm, path []string, wantErr bool) {
		t.Helper()
		node := walk(keytree.NewKeyTree(algo, master32(t)), path)
		//: an unregistered algorithm must surface the core sentinel
		if wantErr {
			_, err := node.DeriveKey()
			//: the failure must carry UnknownKDFAlgorithm
			if !errs.HasCode(err, corecrypto.CodeUnknownKDFAlgorithm) {
				//: any other shape is a contract violation
				t.Fatalf("err = %v want UnknownKDFAlgorithm", err)
			}
			return
		}
		first := deriveBytes(t, node)
		second := deriveBytes(t, node)
		//: repeated derivation at the same node must be stable
		if first != second {
			//: an unstable derivation is a contract violation
			t.Fatalf("unstable derivation")
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.algo, tc.path, tc.wantErr)
		})
	}
}
