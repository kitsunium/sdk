// Package semver — the parser: a version cut into what precedence reads, in
// one pass over its bytes and without a copy of any of them.
package semver

import "strings"

// The bytes the grammar gives a meaning to.
const (
	// prefix opens every version.
	prefix byte = 'v'
	// dot separates two numbers, and two identifiers.
	dot byte = '.'
	// hyphen opens the pre-release part, and is the one byte an identifier
	// may hold besides letters and digits.
	hyphen byte = '-'
	// plus opens the build metadata, and so ends the pre-release part.
	plus byte = '+'
)

// zero is the number a shorthand's omitted component stands for.
const zero string = "0"

// The two ranges an identifier byte is drawn from, and the bit between the two
// cases of an ASCII letter — what lets isDigit and isLetter decide with one
// comparison each.
const (
	// digitCount is how many ASCII digits there are, '0' to '9'.
	digitCount byte = 10
	// letterCount is how many ASCII letters one case has, 'a' to 'z'.
	letterCount byte = 26
	// lowerCaseBit turns an ASCII capital into its lower case, and leaves a
	// lower-case letter as it is.
	lowerCaseBit byte = 0x20
)

// components is a version cut into what precedence reads. Every part is a
// substring of the version it was cut from, so cutting allocates nothing; the
// build metadata is checked and dropped, because nothing here reads it.
type components struct {
	// major is the major number, as written.
	major string
	// minor is the minor number as written, or "0" for "vMAJOR".
	minor string
	// patch is the patch number as written, or "0" for a shorthand.
	patch string
	// prerelease is the pre-release part with its leading '-', or "".
	prerelease string
}

// parse cuts v into its components and reports whether v is a version. On
// failure the components are all empty — so a reading of an invalid version
// is the empty string, as the package documents.
func parse(v string) (components, bool) {
	var parts components
	//: SemVer has no "v"; Go requires one, and so does every reading here.
	if v == "" || v[0] != prefix || !parts.read(v[1:]) {
		//: not a version, and nothing of it is kept.
		return components{}, false
	}
	//: a version, cut.
	return parts, true
}

// read fills the components from rest — the version after its "v" — and
// reports whether rest is MAJOR, MAJOR.MINOR, or MAJOR.MINOR.PATCH followed by
// a well-formed pre-release and build metadata.
func (c *components) read(rest string) bool {
	var ok bool
	c.major, rest, ok = number(rest)
	//: MAJOR is the one number no version can omit, and a bare "vMAJOR" is
	//: the shorthand for vMAJOR.0.0.
	if !ok || rest == "" {
		c.minor, c.patch = zero, zero
		//: a shorthand carries nothing else; a failed number is no version.
		return ok
	}
	//: "vMAJOR-pre" and "vMAJOR+meta" are no versions: only a dot follows.
	if rest[0] != dot {
		//: a shorthand carries no suffix.
		return false
	}
	c.minor, rest, ok = number(rest[1:])
	//: "vMAJOR.MINOR" is the shorthand for vMAJOR.MINOR.0 — with no
	//: pre-release, which only a full version has.
	if !ok || rest == "" {
		c.patch = zero
		//: the shorthand, or a malformed minor number.
		return ok
	}
	//: the same rule after the minor number.
	if rest[0] != dot {
		//: a shorthand carries no suffix.
		return false
	}
	c.patch, rest, ok = number(rest[1:])
	//: a full version with nothing after PATCH: the common case, read here.
	if !ok || rest == "" {
		//: a release, or a malformed patch number.
		return ok
	}
	c.prerelease, ok = suffixes(rest)
	//: a full version with a pre-release, build metadata or both, or neither
	//: well formed.
	return ok
}

// number cuts a decimal number off the front of s: at least one digit, and no
// leading zero unless the number is 0 (SemVer §2).
func number(s string) (num, rest string, ok bool) {
	end := 0
	//: the longest run of digits.
	for end < len(s) && isDigit(s[end]) {
		end++
	}
	//: no digit at all, or a leading zero on a number that is not 0.
	if end == 0 || (end > 1 && s[0] == '0') {
		//: not a number SemVer accepts.
		return "", "", false
	}
	//: the digits, and the remainder for the caller to read on.
	return s[:end], s[end:], true
}

