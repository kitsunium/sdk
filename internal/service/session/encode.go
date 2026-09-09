// Package session — the at-rest framing of a record.
package session

import (
	"encoding/binary"
	"maps"
	"slices"
	"time"
)

// recordVersion is the first byte of every encoded record. A future framing
// change bumps it and decode refuses the old one as RecordCorrupt rather than
// guessing which layout it is looking at.
const recordVersion byte = 0x01

// versionLen is the width of that version byte.
const versionLen int = 1

// lenPrefix is the width of the length prefix in front of every string.
const lenPrefix int = 4

// timeLen is the width of one instant, stored as a big-endian int64 of Unix
// nanoseconds.
const timeLen int = 8

// timeFields is how many instants a record carries: createdAt, lastSeen and
// absoluteExpiry. The idle deadline is deliberately NOT stored — it is derived
// from lastSeen and the store's current IdleTimeout, so changing the timeout in
// configuration reaches the sessions that already exist.
const timeFields int = 3

// stringFields is how many length-prefixed strings the header carries: the
// digest and the subject.
const stringFields int = 2

// pairStrings is how many length-prefixed strings one data entry costs.
const pairStrings int = 2

// maxDataEntries caps a session's key count. A session store is not a
// database: it is per-user state on the request path, and an unbounded payload
// there is a memory-exhaustion vector with a very cheap trigger. The cap is
// enforced on the way IN (Save refuses) and again on the way OUT, before the
// map is allocated — a bound is checked before the work it funds.
const maxDataEntries int = 256

// maxStringLen caps any single key or value at 4 KiB, on the same reasoning.
const maxStringLen int = 4096

// encodeRecord renders rec as a deterministic byte frame:
//
//	u8 version | str digest | str subject | i64 created | i64 lastSeen |
//	i64 absoluteExpiry | u32 count | (str key, str value)*
//
// Keys are emitted in sorted order so the same record always produces the same
// bytes — which is what makes a round-trip test a byte comparison rather than a
// field-by-field one.
//
// The framing is hand-written rather than dispatched through the codec domain
// on purpose. This is an AT-REST frame, not an interchange format: nothing
// outside this package ever reads it, it must be total over map[string]string
// with no reflection and no tag vocabulary, and routing it through the codec
// registry would drag a codec dependency into every binary that wants a
// session store.
func encodeRecord(rec record) []byte {
	buf := make([]byte, 0, encodedSize(rec))
	buf = append(buf, recordVersion)
	buf = appendString(buf, rec.digest)
	buf = appendString(buf, rec.subject)
	buf = appendTime(buf, rec.createdAt)
	buf = appendTime(buf, rec.lastSeen)
	buf = appendTime(buf, rec.absoluteExpiry)
	//: the caller-facing Save has already refused anything above the cap, so
	//: the conversion cannot overflow.
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(rec.data)))
	//: sorted keys make the output deterministic.
	for _, key := range slices.Sorted(maps.Keys(rec.data)) {
		buf = appendString(buf, key)
		buf = appendString(buf, rec.data[key])
	}
	//: the complete frame.
	return buf
}

// encodedSize reports the frame length so encodeRecord allocates once. It is
// exact, not an estimate: the frame has no variable-width parts.
func encodedSize(rec record) int {
	size := versionLen + timeFields*timeLen + lenPrefix
	size += stringFields*lenPrefix + len(rec.digest) + len(rec.subject)
	//: each entry costs two length prefixes plus its bytes.
	for key, value := range rec.data {
		size += pairStrings*lenPrefix + len(key) + len(value)
	}
	//: the exact frame length.
	return size
}

// appendString writes a 4-byte big-endian length followed by the bytes.
func appendString(buf []byte, value string) []byte {
	//: the caller has already bounded the length.
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(value)))
	//: raw bytes, no escaping — the length prefix makes the frame unambiguous.
	return append(buf, value...)
}

// appendTime writes an instant as a big-endian int64 of Unix nanoseconds.
func appendTime(buf []byte, value time.Time) []byte {
	//: UnixNano is monotonic-free and location-free, so a record written under
	//: one TZ reads identically under another.
	return binary.BigEndian.AppendUint64(buf, uint64(value.UnixNano())) //nolint:gosec // two's-complement round trip, reversed in takeTime
}

// decodeRecord parses a frame produced by encodeRecord. Every failure — a bad
// version, a truncated frame, a length prefix pointing past the end, a count
// above the cap — is the same RecordCorrupt verdict.
func decodeRecord(raw []byte) (rec record, err error) {
	//: the version byte gates everything after it.
	if len(raw) < versionLen || raw[0] != recordVersion {
		//: an unknown layout is never guessed at.
		return record{}, wrapAs(RecordCorrupt, nil)
	}
	head, off, ok := decodeHeader(raw, versionLen)
	//: a truncated header cannot be repaired.
	if !ok {
		//: same single verdict.
		return record{}, wrapAs(RecordCorrupt, nil)
	}
	payload, ok := decodeData(raw, off)
	//: a truncated or over-large payload.
	if !ok {
		//: same single verdict.
		return record{}, wrapAs(RecordCorrupt, nil)
	}
	head.data = payload
	//: a complete record.
	return head, nil
}

