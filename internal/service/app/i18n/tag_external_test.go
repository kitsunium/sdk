// Package i18n_test — the parser of a written language tag, and everything it
// refuses by name.
package i18n_test

import (
	"testing"

	corei18n "github.com/kitsunium/sdk/internal/core/app/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svci18n "github.com/kitsunium/sdk/internal/service/app/i18n"
)

func TestParseTagCanonicalises(t *testing.T) {
	t.Parallel()

	type tc struct {
		name                 string
		in                   string
		want                 string
		lang, script, region string
		wantParent           string
		hasParent            bool
	}

	cases := []tc{
		{name: "bare language", in: "fr", want: "fr", lang: "fr"},
		{name: "uppercased language", in: "FR", want: "fr", lang: "fr"},
		{name: "three letter language", in: "fil", want: "fil", lang: "fil"},
		{name: "language and region", in: "pt-br", want: "pt-BR", lang: "pt", region: "BR", wantParent: "pt", hasParent: true},
		{name: "language and script", in: "zh-hant", want: "zh-Hant", lang: "zh", script: "Hant", wantParent: "zh", hasParent: true},
		{name: "all three", in: "ZH-hAnT-tw", want: "zh-Hant-TW", lang: "zh", script: "Hant", region: "TW", wantParent: "zh-Hant", hasParent: true},
		{name: "numeric region", in: "es-419", want: "es-419", lang: "es", region: "419", wantParent: "es", hasParent: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			tag, err := svci18n.ParseTag(c.in)
			if err != nil {
				t.Fatalf("ParseTag(%q) = %v, want a tag", c.in, err)
			}
			if got := tag.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
			if got := tag.Language(); got != c.lang {
				t.Errorf("Language() = %q, want %q", got, c.lang)
			}
			if got := tag.Script(); got != c.script {
				t.Errorf("Script() = %q, want %q", got, c.script)
			}
			if got := tag.Region(); got != c.region {
				t.Errorf("Region() = %q, want %q", got, c.region)
			}

			parent, ok := tag.Parent()
			if ok != c.hasParent {
				t.Fatalf("Parent() ok = %v, want %v", ok, c.hasParent)
			}
			if ok && parent.String() != c.wantParent {
				t.Errorf("Parent() = %q, want %q", parent.String(), c.wantParent)
			}
		})
	}
}

func TestParseTagRefusesEverythingOutsideTheSubset(t *testing.T) {
	t.Parallel()

	// Each entry names a BCP 47 or RFC 4647 construct the domain refuses on
	// purpose. A test that only checked "garbage is refused" would pass while
	// the parser silently DROPPED an extension, which is the failure mode the
	// refusal list exists to prevent.
	cases := map[string]string{
		"empty":                  "",
		"POSIX separator":        "fr_FR",
		"POSIX with charset":     "en_US.UTF-8",
		"extended language":      "zh-cmn-Hans",
		"variant":                "de-CH-1901",
		"extension singleton":    "de-DE-u-co-phonebk",
		"private use":            "x-pig-latin",
		"private use suffix":     "en-x-custom",
		"grandfathered":          "i-klingon",
		"wildcard range":         "*",
		"extended filter range":  "de-*-DE",
		"single letter language": "e",
		"reserved four letter":   "abcd",
		"trailing separator":     "en-",
		"leading separator":      "-en",
		"doubled separator":      "en--US",
		"region before script":   "fr-FR-Latn",
		"two regions":            "fr-FR-CA",
		"digits in language":     "f1",
		"too long":               "fil-Hant-4199",
		"two digit region":       "fr-12",
		"digits as a script":     "zh-1234",
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tag, err := svci18n.ParseTag(in)
			if err == nil {
				t.Fatalf("ParseTag(%q) = %q, want a refusal", in, tag)
			}
			if !errs.HasCode(err, corei18n.CodeInvalidTag) {
				t.Errorf("code = %v, want CodeInvalidTag", err)
			}
			// The written tag travels with every refusal, the parser's own and
			// the value's, so the log names what was refused.
			if !hasField(err, "tag", in) {
				t.Errorf("refusal of %q carries no tag field: %v", in, errs.FieldsOf(err))
			}
		})
	}
}

// hasField reports whether err carries the string field key with value want.
func hasField(err error, key, want string) bool {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == key && field.StringValue() == want {
			return true
		}
	}
	return false
}
