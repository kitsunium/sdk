package msgpack

import (
	"encoding/binary"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Timestamp layout constants.
const (
	// ts32Len is the payload length of timestamp 32.
	ts32Len uint64 = 4
	// ts64Len is the payload length of timestamp 64.
	ts64Len uint64 = 8
	// ts96Len is the payload length of timestamp 96.
	ts96Len uint64 = 12
	// ts64SecBits is how many low bits of timestamp 64 hold the seconds.
	ts64SecBits uint = 34
	// ts64SecMask keeps the seconds of timestamp 64.
	ts64SecMask uint64 = 1<<ts64SecBits - 1
	// ts32Mask is the part of timestamp 64's word that must be zero for the
	// instant to fit timestamp 32.
	ts32Mask uint64 = 0xffffffff00000000
	// maxNanos is the largest nanosecond count the specification allows.
	maxNanos uint64 = 999_999_999
	// ts96NsecLen is the size of timestamp 96's nanosecond field.
	ts96NsecLen int = 4
)

// appendTimestamp appends t as the shortest timestamp form its instant fits.
func appendTimestamp(b []byte, t time.Time) []byte {
	//: reinterpreting a negative second count as unsigned pushes it past
	//: 2³⁴, so every instant before 1970 takes the 96-bit form.
	secs := uint64(t.Unix())
	nsec := uint64(t.Nanosecond())
	//: the 32- and 64-bit forms need the seconds in 34 bits.
	if secs>>ts64SecBits == 0 {
		//: nanoseconds above the seconds, in one word.
		word := nsec<<ts64SecBits | secs
		//: no nanoseconds and 32-bit seconds: timestamp 32.
		if word&ts32Mask == 0 {
			//: fixext 4, type −1, then the seconds.
			return binary.BigEndian.AppendUint32(append(b, codeFixext4, extTimestampByte), uint32(word))
		}
		//: fixext 8, type −1, then the word.
		return binary.BigEndian.AppendUint64(append(b, codeFixext8, extTimestampByte), word)
	}
	//: ext 8 of length 12, type −1, nanoseconds then signed seconds.
	b = append(b, codeExt8, byte(ts96Len), extTimestampByte)
	//: the nanosecond field.
	b = binary.BigEndian.AppendUint32(b, uint32(nsec))
	//: the second field, two's complement.
	return binary.BigEndian.AppendUint64(b, secs)
}

// decodeTimestamp reads a timestamp extension's payload into a UTC time.
func decodeTimestamp(p []byte) (time.Time, error) {
	//: the payload length names the form.
	switch uint64(len(p)) {
	//: timestamp 32: unsigned seconds.
	case ts32Len:
		return time.Unix(int64(binary.BigEndian.Uint32(p)), 0).UTC(), nil
	//: timestamp 64: nanoseconds above 34 bits of seconds.
	case ts64Len:
		word := binary.BigEndian.Uint64(p)
		return timeFromParts(int64(word&ts64SecMask), word>>ts64SecBits)
	//: timestamp 96: nanoseconds, then signed seconds.
	case ts96Len:
		nsec := uint64(binary.BigEndian.Uint32(p))
		return timeFromParts(int64(binary.BigEndian.Uint64(p[ts96NsecLen:])), nsec)
	//: no other length is a timestamp.
	default:
		return time.Time{}, unmarshalFault("timestamp extension has an invalid length", errs.Int(fieldLen, len(p)))
	}
}

// timeFromParts builds the UTC time of sec and nsec, refusing a nanosecond
// count the specification forbids instead of letting time.Unix carry it into
// the seconds.
func timeFromParts(sec int64, nsec uint64) (time.Time, error) {
	//: spec.md: "nanoseconds must not be larger than 999999999".
	if nsec > maxNanos {
		//: a malformed timestamp, not a later instant.
		return time.Time{}, unmarshalFault("timestamp nanoseconds exceed 999999999")
	}
	//: UTC, so the result does not depend on the machine's zone.
	return time.Unix(sec, int64(nsec)).UTC(), nil
}
