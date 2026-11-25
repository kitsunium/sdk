package frozen

import (
	"hash/crc32"
	"math"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Check is how much of an image a load verifies before the table reads it
// in place.
type Check uint8

const (
	// CheckHeader verifies the header alone — its magic, CRC-32C, version,
	// shape, and that its sizes add up to the image's length — in constant
	// time. It is for bytes the caller wrote itself and kept intact: a body
	// damaged after the header was sealed is not detected, and a Get on it may
	// return a wrong answer or panic.
	CheckHeader Check = iota
	// CheckAll verifies the header, then the body's CRC-32C, every remapped
	// slot, every string's bounds, every tag, and that every key leads back to
	// its own slot — in time linear in the image. A table loaded under it
	// never panics and finds every key it holds: it is the mode for bytes from
	// anywhere else.
	CheckAll
)

// corrupt is the error of an image that is not one, for why.
func corrupt(why string) error {
	return origin(ImageCorrupt, errs.String("why", why))
}

// load reads image as a table of shape want, verifying it under check; the
// table reads image in place, so the caller keeps it alive and unchanged.
func load(image []byte, want shape, check Check) (table, error) {
	if uint64(len(image)) < headerSize {
		return table{}, corrupt("shorter than the header")
	}
	h := image[:headerSize]
	if string(h[:4]) != magic {
		return table{}, corrupt("magic")
	}
	if crc32.Checksum(h[:headerSealed], castagnoli) != byteOrder.Uint32(h[headerSealed:]) {
		return table{}, corrupt("header checksum")
	}
	if v := byteOrder.Uint16(h[4:]); v != version {
		return table{}, origin(ImageVersion, errs.Int64("version", int64(v)))
	}
	got := shape{kind: h[6], kw: h[7], vw: h[8], signs: h[9]}
	if got != want {
		return table{}, origin(ImageShape, errs.Int64("kind", int64(got.kind)), errs.Int64("key_width", int64(got.kw)), errs.Int64("value_width", int64(got.vw)), errs.Int64("signs", int64(got.signs)))
	}
	if byteOrder.Uint16(h[10:]) != 0 || byteOrder.Uint64(h[52:]) != 0 {
		return table{}, corrupt("reserved bytes")
	}
	t := table{n: byteOrder.Uint32(h[12:]), nb: byteOrder.Uint32(h[16:]), m: byteOrder.Uint32(h[20:]), seed: byteOrder.Uint64(h[24:]), shape: got}
	if err := sizes(&t); err != nil {
		return table{}, err
	}
	blob := byteOrder.Uint64(h[32:])
	if blob > math.MaxUint32 {
		return table{}, corrupt("blob size")
	}
	l := layoutOf(got, t.n, t.nb, t.m, blob)
	if l.size != uint64(len(image)) || byteOrder.Uint64(h[40:]) != l.size {
		return table{}, corrupt("image size")
	}
	t.view(image, l)
	if check == CheckAll {
		if crc32.Checksum(image[headerSize:], castagnoli) != byteOrder.Uint32(h[48:]) {
			return table{}, corrupt("body checksum")
		}
		if err := t.verify(); err != nil {
			return table{}, err
		}
	}
	return t, nil
}

// sizes refuses counts no build writes: buckets without keys or keys without
// buckets, fewer slots than keys, or more keys than a table holds.
func sizes(t *table) error {
	if t.n == 0 {
		if t.nb != 0 || t.m != 0 {
			return corrupt("an empty table with buckets or slots")
		}
		return nil
	}
	if t.nb == 0 || t.m < t.n || uint64(t.n) > maxKeys {
		return corrupt("bucket or slot count")
	}
	return nil
}

// verify walks the body: every remapped slot is below n, every string lies in
// the blob, and every record's key hashes to that record's own slot — which
// also proves no two records hold one key.
func (t *table) verify() error {
	for i := uint32(0); i < t.m-t.n; i++ {
		if byteOrder.Uint32(t.remap[4*uint64(i):]) >= t.n {
			return corrupt("remapped slot")
		}
	}
	for x := range t.n {
		var h uint64
		if t.shape.kind == kindInts {
			h = hashInt(t.seed, t.intKey(uint64(x)*uint64(t.stride)))
		} else {
			k, ok := t.checkEntry(t.entry(x))
			if !ok {
				return corrupt("string entry past the blob")
			}
			h = hashString(t.seed, k)
		}
		if t.tags[x] != byte(h) {
			return corrupt("tag")
		}
		if t.slot(h) != x {
			return corrupt("record out of its slot")
		}
	}
	return nil
}

// checkEntry returns the key of the string entry at off when the entry — its
// head, its key and its value — lies within the blob.
func (t *table) checkEntry(off uint32) (string, bool) {
	blob := uint64(len(t.blob))
	hs := uint64(t.shape.head())
	if uint64(off)+hs > blob {
		return "", false
	}
	end := uint64(off) + hs + load32(t.blob, int(off))
	if t.shape.kind == kindText {
		end += load32(t.blob, int(off)+int(width4))
	} else {
		end += uint64(t.shape.vw)
	}
	if end > blob {
		return "", false
	}
	return t.stringKey(off), true
}
