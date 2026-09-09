// Package id — NanoID generator (URL-safe alphabet, configurable length).
package id

import (
	coreid "github.com/kitsunium/sdk/internal/core/id"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// nanoIDAlphabet is the canonical NanoID symbol set: 64 URL-safe characters
	// (A-Z, a-z, 0-9, '_' and '-'). Every symbol survives a URL path, a query
	// string, a shell word and a filename unescaped, which is the whole reason
	// the format exists.
	nanoIDAlphabet string = "_-0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	// nanoIDDefaultSize is the canonical NanoID length. 21 characters over a
	// 64-symbol alphabet carry 126 bits, slightly MORE than a UUIDv4's 122, in
	// 21 characters instead of 36. It is the format's published default, not a
	// number invented here — which is what makes it a legitimate zero-value
	// default rather than a guess at the caller's intent (ADR 0031).
	nanoIDDefaultSize int = 21
)

// NanoID is the registered default-size NanoID generator: nanoIDDefaultSize
// URL-safe characters drawn uniformly from a CSPRNG. Use NewNanoID for an
// explicit length.
var NanoID = coreid.Register(nanoIDGen{size: nanoIDDefaultSize})

// nanoIDGen mints size characters drawn uniformly from nanoIDAlphabet. It is
// stateless beyond its length, so the singleton is safe to share.
type nanoIDGen struct {
	size int
}

// NewNanoID returns a NanoID Generator producing size characters. It is NOT
// added to the global registry — bind it to your own variable.
//
// A non-positive size is REFUSED rather than clamped (ADR 0031): a zero-length
// identifier identifies nothing, and any length chosen on the caller's behalf
// here would be the SDK substituting its judgement for the one decision this
// constructor exists to take. Callers who want the canonical length say so by
// not passing one — that is what the registered NanoID singleton is.
func NewNanoID(size int) (g coreid.Generator, err error) {
	//: an explicit non-positive length is a caller mistake, not a request for
	//: the default; refuse at construction so nothing degraded is ever minted.
	if size <= 0 {
		//: name the offending knob; the size is a bounded int, safe to echo.
		return nil, errs.Wrap(InvalidSize, errs.WrapParams{}, errs.Int("size", size))
	}
	//: a validated length yields a plain value generator.
	return nanoIDGen{size: size}, nil
}

// Scheme implements core/id.Generator.
func (nanoIDGen) Scheme() coreid.Scheme {
	//: the registered scheme key.
	return "nanoid"
}

// New draws g.size characters uniformly from the URL-safe alphabet.
func (g nanoIDGen) New() (newID string, err error) {
	//: one output byte per character, resolved through the zero-value rule.
	out := make([]byte, g.length())
	//: fill it with unbiased draws; the only failure is a CSPRNG fault.
	if rerr := randomFromAlphabet(out, nanoIDAlphabet); rerr != nil {
		//: propagate the wrapped entropy failure.
		return "", rerr
	}
	//: the assembled identifier.
	return string(out), nil
}

// length resolves the effective identifier length. The zero value of the struct
// means "no length was specified", which resolves to the canonical default —
// never to zero. NewNanoID still REFUSES an explicit 0 for the reason ADR 0031
// separates the two cases: an unset field is an absent opinion, while an
// argument passed to a constructor is an assertion the caller means it.
func (g nanoIDGen) length() int {
	//: an unset (or impossible) length falls back to the published default.
	if g.size <= 0 {
		//: the canonical 21-character rendering.
		return nanoIDDefaultSize
	}
	//: the length the constructor validated.
	return g.size
}

// randomFromAlphabet fills dst with characters drawn uniformly at random from
// alphabet, which MUST hold between 1 and 256 symbols.
//
// The obvious `alphabet[b%len(alphabet)]` is the defect every reimplementation
// ships. Unless the alphabet size divides the byte range exactly, the possible
// byte values do not spread evenly over the symbols: the ones at the start of
// the alphabet are reachable one extra way, so they appear more often in EVERY
// identifier the generator will ever mint. Nothing observable breaks — the ids
// still look random, still never repeat, still pass a uniqueness test — the
// distribution is simply skewed and the effective entropy is below what the
// length claims.
//
// So instead: mask each random byte down to the smallest 2^k-1 that spans the
// alphabet, and REJECT any masked value landing past the last symbol, redrawing
// for that position. Every accepted value is then equiprobable by construction,
// for any alphabet size.
//
// With the shipped 64-symbol alphabet the mask is exactly 6 bits, so no draw is
// ever rejected and the loop makes a single pass. The rejection is not dead
// weight for that: it is what keeps the function correct if the alphabet ever
// changes, and Test_randomFromAlphabet drives it with non-power-of-two
// alphabets precisely so the branch is exercised rather than assumed.
func randomFromAlphabet(dst []byte, alphabet string) error {
	//: the smallest 2^k-1 that can address every symbol.
	mask := pow2MaskFor(len(alphabet))
	//: draw in batches the width of the request; rejections cost another round.
	buf := make([]byte, len(dst))
	//: how many characters are already placed.
	filled := 0
	//: keep drawing until every position holds an accepted symbol.
	for filled < len(dst) {
		//: a fresh batch of secure random bytes.
		if rerr := readRandom(buf); rerr != nil {
			//: propagate the wrapped entropy failure.
			return rerr
		}
		//: consume the batch, skipping the values that fall outside the set.
		for _, b := range buf {
			//: keep only the low bits the mask spans.
			idx := int(b) & mask
			//: past the last symbol — reject and try the next byte.
			if idx >= len(alphabet) {
				//: skipping is what keeps the distribution uniform.
				continue
			}
			//: place the accepted symbol.
			dst[filled] = alphabet[idx]
			//: advance the write cursor.
			filled++
			//: stop as soon as the request is satisfied.
			if filled == len(dst) {
				//: the remainder of the batch is discarded.
				break
			}
		}
	}
	//: dst is fully populated with uniform draws.
	return nil
}

// pow2MaskFor returns the smallest 2^k-1 able to address n symbols, i.e. the
// mask keeping just enough low bits of a random byte to index the alphabet.
func pow2MaskFor(n int) int {
	//: grow a power of two until it spans the whole alphabet.
	mask := 1
	//: doubling stops at the first power of two that is >= n.
	for mask < n {
		//: next power of two.
		mask <<= 1
	}
	//: 2^k - 1 is the all-ones mask of that width.
	return mask - 1
}