// decodeHeader reads everything up to the entry count.
func decodeHeader(raw []byte, off int) (rec record, next int, ok bool) {
	digest, off, ok := takeString(raw, off)
	//: the lookup key is the first field and the first thing that can be short.
	if !ok {
		//: nothing usable; the caller reports one verdict for all of these.
		return record{}, 0, false
	}
	subject, off, ok := takeString(raw, off)
	//: the principal.
	if !ok {
		//: short frame.
		return record{}, 0, false
	}
	created, off, ok := takeTime(raw, off)
	//: the absolute-deadline anchor.
	if !ok {
		//: short frame.
		return record{}, 0, false
	}
	seen, off, ok := takeTime(raw, off)
	//: the idle-deadline anchor.
	if !ok {
		//: short frame.
		return record{}, 0, false
	}
	expiry, off, ok := takeTime(raw, off)
	//: the ceiling.
	if !ok {
		//: short frame.
		return record{}, 0, false
	}
	//: data is filled by decodeData.
	return record{
		digest: digest, subject: subject,
		createdAt: created, lastSeen: seen, absoluteExpiry: expiry,
	}, off, true
}

// decodeData reads the entry count and the key/value pairs behind it.
func decodeData(raw []byte, off int) (payload map[string]string, ok bool) {
	//: the count itself must fit.
	if off+lenPrefix > len(raw) {
		//: short frame.
		return nil, false
	}
	count := int(binary.BigEndian.Uint32(raw[off : off+lenPrefix]))
	off += lenPrefix
	//: the bound is checked BEFORE the allocation it would fund — a count
	//: field claiming four billion entries must not reserve four billion slots.
	if count > maxDataEntries {
		//: refused without allocating.
		return nil, false
	}
	//: an empty payload stays nil, which ranges as empty.
	if count == 0 {
		//: nothing to read.
		return nil, true
	}
	payload = make(map[string]string, count)
	//: exactly count pairs, or the frame is corrupt — the loop cannot read
	//: past the end because takeString bounds every read.
	for range count {
		key, value, next, pairOK := takePair(raw, off)
		//: a truncated pair invalidates the whole frame.
		if !pairOK {
			//: refused.
			return nil, false
		}
		payload[key] = value
		off = next
	}
	//: trailing bytes are a corrupt frame, not padding.
	return payload, off == len(raw)
}

// takePair reads one key/value pair.
func takePair(raw []byte, off int) (key, value string, next int, ok bool) {
	key, off, ok = takeString(raw, off)
	//: short key.
	if !ok {
		//: refused.
		return "", "", 0, false
	}
	value, off, ok = takeString(raw, off)
	//: short value.
	if !ok {
		//: refused.
		return "", "", 0, false
	}
	//: a complete pair.
	return key, value, off, true
}

// takeString reads one length-prefixed string, bounded by maxStringLen.
func takeString(raw []byte, off int) (value string, next int, ok bool) {
	//: the prefix must be present before it can be trusted.
	if off+lenPrefix > len(raw) {
		//: refused.
		return "", 0, false
	}
	size := int(binary.BigEndian.Uint32(raw[off : off+lenPrefix]))
	off += lenPrefix
	//: bound first, then read — the length field is attacker-shaped input on
	//: any store whose key ever leaks, and this check costs nothing.
	if size > maxStringLen || off+size > len(raw) {
		//: refused without slicing.
		return "", 0, false
	}
	//: the conversion copies, so the frame can be reused or zeroed after.
	return string(raw[off : off+size]), off + size, true
}

// takeTime reads one int64 of Unix nanoseconds.
func takeTime(raw []byte, off int) (value time.Time, next int, ok bool) {
	//: eight bytes or nothing.
	if off+timeLen > len(raw) {
		//: refused.
		return time.Time{}, 0, false
	}
	nanos := int64(binary.BigEndian.Uint64(raw[off : off+timeLen])) //nolint:gosec // reverses appendTime's two's-complement round trip
	//: UTC so a decoded record never carries the reader's local zone.
	return time.Unix(0, nanos).UTC(), off + timeLen, true
}

// boundPayload refuses a session payload above the caps, before it is written.
func boundPayload(data map[string]string) error {
	//: refuse at the door, so no store ever holds a record it cannot re-read.
	if len(data) > maxDataEntries {
		//: the cap is a documented number, not an implementation accident.
		return wrapAs(PayloadTooLarge, nil)
	}
	//: and every entry is checked, not just the count: one huge value is the
	//: same memory-exhaustion vector as many small ones.
	for key, value := range data {
		//: one oversized entry is enough to refuse the whole write.
		if len(key) > maxStringLen || len(value) > maxStringLen {
			//: the key is NOT named in the error: it is caller data, and a
			//: Public string is read by third parties.
			return wrapAs(PayloadTooLarge, nil)
		}
	}
	//: within bounds.
	return nil
}
