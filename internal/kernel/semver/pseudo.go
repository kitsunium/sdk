// Package semver — Go pseudo-versions: the three shapes the modules reference
// defines, recognised by where their stamp sits, and the revision and time
// that stamp carries.
package semver

import (
	"strings"
	"time"
)

// stampLayout is the commit time a pseudo-version carries: UTC, to the second,
// as fourteen digits — the layout the Go toolchain writes it in.
const stampLayout string = "20060102150405"

// stampLen is the length of that time: the fourteen digits before the hyphen
// of the stamp "yyyymmddhhmmss-abcdefabcdef".
const stampLen int = len(stampLayout)

// IsPseudoVersion reports whether v is a Go pseudo-version: a valid version
// whose pre-release ends in "yyyymmddhhmmss-REVISION", immediately preceded
// either by nothing, when the version is vX.0.0, or by an identifier "0" —
// the three shapes of the Go modules reference, with or without build
// metadata such as "+incompatible".
//
// It recognises the SHAPE, as the toolchain's own pattern does: a stamp whose
// fourteen digits are not a real instant still makes a pseudo-version, and
// [PseudoVersionTime] is what reports that the time does not read.
func IsPseudoVersion(v string) bool {
	_, ok := pseudoStamp(v)
	//: a pseudo-version is whatever carries a stamp in the right place.
	return ok
}

// PseudoVersionRev returns the revision a pseudo-version names — by the
// toolchain's convention a commit's first twelve hexadecimal digits — and
// reports whether v is a pseudo-version at all. The revision is returned as
// written: the grammar admits any ASCII letters and digits.
func PseudoVersionRev(v string) (string, bool) {
	stamp, ok := pseudoStamp(v)
	//: not a pseudo-version: there is no revision to read.
	if !ok {
		//: the empty string and an honest false.
		return "", false
	}
	//: everything after the hyphen that ends the time.
	return stamp[stampLen+1:], true
}

// PseudoVersionTime returns the commit time a pseudo-version carries, in UTC
// and to the second, and reports whether it could be read: false when v is
// not a pseudo-version, or when its fourteen digits are not a real instant —
// a thirteenth month, say — which [IsPseudoVersion] does not judge.
func PseudoVersionTime(v string) (time.Time, bool) {
	stamp, ok := pseudoStamp(v)
	//: not a pseudo-version: there is no time to read.
	if !ok {
		//: the zero time and an honest false.
		return time.Time{}, false
	}
	at, err := time.Parse(stampLayout, stamp[:stampLen])
	//: the shape is a pseudo-version's, the digits are no instant.
	if err != nil {
		//: the zero time; the reason is no use to a caller of a kernel reading.
		return time.Time{}, false
	}
	//: a layout without a zone reads as UTC, which is what the stamp is.
	return at, true
}

// pseudoStamp returns the last pre-release identifier of a pseudo-version —
// its stamp, "yyyymmddhhmmss-REVISION" — and reports whether v is one.
func pseudoStamp(v string) (string, bool) {
	parts, ok := parse(v)
	//: a pseudo-version is a valid version with a pre-release.
	if !ok || parts.prerelease == "" {
		//: a release, a shorthand, or not a version at all.
		return "", false
	}
	list := parts.prerelease[1:]
	lastDot := strings.LastIndexByte(list, dot)
	stamp := list[lastDot+1:]
	//: the stamp is always the LAST identifier, and what precedes it decides
	//: between the three shapes.
	if !isStamp(stamp) || !stampPlaced(&parts, list, lastDot) {
		//: no stamp, or a stamp where no pseudo-version puts one.
		return "", false
	}
	//: a pseudo-version, and its stamp.
	return stamp, true
}

// stampPlaced reports whether the stamp ending list — at lastDot+1, lastDot
// being -1 when it is the only identifier — sits where a pseudo-version puts
// it: alone in the pre-release of a vX.0.0, or right after an identifier "0".
func stampPlaced(parts *components, list string, lastDot int) bool {
	//: "vX.0.0-yyyymmddhhmmss-abcdefabcdef": no earlier tag to build on.
	if lastDot < 0 {
		//: the version below every tag of major X.
		return parts.minor == zero && parts.patch == zero
	}
	head := list[:lastDot]
	//: "vX.Y.Z-0.stamp" and "vX.Y.Z-pre.0.stamp": the identifier before the
	//: stamp is exactly 0, which orders the pseudo-version below any
	//: pre-release a later tag could carry.
	return head[strings.LastIndexByte(head, dot)+1:] == zero
}

// isStamp reports whether id is "yyyymmddhhmmss-REVISION": exactly fourteen
// digits, a hyphen, and one or more ASCII letters and digits — no hyphen in
// the revision, so a stamp holds exactly one.
func isStamp(id string) bool {
	//: the hyphen, after exactly fourteen characters, with a revision after it.
	if len(id) < stampLen+2 || id[stampLen] != hyphen {
		//: too short, or the time is not fourteen characters long.
		return false
	}
	//: the time: digits only.
	for i := range stampLen {
		//: one character that is not a digit is enough.
		if !isDigit(id[i]) {
			//: not a time.
			return false
		}
	}
	//: the revision: letters and digits only.
	for i := stampLen + 1; i < len(id); i++ {
		//: a hyphen or any other character ends the match.
		if !isDigit(id[i]) && !isLetter(id[i]) {
			//: not a revision.
			return false
		}
	}
	//: a stamp.
	return true
}
