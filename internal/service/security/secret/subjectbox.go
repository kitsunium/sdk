// Package secret — the subject box: what SubjectKeys.Seal returns, and the
// associated data it and a wrapped key are bound to.
package secret

import (
	"encoding/binary"

	coresecret "github.com/kitsunium/sdk/internal/core/security/secret"
)

// subjectBoxFormat is the first byte of every subject box. A future layout
// gets another byte, and this one keeps opening.
const subjectBoxFormat byte = 0x01

// keyIDLen is the width of the key identifier a subject box carries: 64 bits
// derived from the data key, enough that two keys of one subject — the one an
// erasure destroyed and the one made after — never share it by accident.
const keyIDLen int = 8

// subjectHeaderMin is the header of the shortest subject: the format byte,
// the subject's length byte, one byte of subject, and the key identifier.
const subjectHeaderMin int = 2 + 1 + keyIDLen

// bindPartPrefix is the width of the big-endian length that precedes each
// binding part, the keytree's injective encoding: ("ab", "c") and ("a", "bc")
// are two different bindings.
const bindPartPrefix int = 4

// The domain strings that open every piece of associated data this file
// builds. Each ends in NUL, which neither a subject nor a keyring name can
// contain, so no binding of one kind can be spelled as a binding of another.
const (
	// subjectBoxDomain opens a subject box's associated data.
	subjectBoxDomain string = "kitsunium/secret subject box v1\x00"
	// wrapDomain opens the associated data a data key is wrapped with.
	wrapDomain string = "kitsunium/secret subject key v1\x00"
)

// SubjectOf returns the subject a box sealed by [SubjectKeys.Seal] names,
// without opening it — what an erasure reads to find which keys the records it
// erases were sealed under. It answers [coresecret.SealInvalid] for anything that is not
// a subject box. The subject is not secret: it is a reference, and it travels
// in clear in every box.
func SubjectOf(box []byte) (subject string, err error) {
	parsed, ok := parseSubjectBox(box)
	//: not a box this build makes.
	if !ok {
		//: SealInvalid.
		return "", coresecret.SealInvalid
	}
	//: the reference the header carries.
	return parsed.subject, nil
}

// subjectBox is a subject box split at its header.
type subjectBox struct {
	// subject is the reference the box was sealed for.
	subject string
	// id is the identifier of the data key that sealed it.
	id [keyIDLen]byte
	// header is every byte before the AEAD box, bound as associated data.
	header []byte
	// sealed is the AEAD box.
	sealed []byte
}

// subjectHeader lays out a box's header:
//
//	0x01 | len(subject), one byte | subject | key identifier, 8 bytes
//
// The subject is length-prefixed and the identifier fixed-width, so a header
// reads one way only.
func subjectHeader(subject string, id [keyIDLen]byte) []byte {
	out := make([]byte, 0, subjectHeaderMin-1+len(subject))
	out = append(out, subjectBoxFormat, byte(len(subject)))
	out = append(out, subject...)
	//: the identifier last: fixed width, so nothing follows it ambiguously.
	return append(out, id[:]...)
}

// parseSubjectBox splits box, reporting false for anything this build did not
// make: an unknown format, a length that overruns, a subject outside the
// grammar, or no AEAD box after the header.
func parseSubjectBox(box []byte) (parsed subjectBox, ok bool) {
	//: too short for any header, or a format this build does not know.
	if len(box) < subjectHeaderMin || box[0] != subjectBoxFormat {
		//: not ours.
		return subjectBox{}, false
	}
	length := int(box[1])
	end := 2 + length + keyIDLen
	//: the header must end before the AEAD box starts, and the box be there.
	if length == 0 || length > coresecret.MaxSubjectLen || len(box) <= end {
		//: not ours.
		return subjectBox{}, false
	}
	subject := string(box[2 : 2+length])
	//: a subject the grammar refuses was never sealed by this engine.
	if coresecret.ValidateSubject(subject) != nil {
		//: not ours.
		return subjectBox{}, false
	}
	parsed = subjectBox{subject: subject, header: box[:end], sealed: box[end:]}
	copy(parsed.id[:], box[2+length:end])
	//: the box, split.
	return parsed, true
}

// subjectAAD is the associated data of a subject box: the domain string, the
// whole header — so neither the subject nor the key identifier can be edited —
// then each binding part, length-prefixed.
func subjectAAD(header []byte, bind []string) []byte {
	size := len(subjectBoxDomain) + len(header)
	//: the exact capacity, so the binding is one allocation.
	for _, part := range bind {
		size += bindPartPrefix + len(part)
	}
	out := make([]byte, 0, size)
	out = append(out, subjectBoxDomain...)
	out = append(out, header...)
	//: each part behind its length: the encoding reads back one way only.
	for _, part := range bind {
		out = binary.BigEndian.AppendUint32(out, uint32(len(part)))
		out = append(out, part...)
	}
	//: the associated data.
	return out
}

// wrapAAD is the associated data a subject's data key is wrapped with under
// the root keyring's wrap key — the keyring prefixes its own name and version
// to it — so a wrapped key filed under another subject does not unwrap.
func wrapAAD(subject string) []byte {
	out := make([]byte, 0, len(wrapDomain)+len(subject))
	out = append(out, wrapDomain...)
	//: the subject last: every byte before it is fixed.
	return append(out, subject...)
}