// suffixes reads what follows PATCH — an optional pre-release, then optional
// build metadata — and returns the pre-release with its '-', reporting whether
// both are well formed and nothing else follows them.
func suffixes(rest string) (prerelease string, ok bool) {
	end := 0
	//: a pre-release runs from its '-' to the first byte no identifier holds.
	if rest != "" && rest[0] == hyphen {
		var found identifierList
		end, found = identifiers(rest, 1)
		//: SemVer §9: no empty identifier, and no number written "01".
		if found.empty || found.padded {
			//: a malformed pre-release.
			return "", false
		}
	}
	//: nothing after the pre-release, or nothing at all: a full version.
	if end == len(rest) {
		//: well formed.
		return rest[:end], true
	}
	//: the only thing that may follow is build metadata, to the end (§10).
	if rest[end] != plus {
		//: trailing bytes that are no part of a version.
		return "", false
	}
	tail, found := identifiers(rest, end+1)
	//: build metadata may write a number with leading zeros: it is never
	//: compared. It must still be identifiers, and the last thing in v.
	if found.empty || tail != len(rest) {
		//: malformed metadata, or bytes after it.
		return "", false
	}
	//: the pre-release as written; the metadata is checked and dropped.
	return rest[:end], true
}

// identifierList is what a scan of dot-separated identifiers found wrong with
// them, if anything.
type identifierList struct {
	// empty reports that an identifier held no byte (SemVer §9, §10).
	empty bool
	// padded reports that an identifier made of digits only was written with
	// a leading zero — refused in a pre-release, allowed in build metadata.
	padded bool
}

// add records one finished identifier, numeric when it held digits only.
func (l *identifierList) add(id string, numeric bool) {
	l.empty = l.empty || id == ""
	l.padded = l.padded || paddedNumber(id, numeric)
}

// identifiers scans dot-separated identifiers in s from index from, stopping
// at the first byte no identifier holds — anything but an ASCII letter, digit,
// hyphen or dot — or at the end. It returns where it stopped and what it found
// wrong with the identifiers it read.
func identifiers(s string, from int) (int, identifierList) {
	var found identifierList
	start, numeric := from, true
	//: one pass, each byte classified once.
	for end := from; end < len(s); end++ {
		switch c := s[end]; {
		//: a digit leaves the identifier as numeric as it was.
		case isDigit(c):
		//: a letter or a hyphen makes it alphanumeric.
		case isLetter(c) || c == hyphen:
			numeric = false
		//: a dot closes the identifier begun at start.
		case c == dot:
			found.add(s[start:end], numeric)
			start, numeric = end+1, true
		//: any other byte ends the list, and closes its last identifier.
		default:
			found.add(s[start:end], numeric)
			return end, found
		}
	}
	found.add(s[start:], numeric)
	//: the list ran to the end of s.
	return len(s), found
}

// paddedNumber reports whether id, known to be all digits when numeric, is a
// number written with a leading zero — a second spelling of a number, which
// SemVer refuses in a pre-release (§9).
func paddedNumber(id string, numeric bool) bool {
	//: "0" is a number; "01" is "1" spelled twice.
	return numeric && len(id) > 1 && id[0] == '0'
}

// isDigit reports whether c is an ASCII decimal digit — the ten ASCII digits
// only, SemVer being ASCII. One unsigned comparison: a byte below '0' wraps
// around to 208 or more.
func isDigit(c byte) bool {
	//: '0' to '9', and nothing else lands below ten.
	return c-'0' < digitCount
}

// isLetter reports whether c is an ASCII letter of either case. Setting
// lowerCaseBit maps 'A'-'Z' onto 'a'-'z' and no other byte into that range, so
// one unsigned comparison decides it.
func isLetter(c byte) bool {
	//: 'a' to 'z' once folded, and nothing else lands below twenty-six.
	return (c|lowerCaseBit)-'a' < letterCount
}

