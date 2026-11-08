package dbus

import (
	"io"
	"strings"

	coredbus "github.com/kitsunium/sdk/internal/core/proc/dbus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The authentication (the specification's "Authentication Protocol"): the
// credentials byte, then AUTH EXTERNAL with no identity — the bus asks for
// one (DATA), is answered with none (an empty DATA) and takes the user the
// kernel names as the socket's peer —, OK and the server's GUID, then BEGIN;
// the messages follow on the same stream. Naming the client's own uid, as
// AUTH EXTERNAL <hex uid> does, is REJECTED from a user namespace — a
// sandbox's (S2): the uid there is not the one the bus sees. sd-bus speaks
// the same.

const (
	// maxAuthLine bounds a line the bus sends during the authentication.
	maxAuthLine int = 512
)

// authenticate runs AUTH EXTERNAL over w with no identity of its own,
// reading the bus's answers from r: nil once the bus said OK and was told
// BEGIN.
func authenticate(w io.Writer, r io.ByteReader) error {
	//: the credentials byte and the mechanism, no identity.
	if _, err := io.WriteString(w, "\x00AUTH EXTERNAL\r\n"); err != nil {
		//: the bus hung up.
		return unreachable(err)
	}
	verb, rest, err := authAnswer(r)
	//: no answer.
	if err != nil {
		//: the bus hung up, or a line past its bound.
		return err
	}
	//: the bus asks who connects: nobody it could not check.
	if verb == "DATA" && rest == "" {
		verb, rest, err = noIdentity(w, r)
		//: no answer to the empty DATA.
		if err != nil {
			//: the bus hung up, or a line past its bound.
			return err
		}
	}
	//: the verdict.
	return authVerdict(w, verb, rest)
}

// noIdentity answers the bus's DATA with an empty one — the identity the
// kernel gives of the socket's peer — and returns the bus's next answer.
func noIdentity(w io.Writer, r io.ByteReader) (verb, rest string, err error) {
	//: the empty DATA.
	if _, err = io.WriteString(w, "DATA\r\n"); err != nil {
		//: the bus hung up.
		return "", "", unreachable(err)
	}
	//: OK or REJECTED, or what EXTERNAL never gets.
	return authAnswer(r)
}

// authVerdict acts on the bus's verdict, verb and the rest of its line: OK
// is answered BEGIN.
func authVerdict(w io.Writer, verb, rest string) error {
	//: by the answer.
	switch verb {
	//: accepted.
	case "OK":
		_, err := io.WriteString(w, "BEGIN\r\n")
		//: the messages follow, or the bus hung up.
		return unreachableOrNil(err)
	//: refused: the mechanisms it offers.
	case "REJECTED":
		//: AUTH_REFUSED.
		return errs.Wrap(coredbus.AuthRefused, errs.WrapParams{}, errs.String("mechanisms", rest))
	//: anything else EXTERNAL never gets — a second DATA among them.
	default:
		//: PROTOCOL_VIOLATION.
		return violation("authentication")
	}
}

// authAnswer reads one line of the bus as its verb and the rest.
func authAnswer(r io.ByteReader) (verb, rest string, err error) {
	line, err := authLine(r)
	//: no line.
	if err != nil {
		//: the bus hung up, or a line past its bound.
		return "", "", err
	}
	verb, rest, _ = strings.Cut(line, " ")
	//: the line, cut at its first space.
	return verb, rest, nil
}

// authLine reads one line of the bus, without its CRLF, within maxAuthLine.
func authLine(r io.ByteReader) (string, error) {
	var b strings.Builder
	//: byte after byte up to the line feed.
	for b.Len() <= maxAuthLine {
		c, err := r.ReadByte()
		//: the bus hung up.
		if err != nil {
			//: BUS_UNREACHABLE.
			return "", unreachable(err)
		}
		//: the line's end.
		if c == '\n' {
			//: without its CR.
			return strings.TrimSuffix(b.String(), "\r"), nil
		}
		b.WriteByte(c)
	}
	//: PROTOCOL_VIOLATION.
	return "", violation("authentication line")
}

// unreachable wraps a connection's failure as BUS_UNREACHABLE.
func unreachable(cause error) error {
	//: origin wins on a typed cause.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coredbus.CodeBusUnreachable,
		Reason:   "BUS_UNREACHABLE",
		Public:   "The message bus is unreachable",
		Private:  "service/proc/dbus: the socket could not be connected, or the bus hung up during the handshake; the cause is in the chain",
		ExitCode: exitUnavailable,
	})
}

// unreachableOrNil is nil for nil, BUS_UNREACHABLE otherwise.
func unreachableOrNil(err error) error {
	//: no failure.
	if err == nil {
		//: none.
		return nil
	}
	//: BUS_UNREACHABLE.
	return unreachable(err)
}
