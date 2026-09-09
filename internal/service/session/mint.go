// Package session — minting a session identifier from a random source.
package session

import (
	"io"

	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// mintID reads exactly IDLen bytes from source and builds an identifier.
//
// io.ReadFull, not Read: a short read is a FAILURE, not a smaller identifier.
// Accepting one would silently reduce the entropy the whole domain rests on,
// and it would do so in exactly the situation — a degraded random source — when
// that reduction matters most. A partial read therefore mints nothing and
// returns EntropyFailed.
//
// source is a field on the store rather than a hard-coded crypto/rand.Reader so
// the collision guard in New and Regenerate is reachable from a test: a reader
// that always returns the same bytes is what a broken entropy source looks
// like, and the store's answer to it is asserted rather than assumed. The field
// is deliberately NOT on the exported Config — a knob that lets a caller swap
// the session identifier's random source is a footgun with no legitimate
// production use.
func mintID(source io.Reader) (id coresession.ID, err error) {
	//: a fixed array rather than a make: the length is a constant of the domain.
	var buf [coresession.IDLen]byte
	//: ReadFull turns a short read into an error instead of a weak identifier.
	if _, readErr := io.ReadFull(source, buf[:]); readErr != nil {
		//: the reader's own message is carried as a field, never as the origin.
		return coresession.ID{}, wrapAs(coresession.EntropyFailed, readErr)
	}
	//: NewID re-checks the length and copies the bytes.
	return coresession.NewID(buf[:])
}
