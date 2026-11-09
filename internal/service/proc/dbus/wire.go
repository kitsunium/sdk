package dbus

import (
	"bytes"
	"encoding/binary"
	"io"
	"slices"
	"unicode/utf8"

	coredbus "github.com/kitsunium/sdk/internal/core/proc/dbus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The D-Bus wire format, as far as this client needs it: a method call with
// string and uint32 arguments written in little-endian, and any message of
// either byte order read — its header fields decoded, its body kept for the
// replies and signals the client reads.

const (
	// typeMethodCall is a method call.
	typeMethodCall byte = 1
	// typeMethodReturn is a method's reply.
	typeMethodReturn byte = 2
	// typeError is a method's error reply.
	typeError byte = 3
	// typeSignal is a signal.
	typeSignal byte = 4
	// flagNoReplyExpected asks the bus for no reply.
	flagNoReplyExpected byte = 0x1
	// protocolVersion is the major protocol version.
	protocolVersion byte = 1
	// littleEndian is the little-endian byte-order mark.
	littleEndian byte = 'l'
	// bigEndian is the big-endian byte-order mark.
	bigEndian byte = 'B'
	// fieldPath is the object path's header field.
	fieldPath byte = 1
	// fieldInterface is the interface's header field.
	fieldInterface byte = 2
	// fieldMember is the member's header field.
	fieldMember byte = 3
	// fieldErrorName is an error's name's header field.
	fieldErrorName byte = 4
	// fieldReplySerial is the header field of the serial a reply answers.
	fieldReplySerial byte = 5
	// fieldDestination is the destination's header field.
	fieldDestination byte = 6
	// fieldSender is the sender's header field, which the daemon stamps.
	fieldSender byte = 7
	// fieldSignature is the body's signature's header field.
	fieldSignature byte = 8
	// callBuffer is a method call's usual size, preallocated.
	callBuffer int = 256
	// sizeByte is a byte's size; each basic type is aligned on its size.
	sizeByte int = 1
	// sizeInt16 is a 16-bit integer's size.
	sizeInt16 int = 2
	// sizeInt32 is a 32-bit value's size: an integer, a boolean, a handle.
	sizeInt32 int = 4
	// sizeInt64 is a 64-bit value's size: an integer, a double.
	sizeInt64 int = 8
	// fixedHeader is the header before its field array: the byte order, the
	// type, the flags, the version, the body's length, the serial and the
	// array's length.
	fixedHeader int = 16
	// maxFieldBytes bounds a header's field array.
	maxFieldBytes uint32 = 1 << 16
	// maxBodyBytes is the largest body read; past it a body is read and
	// dropped, never allocated.
	maxBodyBytes uint32 = 1 << 20
	// maxMessageBytes is the specification's largest message.
	maxMessageBytes uint32 = 1 << 27
	// alignStruct is the alignment of a struct, a header field and a body.
	alignStruct int = 8
	// alignUint32 is the alignment of a uint32 and of a string's length.
	alignUint32 int = 4
)

// basicSizes are the fixed-size basic types a skipped header field may hold.
var basicSizes = map[byte]int{
	'y': sizeByte, 'n': sizeInt16, 'q': sizeInt16, 'b': sizeInt32, 'i': sizeInt32,
	'u': sizeInt32, 'h': sizeInt32, 'x': sizeInt64, 't': sizeInt64, 'd': sizeInt64,
}

// message is one message read: its header and its body, the body nil when it
// was past maxBodyBytes and dropped; bodyLen and fieldLen are the lengths its
// fixed header announced.
type message struct {
	order                                      binary.ByteOrder
	path, iface, member, errName, dest, sender string
	sig                                        string
	body                                       []byte
	serial, replySerial, bodyLen, fieldLen     uint32
	typ, flags                                 byte
}

// discarder is what a message is read from: bytes, and a body dropped
// unread.
type discarder interface {
	io.Reader
	// Discard skips the next n bytes.
	Discard(n int) (discarded int, err error)
}

// encoder writes a message, little-endian, aligning each value on its
// offset from the message's start.
type encoder struct {
	b []byte
}

// pad aligns the next value on n bytes.
func (e *encoder) pad(n int) {
	//: zero bytes up to the boundary.
	for len(e.b)%n != 0 {
		e.b = append(e.b, 0)
	}
}

// u32 writes a uint32.
func (e *encoder) u32(v uint32) {
	e.pad(alignUint32)
	e.b = binary.LittleEndian.AppendUint32(e.b, v)
}

// str writes a string or an object path: its length, its bytes, a NUL.
func (e *encoder) str(s string) {
	e.u32(uint32(len(s)))
	e.b = append(e.b, s...)
	e.b = append(e.b, 0)
}

// sig writes a signature: its length on one byte, its bytes, a NUL.
func (e *encoder) sig(s string) {
	e.b = append(e.b, byte(len(s)))
	e.b = append(e.b, s...)
	e.b = append(e.b, 0)
}

// field writes one header field: its code and a variant holding a string
// ('s'), an object path ('o') or a signature ('g').
func (e *encoder) field(code, typ byte, value string) {
	e.pad(alignStruct)
	e.b = append(e.b, code)
	e.sig(string(rune(typ)))
	//: a signature's own encoding, a string's for the others.
	if typ == 'g' {
		e.sig(value)
		return
	}
	e.str(value)
}

// callValue is one method call to the bus driver: its member, its body's
// signature and the body's arguments — strings, then a uint32 when sig ends
// in "u".
type callValue struct {
	member, sig string
	strs        []string
	u           uint32
	flags       byte
}

// encodeCall writes a method call to the bus driver.
func encodeCall(serial uint32, c *callValue) []byte {
	e := &encoder{b: make([]byte, 0, callBuffer)}
	e.b = append(e.b, littleEndian, typeMethodCall, c.flags, protocolVersion, 0, 0, 0, 0)
	e.b = binary.LittleEndian.AppendUint32(e.b, serial)
	e.b = append(e.b, 0, 0, 0, 0)
	e.field(fieldPath, 'o', busPath)
	e.field(fieldInterface, 's', busInterface)
	e.field(fieldMember, 's', c.member)
	e.field(fieldDestination, 's', busDriver)
	//: a body says its signature.
	if c.sig != "" {
		e.field(fieldSignature, 'g', c.sig)
	}
	binary.LittleEndian.PutUint32(e.b[12:], uint32(len(e.b)-fixedHeader))
	e.pad(alignStruct)
	start := len(e.b)
	//: each string argument.
	for _, s := range c.strs {
		e.str(s)
	}
	//: the uint32 argument.
	if len(c.sig) > len(c.strs) {
		e.u32(c.u)
	}
	binary.LittleEndian.PutUint32(e.b[4:], uint32(len(e.b)-start))
	//: the message.
	return e.b
}

// readMessage reads one message: the header bounded before it is read, the
// body read whole or, past maxBodyBytes, read and dropped.
func readMessage(r discarder) (*message, error) {
	var head [fixedHeader]byte
	//: the fixed part, or the end of the connection.
	if _, err := io.ReadFull(r, head[:]); err != nil {
		//: the connection ended.
		return nil, err
	}
	m, err := parseFixed(head[:])
	//: a header the specification does not allow.
	if err != nil {
		//: PROTOCOL_VIOLATION.
		return nil, err
	}
	fields := make([]byte, int(m.fieldLen)+padding(fixedHeader+int(m.fieldLen), alignStruct))
	//: the field array and its padding.
	if _, err := io.ReadFull(r, fields); err != nil {
		//: the connection ended.
		return nil, err
	}
	//: each field.
	if err := m.parseFields(fields[:m.fieldLen]); err != nil {
		//: PROTOCOL_VIOLATION.
		return nil, err
	}
	//: a body past the bound: read and dropped.
	if m.bodyLen > maxBodyBytes {
		_, err := r.Discard(int(m.bodyLen))
		//: dropped, or the connection ended.
		return m, err
	}
	var body bytes.Buffer
	body.Grow(int(m.bodyLen))
	_, err = io.CopyN(&body, r, int64(m.bodyLen))
	m.body = body.Bytes()
	//: the message, or the connection ended.
	return m, err
}

// parseFixed reads the fixed part of a header: the byte order, the type, the
// flags, the version, the lengths and the serial.
func parseFixed(head []byte) (*message, error) {
	m := &message{typ: head[1], flags: head[2]}
	//: the byte order.
	switch head[0] {
	//: little-endian.
	case littleEndian:
		m.order = binary.LittleEndian
	//: big-endian.
	case bigEndian:
		m.order = binary.BigEndian
	//: neither.
	default:
		//: PROTOCOL_VIOLATION.
		return nil, violation("byte order")
	}
	m.bodyLen, m.fieldLen = m.order.Uint32(head[4:]), m.order.Uint32(head[12:])
	m.serial = m.order.Uint32(head[8:])
	//: a version, a serial and lengths the specification allows.
	if head[3] != protocolVersion || m.serial == 0 || m.fieldLen > maxFieldBytes ||
		uint64(m.bodyLen)+uint64(m.fieldLen)+uint64(fixedHeader) > uint64(maxMessageBytes) {
		//: PROTOCOL_VIOLATION.
		return nil, violation("header")
	}
	//: the fixed part.
	return m, nil
}

// padding is how many bytes align offset on n.
func padding(offset, n int) int {
	//: up to the boundary.
	return (n - offset%n) % n
}

// parseFields reads the header's field array: every field the client knows,
// the others skipped when their value is a basic type.
func (m *message) parseFields(b []byte) error {
	d := &decoder{b: b, base: fixedHeader, order: m.order}
	//: field after field.
	for d.off < len(b) {
		//: each field opens on 8 bytes.
		if !d.align(alignStruct) {
			//: PROTOCOL_VIOLATION.
			return violation("header field")
		}
		code, ok := d.byte()
		typ, tok := d.signature()
		//: a code and a single-type variant.
		if !ok || !tok || len(typ) != 1 {
			//: PROTOCOL_VIOLATION.
			return violation("header field")
		}
		//: the field's value.
		if err := m.field(d, code, typ[0]); err != nil {
			//: PROTOCOL_VIOLATION.
			return err
		}
	}
	//: every field.
	return nil
}

// field reads one header field's value into the message.
func (m *message) field(d *decoder, code, typ byte) error {
	strs := map[byte]*string{
		fieldPath: &m.path, fieldInterface: &m.iface, fieldMember: &m.member,
		fieldErrorName: &m.errName, fieldDestination: &m.dest, fieldSender: &m.sender,
	}
	//: by the field.
	switch {
	//: a string or an object path.
	case strs[code] != nil && (typ == 's' || typ == 'o'):
		v, ok := d.string()
		*strs[code] = v
		//: read.
		return okOr(ok, "header string")
	//: the body's signature.
	case code == fieldSignature && typ == 'g':
		v, ok := d.signature()
		m.sig = v
		//: read.
		return okOr(ok, "header signature")
	//: the serial a reply answers.
	case code == fieldReplySerial && typ == 'u':
		v, ok := d.u32()
		m.replySerial = v
		//: read.
		return okOr(ok, "header reply serial")
	//: a field the client does not read, or a known one of another type.
	default:
		//: skipped when it is a basic type.
		return okOr(d.skip(typ), "header field type")
	}
}

// okOr is nil when ok, PROTOCOL_VIOLATION naming rule otherwise.
func okOr(ok bool, rule string) error {
	//: read.
	if ok {
		//: none.
		return nil
	}
	//: PROTOCOL_VIOLATION.
	return violation(rule)
}

// decoder reads values aligned on their offset from the message's start
// (base is b's offset there).
type decoder struct {
	order binary.ByteOrder
	b     []byte
	base  int
	off   int
}

// align skips the padding up to n bytes, which must lie within b.
func (d *decoder) align(n int) bool {
	p := padding(d.base+d.off, n)
	//: past the end.
	if d.off+p > len(d.b) {
		//: short.
		return false
	}
	d.off += p
	//: aligned.
	return true
}

// byte reads one byte.
func (d *decoder) byte() (byte, bool) {
	//: past the end.
	if d.off >= len(d.b) {
		//: short.
		return 0, false
	}
	d.off++
	//: the byte.
	return d.b[d.off-1], true
}

// u32 reads a uint32.
func (d *decoder) u32() (uint32, bool) {
	//: aligned and whole.
	if !d.align(alignUint32) || d.off+alignUint32 > len(d.b) {
		//: short.
		return 0, false
	}
	d.off += alignUint32
	//: the value.
	return d.order.Uint32(d.b[d.off-alignUint32:]), true
}

// string reads a string or an object path: its length, valid UTF-8 without
// NUL, and its terminating NUL.
func (d *decoder) string() (string, bool) {
	n, ok := d.u32()
	//: no length, or bytes past the end.
	if !ok || uint64(d.off)+uint64(n)+1 > uint64(len(d.b)) {
		//: short.
		return "", false
	}
	//: read as text.
	return d.text(int(n))
}

// signature reads a signature: its length on one byte, its bytes, a NUL.
func (d *decoder) signature() (string, bool) {
	n, ok := d.byte()
	//: no length, or bytes past the end.
	if !ok || d.off+int(n)+1 > len(d.b) {
		//: short.
		return "", false
	}
	//: read as text.
	return d.text(int(n))
}

// text reads n bytes and the NUL that ends them.
func (d *decoder) text(n int) (string, bool) {
	s := d.b[d.off : d.off+n]
	d.off += n + 1
	//: valid UTF-8, no NUL within, a NUL after.
	return string(s), d.b[d.off-1] == 0 && utf8.Valid(s) && !slices.Contains(s, 0)
}

// skip skips one value of a basic type; a container is not skipped.
func (d *decoder) skip(typ byte) bool {
	n := basicSizes[typ]
	//: by the type.
	switch {
	//: a string or an object path.
	case typ == 's' || typ == 'o':
		_, ok := d.string()
		//: skipped.
		return ok
	//: a signature.
	case typ == 'g':
		_, ok := d.signature()
		//: skipped.
		return ok
	//: a fixed size, aligned on itself.
	case n > 0:
		//: aligned and whole.
		if !d.align(n) || d.off+n > len(d.b) {
			//: short.
			return false
		}
		d.off += n
		//: skipped.
		return true
	//: a container, or no type.
	default:
		//: not skipped.
		return false
	}
}

// strings reads a body of n strings, which its signature must say.
func (m *message) strings(n int) ([]string, bool) {
	//: the signature the reader expects.
	if m.body == nil || len(m.sig) != n || !allOf(m.sig, 's') {
		//: another body.
		return nil, false
	}
	d := &decoder{b: m.body, order: m.order}
	out := make([]string, 0, n)
	//: each string.
	for range n {
		s, ok := d.string()
		//: short or invalid.
		if !ok {
			//: refused.
			return nil, false
		}
		out = append(out, s)
	}
	//: the strings.
	return out, true
}

// uint32 reads a body of one uint32.
func (m *message) uint32() (uint32, bool) {
	//: the signature the reader expects.
	if m.body == nil || m.sig != "u" {
		//: another body.
		return 0, false
	}
	d := &decoder{b: m.body, order: m.order}
	//: the value.
	return d.u32()
}

// allOf reports whether every byte of s is c.
func allOf(s string, c byte) bool {
	//: each byte.
	for i := range len(s) {
		//: another.
		if s[i] != c {
			//: no.
			return false
		}
	}
	//: yes.
	return true
}

// violation is PROTOCOL_VIOLATION naming the rule broken.
func violation(rule string) error {
	//: the core sentinel with the rule.
	return errs.Wrap(coredbus.ProtocolViolation, errs.WrapParams{}, errs.String("rule", rule))
}
