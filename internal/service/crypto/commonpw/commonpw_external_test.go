// Package commonpw_test — the list loads as the package documents it, and
// answers for every one of its entries.
package commonpw_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/service/crypto/commonpw"
)

// listDigest is the SHA-256 of SecLists' xato-net-10-million-passwords-10000.txt
// at commit 2e3e92569043d24297ca6c35070078e5cf41651e, as the package doc
// records it.
const listDigest = "c63d5e4ccc31344d662583cc39ca4bd5bd20517ff1d24501f0c4e0c22d9b722a"

// TestTheListLoads pins the list: byte for byte the file the package doc
// names, ten thousand lines — one of them the empty password — and, once
// case variants are one, 9 916 distinct passwords, well past the three
// thousand ASVS 5.0 asks for.
func TestTheListLoads(t *testing.T) {
	t.Parallel()
	text := commonpw.ListForTest()
	sum := sha256.Sum256([]byte(text))
	if got := hex.EncodeToString(sum[:]); got != listDigest {
		t.Fatalf("the embedded list's SHA-256 is %s, want %s: it is not the file the package doc names", got, listDigest)
	}
	if lines := strings.Count(text, "\n"); lines != 10_000 {
		t.Fatalf("the list has %d lines, want 10000", lines)
	}
	if n := commonpw.Len(); n != 9_916 || n < 3_000 {
		t.Fatalf("Len() = %d, want 9916", n)
	}
}

// TestEveryEntryIsCommon pins the lookup against the list itself: each of
// its entries is found, as written and upper-cased — except the empty line,
// the empty password, which is a length rule's to refuse.
func TestEveryEntryIsCommon(t *testing.T) {
	t.Parallel()
	for entry := range strings.Lines(commonpw.ListForTest()) {
		entry = strings.TrimRight(entry, "\r\n")
		if entry == "" {
			if commonpw.IsCommon(nil) {
				t.Fatal("the empty password is common: a length rule's job is taken")
			}
			continue
		}
		if !commonpw.IsCommon([]byte(entry)) || !commonpw.IsCommon([]byte(strings.ToUpper(entry))) {
			t.Fatalf("an entry of the list, %q, is not common", entry)
		}
	}
}

// TestIsCommon pins what is refused and what is not: the list's first and
// last entries, case variants, an entry the list spells in mixed case; and
// neither a passphrase, a password close to an entry, a Unicode one, nor the
// empty one — a length rule's job.
func TestIsCommon(t *testing.T) {
	t.Parallel()
	for password, want := range map[string]bool{
		"123456":                       true,
		"password":                     true,
		"PASSWORD":                     true,
		"PassWord":                     true,
		"blitz":                        true,
		"usuckballz1":                  true,
		"Usuckballz1":                  true,
		"correct horse battery staple": false,
		"password ":                    false,
		" password":                    false,
		"passw0rd!":                    false,
		"pässwörd":                     false,
		"":                             false,
	} {
		if got := commonpw.IsCommon([]byte(password)); got != want {
			t.Errorf("IsCommon(%q) = %v, want %v", password, got, want)
		}
	}
}

// TestIsCommonUnderContention pins the list built once, whoever asks first,
// from many goroutines at once under the race detector.
func TestIsCommonUnderContention(t *testing.T) {
	t.Parallel()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if !commonpw.IsCommon([]byte("qwerty")) || commonpw.IsCommon([]byte("not a common one at all")) {
				t.Error("a concurrent question got the wrong answer")
			}
		})
	}
	wg.Wait()
}
