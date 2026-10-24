package token

import (
	"crypto/hmac"
	"hash"
	"strconv"
	"sync/atomic"
)

// scratchSumLen is the widest tag a scratch sums into: SHA-512's 64 octets,
// so a scratch is never the reason a wider MAC cannot be carried.
const scratchSumLen int = 64

// VerifyScratch is the mutable MAC state of one [VerifierInto] caller: a keyed
// stdlib hash.Hash and the token of the verifier that made it, nothing else.
// It exists so a verifier can stay immutable and concurrent while a caller
// that verifies on one thread — an event loop — reuses one keyed MAC instead
// of building a new one per token.
//
// It is NOT safe for concurrent use, and that is its whole contract: one
// scratch per thread. A scratch entered twice at once refuses the second entry
// with [ScratchBusy] before computing anything, and a scratch made by another
// verifier is refused with [ScratchForeign]. The tag it sums into is cleared
// after every comparison, whatever its outcome.
//
// The zero VerifyScratch belongs to no verifier and is refused by every one.
type VerifyScratch struct {
	// mac is the keyed MAC this scratch reuses, Reset before every use.
	mac hash.Hash
	// owner is the token of the verifier that made the scratch; compared by
	// identity, never read.
	owner any
	// inUse is 1 while a verification holds the scratch: entered by a CAS
	// from 0, left by a store of 0.
	inUse atomic.Uint32
	// sum receives the computed tag, so summing allocates nothing.
	sum [scratchSumLen]byte
}

// NewVerifyScratch returns a scratch around mac for the verifier whose token
// is owner. It is the constructor a [VerifierInto] implementation calls from
// NewScratch; a caller asks the verifier, never this function, because only
// the verifier knows its owner token. mac is Reset once here, so the first
// verification pays nothing the later ones do not.
func NewVerifyScratch(owner any, mac hash.Hash) *VerifyScratch {
	//: prime the keyed state once, at construction.
	mac.Reset()
	//: a fresh scratch, idle.
	return &VerifyScratch{mac: mac, owner: owner}
}

// Enter claims s for one verification by the verifier whose token is owner.
// It refuses a nil scratch, the zero scratch and one made for another owner
// with [ScratchForeign], and a scratch another call holds with [ScratchBusy] —
// in every case before any MAC is computed. A nil error must be paired with
// exactly one [VerifyScratch.Leave].
func (s *VerifyScratch) Enter(owner any) error {
	//: a scratch nobody made, or another verifier made, is not this one's.
	if s == nil || s.owner == nil || s.owner != owner {
		//: refuse without touching the MAC.
		return ScratchForeign
	}
	//: one holder at a time: the CAS is the only way in.
	if !s.inUse.CompareAndSwap(0, 1) {
		//: shared across threads: refuse rather than interleave two MACs.
		return ScratchBusy
	}
	//: held.
	return nil
}

// Leave clears the tag s summed into and releases s for the next
// verification. It is called once per successful [VerifyScratch.Enter], on
// every exit path.
func (s *VerifyScratch) Leave() {
	//: nothing of the last tag survives the call that computed it.
	clear(s.sum[:])
	//: release.
	s.inUse.Store(0)
}

// MACEqual reports whether tag authenticates input under the scratch's key,
// comparing with hmac.Equal so the check is constant-time. It reports false
// on a scratch that is not entered, and clears the computed tag before it
// returns, whatever the outcome.
func (s *VerifyScratch) MACEqual(input, tag []byte) bool {
	//: only the holder may drive the MAC.
	if s.inUse.Load() != 1 {
		//: never sum on a scratch nobody entered.
		return false
	}
	//: Reset restores the keyed inner and outer pads; nothing of the last
	//: input remains.
	s.mac.Reset()
	//: Write on a hash.Hash never returns an error (documented contract).
	s.mac.Write(input)
	//: sum into the scratch's own array: no allocation.
	sum := s.mac.Sum(s.sum[:0])
	equal := hmac.Equal(sum, tag)
	//: the tag is cleared whether it matched or not.
	clear(s.sum[:])
	//: constant-time verdict.
	return equal
}

// String implements fmt.Stringer and renders the SHAPE of s, never its state:
// "token.VerifyScratch{owned:true busy:false}".
func (s *VerifyScratch) String() string {
	//: a nil scratch has a shape too.
	if s == nil {
		//: nothing owned, nothing held.
		return "token.VerifyScratch{nil}"
	}
	//: ownership and occupancy only — no key, no tag, no owner.
	return "token.VerifyScratch{owned:" + strconv.FormatBool(s.owner != nil) +
		" busy:" + strconv.FormatBool(s.inUse.Load() == 1) + "}"
}

// GoString implements fmt.GoStringer so %#v stays redacted too — fmt bypasses
// String for Go-syntax formatting and would otherwise dump the keyed MAC.
func (s *VerifyScratch) GoString() string {
	//: same shape-only rendering as String.
	return s.String()
}
