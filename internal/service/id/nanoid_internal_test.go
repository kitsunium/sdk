// Package id — white-box tests for the NanoID generator.
package id

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_nanoIDGen_Scheme pins the registry key. It is what a caller passes to
// id.New, so a drift here unregisters the generator from every consumer that
// asks for it by name.
func Test_nanoIDGen_Scheme(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"the canonical key", "nanoid"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := string(nanoIDGen{}.Scheme()); got != c.want {
			t.Errorf("Scheme() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_nanoIDGen_New pins the length, the alphabet and the uniqueness. A NanoID
// carries no timestamp, so length and alphabet are the entire contract: the
// only thing a consumer can assert about one is that it is N URL-safe
// characters that nobody else got.
func Test_nanoIDGen_New(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		gen  nanoIDGen
		//: the length every minted identifier must have.
		wantLen int
		count   int
	}
	tests := []tc{
		{"the registered default size", nanoIDGen{size: nanoIDDefaultSize}, nanoIDDefaultSize, 256},
		{"a short explicit size", nanoIDGen{size: 1}, 1, 64},
		{"a long explicit size", nanoIDGen{size: 128}, 128, 32},
		//: the zero value resolves to the default rather than minting "".
		{"the zero value", nanoIDGen{}, nanoIDDefaultSize, 32},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(map[string]struct{}, c.count)
		for range c.count {
			got, err := c.gen.New()
			if err != nil {
				t.Fatalf("New = %v, want nil", err)
			}
			if len(got) != c.wantLen {
				t.Fatalf("New() = %q (%d chars), want %d", got, len(got), c.wantLen)
			}
			for _, r := range got {
				if !strings.ContainsRune(nanoIDAlphabet, r) {
					t.Errorf("New() = %q contains %q, outside the URL-safe alphabet", got, r)
				}
			}
			//: a one-character identifier repeats by design; only assert
			//: uniqueness where the space makes a collision a real defect.
			if c.wantLen >= nanoIDDefaultSize {
				if _, dup := seen[got]; dup {
					t.Errorf("New() repeated %q", got)
				}
				seen[got] = struct{}{}
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_nanoIDGen_length pins the zero-value rule. It is the difference between
// a struct whose zero value is the published default and one that mints empty
// strings forever — ADR 0031's "never inert" applied to a value type.
func Test_nanoIDGen_length(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		size int
		want int
	}
	tests := []tc{
		{"an unset size falls back to the default", 0, nanoIDDefaultSize},
		{"a negative size cannot survive either", -7, nanoIDDefaultSize},
		{"an explicit size is honoured", 12, 12},
		{"the default stated explicitly", nanoIDDefaultSize, nanoIDDefaultSize},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := (nanoIDGen{size: c.size}).length(); got != c.want {
			t.Errorf("nanoIDGen{size: %d}.length() = %d, want %d", c.size, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_pow2MaskFor pins the mask width. It is the number the rejection sampler
// is built on: a mask one bit too narrow can never reach the top of the
// alphabet, and one bit too wide rejects half the draws for nothing.
func Test_pow2MaskFor(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		n    int
		want int
	}
	tests := []tc{
		{"a single symbol needs no bits", 1, 0},
		{"two symbols need one bit", 2, 1},
		{"three symbols round up to two bits", 3, 3},
		{"a base32 alphabet is exact", 32, 31},
		{"base62 rounds up to six bits", 62, 63},
		{"the shipped 64-symbol alphabet is exact", 64, 63},
		{"65 symbols spill into a seventh bit", 65, 127},
		{"a full byte", 256, 255},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := pow2MaskFor(c.n)
		if got != c.want {
			t.Errorf("pow2MaskFor(%d) = %d, want %d", c.n, got, c.want)
		}
		//: the mask must span the alphabet — otherwise the last symbols are
		//: unreachable and the identifier quietly loses entropy.
		if got < c.n-1 {
			t.Errorf("pow2MaskFor(%d) = %d cannot address the last symbol", c.n, got)
		}
		//: and it must be all-ones, or masking would punch holes in the range.
		if got&(got+1) != 0 {
			t.Errorf("pow2MaskFor(%d) = %d is not of the form 2^k-1", c.n, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_randomFromAlphabet is the modulo-bias guard, and the reason the sampler
// rejects instead of taking a remainder.
//
// `alphabet[b%len(alphabet)]` looks correct and is not: with 62 symbols the 256
// byte values split 4-or-5 ways, so the first eight symbols come up ~21 % more
// often than the rest — in every identifier, forever. Nothing about the output
// reveals it. The tolerance below (15 %) sits comfortably under that 21 % and
// roughly seven standard deviations away from the uniform expectation, so it
// separates the two mechanisms without being flaky.
//
// The non-power-of-two alphabets are also what execute the rejection branch at
// all: the shipped 64-symbol set makes the mask exact, so with it alone the
// branch would never run and this guard would be verifying nothing.
func Test_randomFromAlphabet(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		alphabet string
		//: draws per symbol; the total sample is len(alphabet) * perSymbol.
		perSymbol int
		//: permitted deviation from the uniform expectation, as a fraction.
		tolerance float64
	}
	tests := []tc{
		//: exact mask — no draw is ever rejected.
		{"the shipped 64-symbol alphabet", nanoIDAlphabet, 2000, 0.15},
		//: 62 of 64 masked values accepted: ~3 % rejection.
		{"a base62 alphabet", base62Alphabet, 2000, 0.15},
		//: 33 of 64 accepted: ~48 % rejection, the loop runs many rounds.
		{"a 33-symbol alphabet", nanoIDAlphabet[:33], 2000, 0.15},
		//: 3 of 4 accepted, and the smallest alphabet that is not a power of two.
		{"three symbols", "abc", 20000, 0.10},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		total := len(c.alphabet) * c.perSymbol
		out := make([]byte, total)
		if err := randomFromAlphabet(out, c.alphabet); err != nil {
			t.Fatalf("randomFromAlphabet = %v, want nil", err)
		}

		counts := make(map[byte]int, len(c.alphabet))
		for _, b := range out {
			//: a symbol outside the alphabet means the mask leaked a value the
			//: rejection was supposed to catch.
			if !strings.ContainsRune(c.alphabet, rune(b)) {
				t.Fatalf("emitted %q, outside the alphabet", b)
			}
			counts[b]++
		}
		//: every symbol must be reachable; a missing one means the mask is too
		//: narrow to address the top of the alphabet.
		if len(counts) != len(c.alphabet) {
			t.Errorf("only %d of %d symbols were ever emitted", len(counts), len(c.alphabet))
		}
		//: and no symbol may be over- or under-represented beyond the band.
		low := float64(c.perSymbol) * (1 - c.tolerance)
		high := float64(c.perSymbol) * (1 + c.tolerance)
		for _, sym := range []byte(c.alphabet) {
			got := float64(counts[sym])
			if got < low || got > high {
				t.Errorf("symbol %q appeared %d times, want %d±%.0f%% — the draw is biased",
					sym, counts[sym], c.perSymbol, c.tolerance*100)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: a zero-width request must terminate rather than spin looking for a
	//: character it will never be asked to place.
	if err := randomFromAlphabet(nil, nanoIDAlphabet); err != nil {
		t.Errorf("randomFromAlphabet(nil) = %v, want nil", err)
	}
}

// Test_NewNanoID_refuses pins the ADR 0031 refusal. A generator built from a
// non-positive length would return "" from every call — an identifier that
// identifies nothing, handed back as if it worked.
func Test_NewNanoID_refuses(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		size int
		//: whether the constructor must hand back a working generator.
		ok bool
	}
	tests := []tc{
		{"zero is refused", 0, false},
		{"a negative size is refused", -1, false},
		{"a large negative size is refused", -4096, false},
		{"one is the smallest usable size", 1, true},
		{"the canonical size", nanoIDDefaultSize, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		gen, err := NewNanoID(c.size)
		if c.ok {
			if err != nil {
				t.Fatalf("NewNanoID(%d) = %v, want nil", c.size, err)
			}
			got, genErr := gen.New()
			if genErr != nil {
				t.Fatalf("New = %v, want nil", genErr)
			}
			if len(got) != c.size {
				t.Errorf("New() = %q (%d chars), want %d", got, len(got), c.size)
			}
			return
		}
		//: the refusal must be the typed sentinel, not a nil error with a
		//: crippled generator.
		if err == nil {
			t.Fatalf("NewNanoID(%d) = nil error, want ID_INVALID_SIZE", c.size)
		}
		if gen != nil {
			t.Errorf("NewNanoID(%d) handed back a generator alongside its refusal", c.size)
		}
		if !errs.HasCode(err, CodeIDInvalidSize) {
			t.Errorf("NewNanoID(%d) = %v, want code %s", c.size, err, CodeIDInvalidSize)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
