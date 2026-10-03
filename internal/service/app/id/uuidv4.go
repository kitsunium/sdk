// Package id — UUIDv4 (random) generator (RFC 9562 §5.4).
package id

import (
	"crypto/rand"

	coreid "github.com/kitsunium/sdk/internal/core/app/id"
)

// UUIDv4 is the registered random UUIDv4 generator (RFC 9562 §5.4).
var UUIDv4 = coreid.Register(uuidv4Gen{})

// uuidv4Gen produces 122 random bits with the version/variant bits stamped.
type uuidv4Gen struct{}

// Scheme implements core/app/id.Generator.
func (uuidv4Gen) Scheme() coreid.Scheme {
	//: the registered scheme key.
	return "uuidv4"
}

// New draws 16 secure random bytes and stamps the v4 version + variant bits.
func (uuidv4Gen) New() (newID string, err error) {
	//: 16 raw bytes back the 128-bit identifier.
	var b [uuidRawLen]byte
	//: fill the whole identifier with secure randomness; crypto/rand.Read
	//: cannot fail (see the package doc).
	_, _ = rand.Read(b[:])
	//: stamp version 4 (random) + the RFC variant bits.
	setUUIDBits(b[:], uuidVersion4)
	//: render the canonical dashed-hex form.
	return formatUUID(b[:]), nil
}
