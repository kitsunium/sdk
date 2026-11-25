package frozen

import "unsafe"

// stringOf returns b's bytes as a string without copying them: the one use of
// unsafe in the package. A table's blob is a slice of its image, and a string
// over it is what lets Get compare a key and return a Dict value with no
// allocation. It is sound because nothing writes to an image once a table views
// it — a built table owns its image and never writes it again, and Load's
// contract makes the caller keep its bytes unchanged for as long as the table
// lives.
func stringOf(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}
