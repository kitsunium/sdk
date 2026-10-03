// Package yaml — the streaming decoder: one document per Decode.
package yaml

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// yamlDecoder reads a stream of documents, each separated by a "---" line or
// closed by a "..." line, and decodes them one per Decode. A document is
// read whole — bounded at maxYAMLBytes — and then parsed exactly as Unmarshal
// parses one, so every refusal of the subset applies to each.
type yamlDecoder struct {
	// r reads the stream.
	r *bufio.Reader
	// doc holds the document being read; reused across Decode calls.
	doc []byte
	// next holds a "---" line read past the end of the previous document,
	// which opens the next one.
	next []byte
	// line counts the stream's lines consumed before the next document.
	line int
	// done latches the end of the stream or a failure.
	done bool
}

// documentState is what readDocument has seen of the document it reads.
type documentState struct {
	// explicit says a "---" opened it.
	explicit bool
	// content says a line held content.
	content bool
}

// newStreamDecoder returns a decoder reading documents from r.
func newStreamDecoder(r io.Reader) *yamlDecoder {
	//: buffered, so a document is read a line at a time.
	return &yamlDecoder{r: bufio.NewReader(r)}
}

// Decode reads the next document into v. It returns io.EOF once the stream
// holds no further document.
func (d *yamlDecoder) Decode(v any) error {
	//: the stream ended or failed.
	if d.done {
		//: nothing more.
		return io.EOF
	}
	first := d.line + 1
	found, err := d.readDocument()
	//: the stream could not be read, or a document is too large.
	if err != nil {
		d.done = true
		//: refused.
		return err
	}
	//: no document left.
	if !found {
		d.done = true
		//: the end.
		return io.EOF
	}
	//: the document, located in the stream.
	if err := decodeDocument(d.doc, v, first); err != nil {
		d.done = true
		//: refused.
		return err
	}
	//: decoded.
	return nil
}

// More reports whether Decode may still return a document: false once the
// stream has ended or a Decode has failed.
func (d *yamlDecoder) More() bool {
	//: the latch.
	return !d.done
}

// readDocument reads the next document's lines into d.doc and reports whether
// there was one: a document is closed by a "..." line, by the "---" line that
// opens the next, or by the end of the stream, and is one when it holds a
// "---" or content.
func (d *yamlDecoder) readDocument() (bool, error) {
	d.doc = append(d.doc[:0], d.next...)
	state := documentState{explicit: len(d.next) > 0}
	//: the "---" carried over from the previous document is this one's first line.
	if state.explicit {
		d.line++
	}
	d.next = d.next[:0]
	//: one line per iteration.
	for {
		start, last, err := d.appendLine()
		//: the stream failed, or the document is too large.
		if err != nil {
			//: refused.
			return false, err
		}
		//: the end of the stream.
		if len(d.doc) == start {
			break
		}
		ended, terr := d.takeLine(&state, start)
		//: the document ends at this line, or is refused.
		if ended || terr != nil {
			//: whole, unless refused.
			return terr == nil, terr
		}
		//: the last line had no line break.
		if last {
			break
		}
	}
	//: a document, if the stream held one.
	return state.found(), nil
}

// appendLine appends the stream's next line to d.doc and returns where it
// starts and whether the stream ended with it; a read failure other than the
// end is returned as such.
func (d *yamlDecoder) appendLine() (start int, last bool, err error) {
	start = len(d.doc)
	var readErr error
	d.doc, readErr = d.readLine(d.doc)
	//: a failure other than the end.
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		//: refused.
		return start, true, readErr
	}
	//: the line, and whether it was the last.
	return start, readErr != nil, nil
}

// found reports whether the document is one: a "---" or content.
func (s *documentState) found() bool {
	//: either.
	return s.explicit || s.content
}

// takeLine classifies the line d.doc[start:] for the document being read and
// reports whether the document ends with it: a "---" after a document opens
// the next one and is carried over, a "..." closes this one.
func (d *yamlDecoder) takeLine(state *documentState, start int) (bool, error) {
	line := d.doc[start:]
	switch {
	//: a "---" after a document opens the next one.
	case isMarkerLine(line, "---") && state.found():
		d.next = append(d.next, line...)
		d.doc = d.doc[:start]
		//: this document is whole.
		return true, nil
	//: the "---" opening this one.
	case isMarkerLine(line, "---"):
		state.explicit = true
	//: "..." closes this document, which must hold something.
	case isMarkerLine(line, "..."):
		d.line++
		//: a "..." closing no document.
		if !state.found() {
			//: refused, as the parser refuses it.
			return true, syntaxError(d.line, 1, "a document end marker closes no document")
		}
		//: whole.
		return true, nil
	//: a line with content.
	case hasContent(line):
		state.content = true
	}
	d.line++
	//: the document goes on.
	return false, nil
}

// readLine appends the next line of the stream, its line break included, to
// dst, refusing a document that grows past maxYAMLBytes.
func (d *yamlDecoder) readLine(dst []byte) ([]byte, error) {
	//: a long line arrives in several pieces.
	for {
		piece, err := d.r.ReadSlice('\n')
		dst = append(dst, piece...)
		//: the bound, checked as the document grows.
		if len(dst) > maxYAMLBytes {
			//: refused, with the bound.
			return dst, errs.Wrap(UnmarshalFailed, errs.WrapParams{},
				errs.Int("cap", maxYAMLBytes), errs.String("detail", "a document of the stream is larger than the decoder accepts"))
		}
		//: more of the same line.
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		//: a read failure other than the end.
		if err != nil && !errors.Is(err, io.EOF) {
			//: the reader's own error stays the cause.
			return dst, errs.Wrap(err, errs.WrapParams{
				Code:    CodeYAMLUnmarshalFailed,
				Reason:  "UNMARSHAL_FAILED",
				Public:  "YAML decoding failed",
				Private: "service/data/codec/yaml.Decoder: the reader failed",
			})
		}
		//: the line, and io.EOF when it was the last.
		return dst, err
	}
}

// isMarkerLine reports whether line is the document marker marker at column
// 0, followed by a blank or the line's end.
func isMarkerLine(line []byte, marker string) bool {
	//: the marker.
	if !bytes.HasPrefix(line, []byte(marker)) {
		//: no.
		return false
	}
	rest := line[len(marker):]
	//: alone, or followed by a separator.
	return len(rest) == 0 || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' || rest[0] == '\r'
}

// hasContent reports whether line holds more than blanks and a comment. A
// directive is not content: it belongs to the document it precedes, where the
// parser refuses it by name.
func hasContent(line []byte) bool {
	trimmed := bytes.TrimLeft(line, " \t")
	//: nothing, or a line break.
	if len(trimmed) == 0 || trimmed[0] == '\n' || trimmed[0] == '\r' {
		//: no.
		return false
	}
	//: a comment, or a directive at column 0.
	return trimmed[0] != '#' && line[0] != '%'
}
