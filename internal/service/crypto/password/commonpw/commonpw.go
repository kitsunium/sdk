// Package commonpw holds the ten thousand most common passwords, and says
// whether a password is one of them (ADR 0143 §D10). Public facade:
// pkg/v1/crypto/password, IsCommon.
//
// # What the list is, and where it comes from
//
// The list is xato-net-10-million-passwords-10000.txt from SecLists
// (https://github.com/danielmiessler/SecLists, Passwords/Common-Credentials),
// embedded byte for byte as it stood at commit
// 2e3e92569043d24297ca6c35070078e5cf41651e, SHA-256
// c63d5e4ccc31344d662583cc39ca4bd5bd20517ff1d24501f0c4e0c22d9b722a: the
// 10 000 passwords that occur most often in the ten million credentials Mark
// Burnett released into the public domain in 2015, most frequent first.
//
// SecLists is distributed under the MIT licence, Copyright (c) 2018 Daniel
// Miessler; its text is LICENSE.SecLists, beside the list, and travels with it
// in the module. The SDK's own licence is MIT too.
//
// # Why this list
//
// NIST SP 800-63B-4 requires a verifier to compare a new password against a
// blocklist of commonly used ones, and OWASP ASVS 5.0 (6.2.4) asks for at least
// the top 3 000. Ten thousand covers that with room, at 76 KB in the binary.
// Its provenance is known end to end, which a list merged from breaches of
// unknown origin does not offer.
//
// # How a password is compared
//
// Case-insensitively: a password and every entry are compared lower-cased as
// strings.ToLower lower-cases them, so PASSWORD and Password are refused with
// password — a guessing attacker tries case variants first, and refusing them
// costs a person nothing. Nothing else is normalised: no trimming, no
// substitution of look-alike characters. The list is sorted once, at the first
// question, and each question is a binary search that lower-cases the password
// as it compares it: no copy of the password is made, as a string or
// otherwise, and it is neither kept nor logged.
//
// One line of the file is empty: the empty password is among the ten thousand
// most used. It is not an entry here — refusing it is a length rule's job, and
// IsCommon answers false for it — so the list holds 9 999 passwords, 9 916
// once the case variants of one another are one.
//
// A policy that requires fifteen characters, NIST's minimum for a password
// used alone, has little left to refuse here — two entries are that long — so
// the list matters most beside a shorter minimum, such as the eight NIST allows
// with a second factor.
package commonpw

import (
	"cmp"
	_ "embed"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

var (
	// listText is the list, one password per line, as SecLists publishes it.
	//
	//go:embed xato-net-10-million-passwords-10000.txt
	listText string

	// sorted is the list lower-cased, deduplicated and sorted, built at the
	// first question.
	sorted = sync.OnceValue(func() []string {
		entries := make([]string, 0, strings.Count(listText, "\n"))
		//: one entry a line, compared lower-cased, as passwords are.
		for line := range strings.Lines(listText) {
			//: the empty line is the empty password, which a length rule refuses.
			if entry := strings.TrimRight(line, "\r\n"); entry != "" {
				entries = append(entries, strings.ToLower(entry))
			}
		}
		slices.Sort(entries)
		//: a password and its case variants are one entry.
		return slices.Compact(entries)
	})
)

// IsCommon reports whether password, lower-cased, is one of the list's
// entries, lower-cased. An empty password is not one of them: refusing it is
// a length rule's job.
func IsCommon(password []byte) bool {
	//: nothing to compare.
	if len(password) == 0 {
		//: not on the list.
		return false
	}
	_, found := slices.BinarySearchFunc(sorted(), password, compareFolded)
	//: on the list, or not.
	return found
}

// compareFolded compares entry with password lower-cased, byte for byte, as
// strings.Compare(entry, strings.ToLower(string(password))) would, without
// building that string: each rune of password is lower-cased by
// unicode.ToLower and encoded as strings.ToLower encodes it — an invalid byte
// as U+FFFD — into a buffer on the stack.
func compareFolded(entry string, password []byte) int {
	var encoded [utf8.UTFMax]byte
	at := 0
	//: rune by rune.
	for len(password) > 0 {
		r, size := utf8.DecodeRune(password)
		password = password[size:]
		n := utf8.EncodeRune(encoded[:], unicode.ToLower(r))
		//: each byte of the lower-cased rune against the entry's.
		for _, b := range encoded[:n] {
			//: the entry ended first: it sorts before.
			if at == len(entry) {
				//: shorter.
				return -1
			}
			//: the first byte that differs decides.
			if c := cmp.Compare(entry[at], b); c != 0 {
				//: before or after.
				return c
			}
			at++
		}
	}
	//: the password ended: equal, or the entry is longer and sorts after.
	return cmp.Compare(len(entry), at)
}

// Len returns how many distinct passwords the list holds once lower-cased:
// its 9 999 passwords, less the case variants of one another — 9 916.
func Len() int {
	//: the deduplicated list.
	return len(sorted())
}
