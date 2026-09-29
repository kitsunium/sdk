package core_test

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	model "github.com/kitsunium/sdk/framework/model/internal/core"
)

// grammarBytes are the bytes the words are built from: every class the
// patterns name, its edges, and what none of them accepts.
const grammarBytes = "aAzZ09-_./ \t\n\v\f\rv1é\x00\xff"

// The matchers are the patterns applied by hand: on every short word of the
// alphabet, on words about the length bounds and on random ones, each says
// what the compiled pattern says.
func TestTheMatchersAgreeWithThePatterns(t *testing.T) {
	route := regexp.MustCompile(`^` + model.RoutePattern + `$`)
	cases := []struct {
		name    string
		pattern *regexp.Regexp
		match   func(string) bool
	}{
		{"segment", regexp.MustCompile(`^` + model.SegmentPattern + `$`), model.ValidSegment},
		{"name", regexp.MustCompile(`^` + model.NamePattern + `$`), model.ValidName},
		{"contract", regexp.MustCompile(`^` + model.ContractPattern + `$`), model.ValidContract},
		{"route", route, func(s string) bool {
			// A route names an endpoint only; a name is one without the route.
			_, err := model.ParseID("svc/endpoint/" + s)
			return err == nil && !model.ValidName(s)
		}},
	}
	words := wordsToCheck()
	for _, c := range cases {
		for _, w := range words {
			want := c.pattern.MatchString(w)
			if c.name == "route" {
				want = want && !model.ValidName(w)
			}
			if got := c.match(w); got != want {
				t.Errorf("%s %q: the matcher says %v, the pattern %v", c.name, w, got, want)
			}
		}
	}
}

// wordsToCheck are every word of up to three bytes of grammarBytes, words of
// 61 to 65 bytes, contracts and routes built from pieces, and random words.
func wordsToCheck() []string {
	words := []string{""}
	level := []string{""}
	for range 3 {
		var next []string
		for _, w := range level {
			for i := range len(grammarBytes) {
				next = append(next, w+grammarBytes[i:i+1])
			}
		}
		words, level = append(words, next...), next
	}
	for n := 61; n <= 65; n++ {
		for _, head := range []string{"a", "A", "0"} {
			for _, fill := range []string{"a", "Z", "-", "_", "."} {
				words = append(words, head+strings.Repeat(fill, n-1))
			}
		}
	}
	for _, name := range []string{"render", "r", "R", "a-b", strings.Repeat("a", 63), strings.Repeat("a", 64)} {
		for _, v := range []string{"/v1", "/v0", "/v9999", "/v10000", "/v", "/v1a", "/V1", "v1", "/v12/v3"} {
			words = append(words, name+v)
		}
	}
	for _, m := range []string{"GET", "G", "get", "GeT", "", "G T", "É"} {
		for _, p := range []string{" /", " /a", " /a b", " /a\tb", " /a\vb", " /\xff", "/a", "  /a", " //x"} {
			words = append(words, m+p)
		}
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		b := make([]byte, rng.IntN(12))
		for i := range b {
			b[i] = grammarBytes[rng.IntN(len(grammarBytes))]
		}
		words = append(words, string(b))
	}
	return words
}
