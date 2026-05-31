package keytree_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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

// refEncodePath reconstructs the frozen canonical HKDF info independently of
// the production encodePath: per segment, a 4-byte big-endian length prefix
// followed by the segment bytes. Any drift in prefix width, endianness, or
// layout makes this reference diverge from production and fails the KAT.
func refEncodePath(path []string) []byte {
	var buf []byte
	//: length-prefix each segment exactly as the frozen wire format requires
	for _, seg := range path {
		var lp [4]byte
		binary.BigEndian.PutUint32(lp[:], uint32(len(seg)))
		buf = append(buf, lp[:]...)
		buf = append(buf, seg...)
	}
	return buf
}

// refHKDFSHA256 is a stdlib-only RFC 5869 HKDF-SHA256 reference (empty salt),
// independent of the production deriver. It freezes the extract+expand
// construction so a change to salt handling or hash choice fails the KAT.
func refHKDFSHA256(secret, info []byte, length int) []byte {
	//: extract — empty salt becomes a zero block of the hash size (RFC 5869)
	salt := make([]byte, sha256.Size)
	extract := hmac.New(sha256.New, salt)
	extract.Write(secret)
	prk := extract.Sum(nil)

	//: expand — T(n) = HMAC(prk, T(n-1) | info | n) concatenated to length
	out := make([]byte, 0, length)
	var prev []byte
	counter := byte(1)
	//: emit blocks until the requested length is satisfied
	for len(out) < length {
		expand := hmac.New(sha256.New, prk)
		expand.Write(prev)
		expand.Write(info)
		expand.Write([]byte{counter})
		prev = expand.Sum(nil)
		out = append(out, prev...)
		counter++
	}
	return out[:length]
}

// Test_KeyTree_DeriveKey_KAT pins a known-answer vector for the derivation.
//
// The encodePath layout (4-byte big-endian length prefix per segment) plus the
// HKDF-SHA256 info construction is a frozen derivation wire format: it
// determines the AEAD key every downstream consumer receives for a given
// (master, path). The relative-property tests (determinism, injectivity,
// stability) all still pass if the prefix width, endianness, or info layout
// changes -- silently re-keying every existing path. This KAT fixes a literal
// 32-byte hex master and a literal expected 32-byte hex key, AND cross-checks
// against an independent stdlib reference (refEncodePath + refHKDFSHA256), so
// any change to the frozen format breaks it loudly instead of silently.
func Test_KeyTree_DeriveKey_KAT(t *testing.T) {
	t.Parallel()
	//: table-driven cases keep arms isolated; each row is a frozen vector
	cases := []struct {
		name      string
		masterHex string
		path      []string
		wantHex   string
	}{
		{
			name:      "svc-db",
			masterHex: "0708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20212223242526",
			path:      []string{"svc", "db"},
			wantHex:   "bdc7d469e64e188c7924aacafcb55145d152d3b70f0c3d0b994cc458d13ef384",
		},
	}
	check := func(t *testing.T, masterHex string, path []string, wantHex string) {
		t.Helper()
		masterRaw, err := hex.DecodeString(masterHex)
		//: the pinned master literal must decode cleanly
		if err != nil {
			//: a malformed fixture aborts the test
			t.Fatalf("decode master: %v", err)
		}
		master, err := corecrypto.NewKey(masterRaw)
		//: the fixture master key must construct cleanly
		if err != nil {
			//: a broken fixture aborts the test
			t.Fatalf("NewKey: %v", err)
		}
		node := walk(keytree.NewKeyTree(hkdf, master), path)
		key, err := node.DeriveKey()
		//: derivation along the registered algo must succeed
		if err != nil {
			//: a derivation fault aborts the test
			t.Fatalf("DeriveKey: %v", err)
		}
		got := key.Bytes()
		want, err := hex.DecodeString(wantHex)
		//: the pinned expected literal must decode cleanly
		if err != nil {
			//: a malformed fixture aborts the test
			t.Fatalf("decode want: %v", err)
		}
		//: the literal vector locks the frozen output byte-for-byte
		if !bytes.Equal(got, want) {
			//: any change to encodePath or the HKDF info contract changes this vector
			t.Fatalf("KAT mismatch: derivation wire format changed\n got=%x\nwant=%x", got, want)
		}
		//: the independent reference cross-checks the encodePath + HKDF contract
		ref := refHKDFSHA256(masterRaw, refEncodePath(path), corecrypto.KeyLen)
		//: production and the from-scratch reference must agree on the vector
		if !bytes.Equal(got, ref) {
			//: a divergence means the production format drifted from RFC 5869
			t.Fatalf("KAT vs reference mismatch\n got=%x\n ref=%x", got, ref)
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.masterHex, tc.path, tc.wantHex)
		})
	}
}
