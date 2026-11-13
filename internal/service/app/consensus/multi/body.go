package multi

import (
	"encoding/binary"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The packed body's layout: a version byte, then uvarints, each seven bits a
// byte, the high bit set on every byte but the last.
const (
	// bodyVersion is the first byte of every packed body: the version of the
	// layout that follows. A body of another version does not parse.
	bodyVersion byte = 1
	// bodyHeaderMax is the most a body's header takes: its version byte and
	// the uvarint count of its commands.
	bodyHeaderMax int = 1 + binary.MaxVarintLen64
	// uvarintMore is the bit of a uvarint byte that says another follows: a
	// value at or above it needs one byte more.
	uvarintMore uint64 = 0x80
	// uvarintBits is how many bits of the value one uvarint byte carries.
	uvarintBits uint = 7
)

// cmdCost is what cmd adds to a packed body: its uvarint length, then its
// bytes.
func cmdCost(cmd []byte) int { return uvarintLen(uint64(len(cmd))) + len(cmd) }

// uvarintLen is the length of v's uvarint encoding.
func uvarintLen(v uint64) int {
	n := 1
	for v >= uvarintMore {
		v >>= uvarintBits
		n++
	}
	return n
}

// appendBody appends the packed body of cmds to dst: the version, the count
// of commands, then each command as its uvarint length and its bytes.
func appendBody(dst []byte, cmds [][]byte) []byte {
	dst = append(dst, bodyVersion)
	dst = binary.AppendUvarint(dst, uint64(len(cmds)))
	for _, c := range cmds {
		dst = binary.AppendUvarint(dst, uint64(len(c)))
		dst = append(dst, c...)
	}
	return dst
}

// splitBody appends to dst one entry per command of e's packed body, each a
// copy of e whose Data is the command — a slice of e.Data capped at its end —
// and whose BodySum is the command's own; ChainSum, which chains the log's
// headers, is zero. A body that does not parse is FRAME_INVALID and leaves
// dst as it was.
func splitBody(dst []coreconsensus.EntryValue, e *coreconsensus.EntryValue, sum func([]byte) uint32) ([]coreconsensus.EntryValue, error) {
	b := e.Data
	if len(b) == 0 || b[0] != bodyVersion {
		return dst, malformed("version")
	}
	b = b[1:]
	n, k := binary.Uvarint(b)
	// Every command takes one byte at least, its length: a count above the
	// bytes left cannot be, and is refused before anything is reserved.
	if k <= 0 || n > uint64(len(b)-k) {
		return dst, malformed("count")
	}
	b = b[k:]
	start := len(dst)
	for range n {
		l, k := binary.Uvarint(b)
		if k <= 0 || l > uint64(len(b)-k) {
			return dst[:start], malformed("command length")
		}
		cmd := b[k : k+int(l) : k+int(l)]
		b = b[k+int(l):]
		sub := *e
		sub.Data, sub.BodySum, sub.ChainSum = cmd, sum(cmd), 0
		dst = append(dst, sub)
	}
	if len(b) != 0 {
		return dst[:start], malformed("trailing bytes")
	}
	return dst, nil
}

// malformed is the FRAME_INVALID of a packed body, naming what failed.
func malformed(what string) error {
	return origin(coreconsensus.FrameInvalid, errs.String("body", what))
}

// origin returns sentinel itself with fields: an SDK cause keeps its code,
// reason and texts (origin wins on wrap), and gains the fields.
func origin(sentinel *errs.Error, fields ...errs.FieldValue) error {
	return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
}
