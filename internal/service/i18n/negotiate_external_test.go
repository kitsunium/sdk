// Package i18n_test — Accept-Language negotiation, against the RFCs.
package i18n_test

import (
	"strings"
	"testing"

	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svci18n "github.com/kitsunium/sdk/internal/service/i18n"
)

// negotiatorOver builds a negotiator over the named tags, falling back to the
// first.
func negotiatorOver(t *testing.T, spellings ...string) *svci18n.Negotiator {
	t.Helper()

	tags := make([]corei18n.TagValue, 0, len(spellings))
	for _, spelling := range spellings {
		tags = append(tags, mustTag(t, spelling))
	}

	negotiator, err := svci18n.NewNegotiator(tags, tags[0])
	if err != nil {
		t.Fatalf("NewNegotiator = %v", err)
	}
	return negotiator
}

func TestNegotiateFollowsRFC4647Lookup(t *testing.T) {
	t.Parallel()

	negotiator := negotiatorOver(t, "en", "fr", "pt-PT", "zh-Hant")

	cases := map[string]struct {
		header string
		want   string
	}{
		"exact match":                     {header: "fr", want: "fr"},
		"case insensitive":                {header: "FR", want: "fr"},
		"truncates the range one subtag":  {header: "fr-CH", want: "fr"},
		"truncates two subtags":           {header: "fr-Latn-CH", want: "fr"},
		"prefers the longest match":       {header: "pt-PT", want: "pt-PT"},
		"a narrower range finds the base": {header: "zh-Hant-TW", want: "zh-Hant"},
		"quality order wins":              {header: "de;q=0.9, fr;q=0.8", want: "fr"},
		"explicit quality beats implicit": {header: "fr;q=0.5, en", want: "en"},
		"first of equal qualities wins":   {header: "fr;q=0.5, en;q=0.5", want: "fr"},
		"whitespace is tolerated":         {header: "  fr ;  q = 0.5 ,  en ; q=0.4 ", want: "fr"},
		"unsupported falls through":       {header: "de, ja, fr", want: "fr"},
		"nothing matches":                 {header: "de, ja", want: "en"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := negotiator.Negotiate(c.header); got.String() != c.want {
				t.Errorf("Negotiate(%q) = %q, want %q", c.header, got, c.want)
			}
		})
	}
}

func TestLookupTruncatesTheRangeAndNeverExtendsIt(t *testing.T) {
	t.Parallel()

	// The consequence of implementing RFC 4647 §3.4 exactly, and the reason
	// it is documented rather than papered over: a range of "en" does NOT
	// select a supported "en-GB". Lookup truncates the RANGE; it never
	// lengthens it. The remedy is to name the catalogue after the base
	// language, which is why this is a documented behaviour and not a bug.
	negotiator := negotiatorOver(t, "fr", "en-GB")

	if got := negotiator.Negotiate("en"); got.String() != "fr" {
		t.Errorf("Negotiate(%q) = %q, want the fallback %q", "en", got, "fr")
	}
	if got := negotiator.Negotiate("en-GB"); got.String() != "en-GB" {
		t.Errorf("Negotiate(%q) = %q, want %q", "en-GB", got, "en-GB")
	}
}

func TestAZeroQualityIsARefusalAndUsesBasicFiltering(t *testing.T) {
	t.Parallel()

	// RFC 9110 §12.4.2: "q=0" means not acceptable. The tag it refuses is
	// decided by RFC 4647 §3.3.1 basic filtering — the range is a prefix of
	// the tag — so "en;q=0" refuses "en-GB" too, which is what a client
	// saying "not English" means. Selection uses §3.4 Lookup; refusal uses
	// §3.3.1. They are on opposite sides of the match on purpose.
	negotiator := negotiatorOver(t, "fr", "en", "en-GB")

	cases := map[string]struct {
		header string
		want   string
	}{
		"refuses the exact tag":        {header: "en;q=0, fr;q=0.1", want: "fr"},
		"refuses a narrowing of it":    {header: "en-GB, en;q=0", want: "fr"},
		"refuses even when alone":      {header: "en;q=0", want: "fr"},
		"a sibling stays acceptable":   {header: "en-GB;q=0, en", want: "en"},
		"zero does not select anybody": {header: "fr;q=0", want: "fr"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := negotiator.Negotiate(c.header); got.String() != c.want {
				t.Errorf("Negotiate(%q) = %q, want %q", c.header, got, c.want)
			}
		})
	}
}

func TestAMalformedHeaderIsNeverAnError(t *testing.T) {
	t.Parallel()

	// The header is written by a stranger. RFC 4647 §3.4 prescribes exactly
	// one response to a range that cannot be used — skip it — and failing a
	// request over a peculiar browser setting is the mistake ADR 0051 already
	// refused for `traceparent`. Negotiate has no error return at all.
	negotiator := negotiatorOver(t, "en", "fr")

	cases := map[string]struct {
		header string
		want   string
	}{
		"empty":                       {header: "", want: "en"},
		"only whitespace":             {header: "   ", want: "en"},
		"only commas":                 {header: ",,,", want: "en"},
		"trailing comma":              {header: "fr,", want: "fr"},
		"leading comma":               {header: ",fr", want: "fr"},
		"pure garbage":                {header: "!!!;;;===", want: "en"},
		"quality above one":           {header: "fr;q=2", want: "en"},
		"non numeric quality":         {header: "fr;q=abc", want: "en"},
		"four fraction digits":        {header: "fr;q=0.5000", want: "en"},
		"one with a fraction":         {header: "fr;q=1.5", want: "en"},
		"empty quality":               {header: "fr;q=", want: "en"},
		"bare parameter":              {header: "fr;q", want: "en"},
		"unknown parameter":           {header: "fr;charset=utf-8", want: "en"},
		"second parameter":            {header: "fr;q=0.9;x=1", want: "en"},
		"a bad element does not kill": {header: "fr;q=2, fr;q=0.4", want: "fr"},
		"nul byte in the range":       {header: "f\x00r", want: "en"},
		"very long range":             {header: strings.Repeat("a", 4096), want: "en"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := negotiator.Negotiate(c.header); got.String() != c.want {
				t.Errorf("Negotiate(%q) = %q, want %q", c.header, got, c.want)
			}
		})
	}
}

