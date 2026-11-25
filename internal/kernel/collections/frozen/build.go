package frozen

import (
	"math"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxKeys is the most keys a table holds: its slots, n·100/α of them, are
// counted in a uint32.
const maxKeys = math.MaxUint32 / 100 * loadPercent

// writer fills the record of key i at rec and its blob bytes at blob, and
// returns how many blob bytes it wrote.
type writer func(i int, rec, blob []byte, off uint32) uint32

// build solves the hash function of n keys and writes the image of shape s:
// hash and same as solve takes them, blob the blob's size in bytes, write each
// key's record, in slot order so the blob is in the records' order.
func build(s shape, n int, blob uint64, hash func(seed uint64, i int) uint64, same func(i, j int) bool, write writer) (table, error) {
	if uint64(n) > maxKeys || blob > math.MaxUint32 {
		return table{}, origin(TooLarge, errs.Int64("keys", int64(n)), errs.Int64("blob", int64(min(blob, math.MaxInt64))))
	}
	p, dup, ok := solve(n, hash, same)
	if !ok {
		if dup >= 0 {
			return table{}, origin(DuplicateKey, errs.Int64("index", int64(dup)))
		}
		return table{}, origin(Unsolved, errs.Int64("keys", int64(n)))
	}
	t := table{seed: p.seed, n: p.n, nb: p.nb, m: p.m, shape: s}
	l := layoutOf(s, p.n, p.nb, p.m, blob)
	if l.size > math.MaxInt {
		return table{}, origin(TooLarge, errs.Int64("keys", int64(n)), errs.Int64("blob", int64(blob)))
	}
	image := make([]byte, l.size)
	copy(image[headerSize:], p.pilots)
	for i, x := range p.remap {
		le.PutUint32(image[l.remap+4*uint64(i):], x)
	}
	at := make([]uint32, n)
	for i, x := range p.pos {
		at[x] = uint32(i)
	}
	stride := uint64(s.stride())
	body := image[l.blob:]
	off := uint32(0)
	for x, i := range at {
		rec := image[l.recs+uint64(x)*stride:]
		off += write(int(i), rec[:stride], body[off:], off)
		image[l.tags+uint64(x)] = byte(p.hs[i])
	}
	t.view(image, l)
	writeHeader(image, &t)
	return t, nil
}

// widthOf is T's size in bytes and whether it is signed, found by shifting a
// one out of it.
func widthOf[T Integer]() (byte, bool) {
	var n byte
	for one := T(1); one != 0; one <<= 1 {
		n++
	}
	var zero T
	return n / 8, zero-1 < zero
}

// signs packs the two signedness flags of a shape.
func signs(key, value bool) byte {
	var s byte
	if key {
		s |= 1
	}
	if value {
		s |= 2
	}
	return s
}

// lengths refuses keys and values of different lengths.
func lengths(keys, values int) error {
	if keys == values {
		return nil
	}
	return origin(LengthMismatch, errs.Int64("keys", int64(keys)), errs.Int64("values", int64(values)))
}

// origin builds a fresh error on its own sentinel, with fields.
func origin(sentinel *errs.Error, fields ...errs.FieldValue) error {
	return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
}
