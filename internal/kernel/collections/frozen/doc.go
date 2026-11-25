// Package frozen provides read-only maps built once from a known key set and
// read in place: Map (integer keys, integer values), StringMap (string keys,
// integer values) and Dict (string keys, string values). A kernel primitive:
// stdlib-only, and generic down to its signatures — keys, values and bytes.
//
// # The hash function
//
// A build searches for a minimal perfect hash function over its keys, in the
// manner of PTHash and PtrHash: the keys fall into buckets of about three, the
// largest bucket first, and each bucket is given the one-byte pilot that sends
// all of its keys to free slots, evicting a cheaper bucket when no pilot finds
// them free. The table has n·100/97 slots; the few keys that land past n are
// remapped onto the holes below it. n keys take exactly n records, with no
// empty one.
//
// A lookup hashes the key, reads its bucket's pilot (a third of a byte a key,
// which stays in cache), computes the slot, and reads the slot's tag — a byte
// of the key's hash — before anything else, so an absent key is almost always
// refused without touching a record. A present key then costs one record
// read for an integer table, and the offset and one blob read for a string
// table. Nothing is allocated, and a table is safe for concurrent use: it is
// never written once built.
//
// # The image
//
// A table IS its image: one byte slice laid out as the image format says, read
// in place with encoding/binary. A table of any size therefore holds the same
// handful of pointers and nothing the collector scans. AppendBinary and
// MarshalBinary write the image; LoadMap, LoadStringMap and LoadDict read one
// without copying it, so the bytes the caller passes are the table's for as
// long as the table lives — the caller keeps them alive and unchanged. No file
// is mapped here: the kernel is stdlib-only and every platform's, and the
// caller decides where the bytes come from.
//
// The image is little-endian and versioned: a 64-byte header (magic, version,
// the key and value types' shape, the counts, the seed, the sizes, a CRC-32C
// of the body and one of the header), then the pilots, the tags, the remap,
// the records and the blob. A build is a function of its key set alone — the
// seeds are a fixed sequence and the buckets are placed in a fixed order —
// so one set always writes the same bytes, whatever the order it was given in.
//
// # Keys and values
//
// Integer keys and values are any integer type, held at their own width; an
// image records the widths and the signs, and loading one as other types is
// refused. Floats are not keys: NaN equals nothing, and a frozen table would
// have to choose between refusing it and holding a key no Get finds.
package frozen
