// Package id — UUIDv7 (time-ordered) generator (RFC 9562 §5.7).
package id

import (
	"crypto/rand"

	coreid "github.com/kitsunium/sdk/internal/core/app/id"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// UUIDv7 is the registered time-ordered UUIDv7 generator (RFC 9562 §5.7): a
// 48-bit Unix-millisecond prefix makes successive ids k-sortable.
var UUIDv7 = coreid.Register(uuidv7Gen{})

// uuidv7Gen prefixes a millisecond timestamp then fills the remainder randomly.
type uuidv7Gen struct{}

// Scheme implements core/app/id.Generator.
func (uuidv7Gen) Scheme() coreid.Scheme {
	//: the registered scheme key.
	return "uuidv7"
}

// New writes the 48-bit ms timestamp, fills the rest with randomness, and
// stamps the v7 version + variant bits.
func (uuidv7Gen) New() (newID string, err error) {
	//: 16 raw bytes back the 128-bit identifier.
	var b [uuidRawLen]byte
	//: the leading 6 bytes carry the Unix-millisecond timestamp (big-endian).
	putUint48BE(b[:], clock.System.Now().UnixMilli())
	//: the remaining bytes (after the timestamp) are secure random;
	//: crypto/rand.Read cannot fail (see the package doc).
	_, _ = rand.Read(b[tsBytes:])
	//: stamp version 7 (time-ordered) + the RFC variant bits.
	setUUIDBits(b[:], uuidVersion7)
	//: render the canonical dashed-hex form.
	return formatUUID(b[:]), nil
}
