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
