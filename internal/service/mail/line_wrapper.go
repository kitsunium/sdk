// Package mail — the fixed-width line breaker every base64 body streams
// through.
package mail

import "io"

// lineWrapper breaks a stream into fixed-width CRLF-terminated lines.
//
// It exists so a 1 MiB attachment never becomes one 1.4-million-octet line: RFC
// 2045 §6.8 caps a base64 line at 76 characters, RFC 5322 §2.1.1 caps ANY line
// at 998 octets, and more than one MTA truncates a longer one silently.
type lineWrapper struct {
	dst   io.Writer
	width int
	used  int
}

// Write emits p, inserting a CRLF every [lineWrapper.width] octets.
func (w *lineWrapper) Write(p []byte) (n int, err error) {
	//: consume the input in whatever the current line still has room for.
	for len(p) > 0 {
		room := w.width - w.used
		//: a full line ends before anything more is written.
		if room == 0 {
			//: terminate and start a new one.
			if _, crErr := io.WriteString(w.dst, crlf); crErr != nil {
				//: report how much of p made it out.
				return n, crErr
			}
			w.used = 0
			//: the whole width is available again.
			continue
		}
		//: never write more than fits.
		chunk := min(room, len(p))
		written, writeErr := w.dst.Write(p[:chunk])
		n += written
		w.used += written
		//: a short or failed write stops the stream.
		if writeErr != nil {
			//: propagated with the count.
			return n, writeErr
		}
		p = p[chunk:]
	}
	//: everything consumed.
	return n, nil
}

// finish terminates a partially filled line, so the body does not run into the
// boundary delimiter that follows it.
func (w *lineWrapper) finish() error {
	//: an empty current line needs no terminator of its own.
	if w.used == 0 {
		//: already at a line start.
		return nil
	}
	_, err := io.WriteString(w.dst, crlf)
	w.used = 0
	//: the buffer's own failure, if any.
	return err
}
