// Package id — UUIDv4 (random) generator (RFC 9562 §5.4).
package id

import coreid "github.com/kitsunium/sdk/internal/core/id"

// UUIDv4 is the registered random UUIDv4 generator (RFC 9562 §5.4).
var UUIDv4 = coreid.Register(uuidv4Gen{})

// uuidv4Gen produces 122 random bits with the version/variant bits stamped.
type uuidv4Gen struct{}

// Scheme implements core/id.Generator.
func (uuidv4Gen) Scheme() coreid.Scheme {
	//: the registered scheme key.
	return "uuidv4"
}

// New draws 16 secure random bytes and stamps the v4 version + variant bits.
func (uuidv4Gen) New() (newID string, err error) {
	//: 16 raw bytes back the 128-bit identifier.
	var b [uuidRawLen]byte
	//: fill the whole identifier with secure randomness.
	if rerr := readRandom(b[:]); rerr != nil {
		//: propagate the wrapped entropy failure.
		return "", rerr
	}
	//: stamp version 4 (random) + the RFC variant bits.
	setUUIDBits(b[:], uuidVersion4)
	//: render the canonical dashed-hex form.
	return formatUUID(b[:]), nil
}
