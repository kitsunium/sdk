// Package file implements a Sink that appends formatted records to an
// on-disk file. Opens with O_APPEND|O_CREATE|O_WRONLY so concurrent
// goroutines (and other processes appending to the same file) emit atomic
// records as long as the payload stays under PIPE_BUF on POSIX systems.
//
// Rotation is intentionally out of scope for this commit — callers needing
// rolling files compose this sink with a future sink/rotate wrapper, or
// with a third-party rotator like lumberjack.v2 in their own application
// code.
package file