func TestTheWildcardIsSkippedDuringLookup(t *testing.T) {
	t.Parallel()

	// RFC 4647 §3.4 says the "*" range is skipped in Lookup. And "*;q=0" —
	// "nothing but what I listed" — changes nothing here, because a page has
	// to render and RFC 9110 §12.5.4 itself discourages answering 406 to an
	// Accept-Language a server cannot satisfy.
	negotiator := negotiatorOver(t, "en", "fr")

	cases := map[string]struct {
		header string
		want   string
	}{
		"wildcard alone":            {header: "*", want: "en"},
		"wildcard first":            {header: "*, fr", want: "fr"},
		"wildcard highest quality":  {header: "*;q=1.0, fr;q=0.1", want: "fr"},
		"wildcard refused entirely": {header: "fr, *;q=0", want: "fr"},
		"only a refused wildcard":   {header: "*;q=0", want: "en"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := negotiator.Negotiate(c.header); got.String() != c.want {
				t.Errorf("Negotiate(%q) = %q, want %q", c.header, got, c.want)
			}
		})
	}
}

func TestAnEnormousHeaderIsBoundedRatherThanParsedWhole(t *testing.T) {
	t.Parallel()

	// A header is client-controlled and can be arbitrarily long; parsing all
	// of it would let a client spend the server's time on its own behalf. The
	// elements past the bound are IGNORED rather than treated as an error.
	negotiator := negotiatorOver(t, "en", "fr")

	header := strings.Repeat("de;q=0.9,", 5000) + "fr"
	if got := negotiator.Negotiate(header); got.String() != "en" {
		t.Errorf("Negotiate(huge) = %q, want the fallback %q — the tail past the bound is ignored", got, "en")
	}

	// And a header within the bound still reaches its last element.
	header = strings.Repeat("de;q=0.9,", 8) + "fr"
	if got := negotiator.Negotiate(header); got.String() != "fr" {
		t.Errorf("Negotiate(short) = %q, want %q", got, "fr")
	}
}

func TestNewNegotiatorRefusesAnUnfinishedWiring(t *testing.T) {
	t.Parallel()

	english := mustTag(t, "en")

	// ADR 0031's refuse half: an empty set is not "accept anything".
	if _, err := svci18n.NewNegotiator(nil, english); !errs.HasCode(err, svci18n.CodeNegotiationEmpty) {
		t.Errorf("NewNegotiator(nil) = %v, want CodeNegotiationEmpty", err)
	}

	var unset corei18n.TagValue
	if _, err := svci18n.NewNegotiator([]corei18n.TagValue{english}, unset); !errs.HasCode(err, svci18n.CodeCatalogInvalid) {
		t.Errorf("NewNegotiator with an unset fallback = %v, want CodeCatalogInvalid", err)
	}

	// A fallback outside the set would make the common path return a
	// language the caller declared it does not serve.
	if _, err := svci18n.NewNegotiator([]corei18n.TagValue{english}, mustTag(t, "fr")); !errs.HasCode(err, svci18n.CodeCatalogInvalid) {
		t.Errorf("NewNegotiator with an unsupported fallback = %v, want CodeCatalogInvalid", err)
	}
}

func TestTheSupportedSetIsClonedInAndOut(t *testing.T) {
	t.Parallel()

	english, french := mustTag(t, "en"), mustTag(t, "fr")
	supported := []corei18n.TagValue{english, french}

	negotiator, err := svci18n.NewNegotiator(supported, english)
	if err != nil {
		t.Fatalf("NewNegotiator = %v", err)
	}

	// The caller's slice must not be able to change the set every request is
	// negotiated against.
	supported[1] = corei18n.TagValue{}
	if got := negotiator.Negotiate("fr"); got != french {
		t.Errorf("Negotiate = %q after the caller mutated its slice, want %q", got, french)
	}

	out := negotiator.Supported()
	out[0] = corei18n.TagValue{}
	if again := negotiator.Supported(); again[0] != english {
		t.Error("Supported() handed out its internal slice")
	}
}

func TestNegotiateNeverReturnsTheZeroTag(t *testing.T) {
	t.Parallel()

	// A renderer has to render. Whatever the header says, the result names a
	// language the caller declared it serves.
	negotiator := negotiatorOver(t, "en", "fr", "ja")

	for _, header := range []string{
		"", "*", "*;q=0", "en;q=0, fr;q=0, ja;q=0", "!!!", ",,,",
		"zz-ZZ", "xx;q=0.9,yy;q=0.8", strings.Repeat("q;q=0,", 100),
	} {
		got := negotiator.Negotiate(header)
		if got.IsZero() {
			t.Errorf("Negotiate(%q) returned the zero Tag", header)
		}
	}
}
