package server

import (
	"math/bits"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// optionSet is the set of option kinds a group was given (ADR 0187), one bit
// per corenet.OptionKind. The kind is an iota, not a bit: the set does the
// shifting, so a kind added at the end of the block shifts no published value.
// A refusal reads the set, so an option given with its zero value — a
// ReadTimeout(0) — counts as given, which a guess from non-zero fields missed.
type optionSet uint64

// add records that an option of kind k was given. A kind past the set's width
// cannot be recorded; Test_optionSet_width keeps every kind within it.
func (s *optionSet) add(k corenet.OptionKind) {
	//: one bit per kind, the shift past 63 yielding no bit at all.
	*s |= 1 << k
}

// has reports whether an option of kind k was given.
func (s optionSet) has(k corenet.OptionKind) bool {
	//: the kind's own bit.
	return s&(1<<k) != 0
}

// list returns the kinds given, in the constant block's order, so the first
// refusal reported is reproducible.
func (s optionSet) list() []corenet.OptionKind {
	out := make([]corenet.OptionKind, 0, bits.OnesCount64(uint64(s)))
	//: lowest bit first, which is the block's order.
	for rest := uint64(s); rest != 0; rest &= rest - 1 {
		out = append(out, corenet.OptionKind(bits.TrailingZeros64(rest)))
	}
	//: possibly empty, never nil-checked by a caller.
	return out
}
