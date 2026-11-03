// Package stdhash — the conformance table of the hashers (kitsunium/sdk#1123):
// the contract every Hasher this package registers keeps, written once and run
// over each, the hasher's Algorithm in the subtest's name. It replaced the five
// per-hasher Algorithm and New tests, which differed by two words; an engine
// added later is held to every row, and hasherFacts fails it until its size
// and known answer are stated.
package stdhash

import (
	"bytes"
	"encoding/hex"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// hasherFact is what the table must be told about one hasher: its digest size
// and its digest of "abc", the published known answer (FIPS 180-4 and 202,
// the FNV and CRC-32C reference values).
type hasherFact struct {
	size int
	abc  string
}

// hasherFacts are the facts of every hasher this package registers, keyed by
// the frozen Algorithm.
var hasherFacts = map[corecrypto.Algorithm]hasherFact{
	"sha256":   {32, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	"sha512":   {64, "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"},
	"sha3-256": {32, "3a985da74fe225b2045c172d6bd390bd855f086e3e9d525b46bfe24511431532"},
	"fnv1a-64": {8, "e71fa2190541574b"},
	"crc32c":   {4, "364b3fb7"},
}

// everyHasher returns every Hasher the registry holds, read from it and
// never listed by hand.
func everyHasher(t *testing.T) []corecrypto.Hasher {
	t.Helper()
	var out []corecrypto.Hasher
	for _, algo := range corecrypto.AvailableHashers() {
		h, ok := corecrypto.LookupHasher(algo)
		if !ok {
			t.Fatalf("AvailableHashers lists %q, LookupHasher misses it", algo)
		}
		out = append(out, h)
	}
	if len(out) < len(hasherFacts) {
		t.Fatalf("the registry holds %d hashers, the package registers %d", len(out), len(hasherFacts))
	}
	return out
}

// TestHasherConformance runs every row over every registered hasher.
func TestHasherConformance(t *testing.T) {
	t.Parallel()
	for _, h := range everyHasher(t) {
		t.Run(string(h.Algorithm()), func(t *testing.T) {
			t.Parallel()
			fact, ok := hasherFacts[h.Algorithm()]
			//: a hasher the table knows nothing of is held to nothing.
			if !ok {
				t.Fatalf("hasher %q is registered with no size and known answer in hasherFacts", h.Algorithm())
			}
			//: Algorithm: registered under its own frozen key.
			if got, _ := corecrypto.LookupHasher(h.Algorithm()); got != h {
				t.Errorf("LookupHasher(%q) is not the hasher that reports it", h.Algorithm())
			}
			//: Size: the digest size, and the length of what Sum appends.
			first := h.New()
			if first.Size() != fact.size || len(first.Sum(nil)) != fact.size {
				t.Errorf("Size()=%d, len(Sum)=%d, want %d", first.Size(), len(first.Sum(nil)), fact.size)
			}
			//: the known answer, written in two pieces to cross a Write.
			if _, err := first.Write([]byte("a")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if _, err := first.Write([]byte("bc")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if got := hex.EncodeToString(first.Sum(nil)); got != fact.abc {
				t.Errorf("digest of \"abc\" = %s, want %s", got, fact.abc)
			}
			//: New: a fresh state each call — what one hash absorbed, the
			//: next does not start from.
			second := h.New()
			if bytes.Equal(second.Sum(nil), first.Sum(nil)) {
				t.Errorf("New() returned a hash already holding the previous one's input")
			}
		})
	}
}
