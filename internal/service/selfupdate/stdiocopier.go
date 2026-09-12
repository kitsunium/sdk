// Package selfupdate replaces the running binary with a newer signed release.
package selfupdate

import "io"

// stdIOCopier implements Copier using standard io package.
type stdIOCopier struct{}

// Copy copies from src to dst.
func (stdIOCopier) Copy(dst io.Writer, src io.Reader) (written int64, copyErr error) {
	//: Stream binary content from HTTP response to temporary file.
	return io.Copy(dst, src)
}