// falseFirst orders two booleans, false before true. It decides the two
// orderings the grammar states as "one has it, the other does not": an
// invalid string below a version, and an identifier list that runs out below
// one that goes on.
func falseFirst(x, y bool) int {
	switch {
	//: equal, whichever value it is.
	case x == y:
		return 0
	//: only x is true.
	case x:
		return 1
	//: only y is true.
	default:
		return -1
	}
}

// compareNumbers orders two decimal numbers written without leading zeros: the
// longer is the larger, and two of one length compare digit by digit. No
// conversion takes place, so a number of any size orders exactly.
func compareNumbers(x, y string) int {
	//: without leading zeros, more digits is a larger number.
	if len(x) != len(y) {
		//: decided by the length.
		return falseFirst(len(x) > len(y), len(y) > len(x))
	}
	//: equal lengths: the first digit that differs decides.
	for i := range len(x) {
		//: ASCII digit order is numeric order.
		if x[i] != y[i] {
			//: decided by this digit.
			return falseFirst(x[i] > y[i], y[i] > x[i])
		}
	}
	//: every digit equal.
	return 0
}

// comparePrereleases orders two pre-release parts, each "" or "-" followed by
// identifiers, by SemVer §11.3 and §11.4.
func comparePrereleases(x, y string) int {
	switch {
	//: the same part, or both releases.
	case x == y:
		return 0
	//: a release outranks every one of its pre-releases (§11.3), and every
	//: pre-release ranks below its release.
	case x == "" || y == "":
		return falseFirst(x == "", y == "")
	//: two pre-releases: identifier by identifier, after the '-'.
	default:
		return compareIdentifierLists(x[1:], y[1:])
	}
}

// compareIdentifierLists orders two dot-separated identifier lists left to
// right until one identifier differs; when one list runs out first, it is the
// lower (SemVer §11.4.4).
func compareIdentifierLists(x, y string) int {
	//: one pair of identifiers per round.
	for {
		idX, restX := nextIdentifier(x)
		idY, restY := nextIdentifier(y)
		//: the first identifier that differs decides.
		if order := compareIdentifiers(idX, idY); order != 0 {
			//: decided by this pair.
			return order
		}
		//: equal so far, and at least one list is exhausted.
		if restX == "" || restY == "" {
			//: the list that goes on is the higher; two that end together tie.
			return falseFirst(restX != "", restY != "")
		}
		x, y = restX[1:], restY[1:]
	}
}

// nextIdentifier splits the first identifier off list; the rest keeps its
// leading dot, so it is "" exactly when the list is exhausted.
func nextIdentifier(list string) (id, rest string) {
	end := 0
	//: up to the next dot, or the end.
	for end < len(list) && list[end] != dot {
		end++
	}
	//: the identifier, and what follows it.
	return list[:end], list[end:]
}

// compareIdentifiers orders two pre-release identifiers by SemVer §11.4.1 to
// §11.4.3: numbers numerically, other identifiers in ASCII order, and a number
// below any identifier that is not one.
func compareIdentifiers(x, y string) int {
	numericX, numericY := isNumeric(x), isNumeric(y)
	switch {
	//: two numbers compare as numbers: 2 < 11.
	case numericX && numericY:
		return compareNumbers(x, y)
	//: exactly one is a number, and a number ranks below an alphanumeric
	//: identifier: the one that is NOT a number is the higher.
	case numericX || numericY:
		return falseFirst(!numericX, !numericY)
	//: two alphanumeric identifiers compare lexically, in ASCII order.
	default:
		return strings.Compare(x, y)
	}
}

// isNumeric reports whether id — a well-formed identifier — is made of digits
// only, which is what makes it a number to precedence.
func isNumeric(id string) bool {
	//: one byte that is not a digit makes it alphanumeric.
	for i := range len(id) {
		//: a letter or a hyphen.
		if !isDigit(id[i]) {
			//: alphanumeric.
			return false
		}
	}
	//: digits only; never empty, since the identifier is well formed.
	return true
}
