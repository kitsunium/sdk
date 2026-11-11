package http2

import (
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// The bits of a huffTable word.
const (
	// huffEmit says a symbol ended on this nibble.
	huffEmit uint32 = 1 << 8
	// huffFail says no code can take this nibble: an EOS, or too much padding.
	huffFail uint32 = 1 << 9
	// huffStateMask extracts a word's next state.
	huffStateMask uint32 = 0xff
	// huffSymShift places the emitted symbol in a word.
	huffSymShift uint = 16
	// nibbleShift places a byte's high nibble.
	nibbleShift uint = 4
	// lowNibble extracts a byte's low nibble.
	lowNibble byte = 0x0f
)

// huffmanString decodes a Huffman-coded string into the scratch, RFC 7541
// §5.2: a nibble automaton, one table look-up a nibble, bounded by budget and
// by the input's own length, so a code cannot expand a string past what is
// allowed.
func (r *decodeRun) huffmanString(raw []byte, budget int) ([]byte, error) {
	start := len(r.scratch)
	state := uint32(0)
	scratch := r.scratch
	//: every byte is two nibbles; at most one symbol ends in each.
	for _, b := range raw {
		var err error
		//: a code that cannot go on ends the block.
		if scratch, state, err = huffStep(scratch, state, b>>nibbleShift); err != nil {
			return nil, err
		}
		//: the second nibble of the byte.
		if scratch, state, err = huffStep(scratch, state, b&lowNibble); err != nil {
			return nil, err
		}
		//: refused at the byte that passes the ceiling, not after the whole string.
		if len(scratch)-start > budget {
			return nil, corehttp2.HeadersTooLarge
		}
	}
	//: the string must end on a code boundary, with at most seven bits of EOS's prefix.
	if !huffAccept[state] {
		return nil, corehttp2.CompressionFailed
	}
	r.scratch = scratch
	return scratch[start:len(scratch):len(scratch)], nil
}

// huffStep runs the automaton over one nibble, appending the symbol a code
// ended with, if any.
func huffStep(scratch []byte, state uint32, nibble byte) ([]byte, uint32, error) {
	word := huffTable[state<<nibbleShift|uint32(nibble)]
	//: an EOS or padding past seven bits: the code has no such continuation.
	if word&huffFail != 0 {
		return nil, 0, corehttp2.CompressionFailed
	}
	//: this nibble ended a code: one symbol, at most, by the shortest code's five bits.
	if word&huffEmit != 0 {
		scratch = append(scratch, byte(word>>huffSymShift))
	}
	return scratch, word & huffStateMask, nil
}
