// Package i18n_test — the port's values as a caller builds and uses them.
package i18n_test

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/core/app/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxPublicRunes is the SDK-wide bound on a wire-safe Public message (rule 4).
const maxPublicRunes int = 120

// secret is the recognisable argument value the non-disclosure test drives
// every error path with. It looks like something a user would type into a form
// and something an operator would be sorry to find in a log.
const secret string = "sk-live-4f9a2c8e-EXFILTRATE-ME"

func TestNewTagCanonicalises(t *testing.T) {
	t.Parallel()

	type tc struct {
		name                          string
		lang, script, region          string
		want                          string
		wantLang, wantScript, wantReg string
		wantParent                    string
		hasParent                     bool
	}

	// The value's own case rule: lowercase language, Titlecase script,
	// UPPERCASE alpha region. Reading a WRITTEN tag — the separator, the
	// refusals by name — is internal/service/app/i18n's ParseTag, tested there.
	cases := []tc{
		{name: "bare language", lang: "fr", want: "fr", wantLang: "fr"},
		{name: "uppercased language", lang: "FR", want: "fr", wantLang: "fr"},
		{name: "three letter language", lang: "fil", want: "fil", wantLang: "fil"},
		{name: "language and region", lang: "pt", region: "br", want: "pt-BR", wantLang: "pt", wantReg: "BR", wantParent: "pt", hasParent: true},
		{name: "language and script", lang: "zh", script: "hant", want: "zh-Hant", wantLang: "zh", wantScript: "Hant", wantParent: "zh", hasParent: true},
		{name: "all three", lang: "ZH", script: "hAnT", region: "tw", want: "zh-Hant-TW", wantLang: "zh", wantScript: "Hant", wantReg: "TW", wantParent: "zh-Hant", hasParent: true},
		{name: "numeric region", lang: "es", region: "419", want: "es-419", wantLang: "es", wantReg: "419", wantParent: "es", hasParent: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			tag, err := i18n.NewTag(c.lang, c.script, c.region)
			if err != nil {
				t.Fatalf("NewTag(%q, %q, %q) = %v, want a tag", c.lang, c.script, c.region, err)
			}
			if got := tag.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
			if got := tag.Language(); got != c.wantLang {
				t.Errorf("Language() = %q, want %q", got, c.wantLang)
			}
			if got := tag.Script(); got != c.wantScript {
				t.Errorf("Script() = %q, want %q", got, c.wantScript)
			}
			if got := tag.Region(); got != c.wantReg {
				t.Errorf("Region() = %q, want %q", got, c.wantReg)
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

func TestNewTagRefusesASubtagOfTheWrongShape(t *testing.T) {
	t.Parallel()

	// Whoever assembles a tag, the value holds its subtags to the subset's
	// shapes: no path into the type skips them.
	cases := map[string][3]string{
		"empty language":         {"", "", ""},
		"single letter language": {"e", "", ""},
		"reserved four letter":   {"abcd", "", ""},
		"digits in language":     {"f1", "", ""},
		"underscore in language": {"fr_FR", "", ""},
		"short script":           {"zh", "Han", ""},
		"digits in script":       {"zh", "H4nt", ""},
		"long script":            {"zh", "Hantx", ""},
		"one letter region":      {"fr", "", "F"},
		"two digit region":       {"fr", "", "12"},
		"letters as M.49":        {"es", "", "abc"},
		"four digit region":      {"de", "", "1901"},
	}

	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tag, err := i18n.NewTag(parts[0], parts[1], parts[2])
			if err == nil {
				t.Fatalf("NewTag(%q) = %q, want a refusal", parts, tag)
			}
			if !errs.HasCode(err, i18n.CodeInvalidTag) {
				t.Errorf("code = %v, want CodeInvalidTag", err)
			}
		})
	}
}

func TestZeroTagNamesNoLanguage(t *testing.T) {
	t.Parallel()

	var zero i18n.TagValue

	if !zero.IsZero() {
		t.Error("IsZero() = false on the zero Tag")
	}
	if zero.String() != "" {
		t.Errorf("String() = %q, want empty", zero.String())
	}
	if zero.Language() != "" {
		t.Errorf("Language() = %q, want empty", zero.Language())
	}
	if _, ok := zero.Parent(); ok {
		t.Error("Parent() reported a parent for the zero Tag")
	}
}

func TestFormOtherIsTheZeroValue(t *testing.T) {
	t.Parallel()

	// CLDR guarantees `other` in every language, so it is the only category
	// that is safe as a forgotten return value.
	var zero i18n.Form

	if zero != i18n.FormOther {
		t.Fatalf("zero Form = %v, want FormOther", zero)
	}
	if zero.String() != "other" {
		t.Errorf("String() = %q, want %q", zero.String(), "other")
	}
}

func TestFormOutsideTheEnumNeverRendersAsACategory(t *testing.T) {
	t.Parallel()

	corrupt := i18n.Form(200)

	if corrupt.Valid() {
		t.Error("Valid() = true for a Form outside the enum")
	}
	if got := corrupt.String(); got != "invalid" {
		t.Errorf("String() = %q, want %q", got, "invalid")
	}
}

func TestParseFormAcceptsTheSixAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]i18n.Form{
		"zero": i18n.FormZero, "one": i18n.FormOne, "two": i18n.FormTwo,
		"few": i18n.FormFew, "many": i18n.FormMany, "other": i18n.FormOther,
	} {
		got, err := i18n.ParseForm(name)
		if err != nil {
			t.Fatalf("ParseForm(%q) = %v", name, err)
		}
		if got != want {
			t.Errorf("ParseForm(%q) = %v, want %v", name, got, want)
		}
	}

	// A dropped typo of "other" would leave a message with no catch-all at
	// all, which is why an unknown name is refused rather than ignored.
	for _, name := range []string{"", "otehr", "Other", "singular", "plural", "1"} {
		if _, err := i18n.ParseForm(name); !errs.HasCode(err, i18n.CodeInvalidForm) {
			t.Errorf("ParseForm(%q) = %v, want CodeInvalidForm", name, err)
		}
	}
}

func TestIntDescribesTheAbsoluteValue(t *testing.T) {
	t.Parallel()

	for _, in := range []int64{0, 1, -1, 42, -42, math.MaxInt64} {
		count := i18n.Int(in)

		want := uint64(in)
		if in < 0 {
			want = uint64(-in)
		}
		if count.IntegerPart() != want {
			t.Errorf("Int(%d).IntegerPart() = %d, want %d", in, count.IntegerPart(), want)
		}
		if count.VisibleFractionDigits() != 0 {
			t.Errorf("Int(%d).VisibleFractionDigits() = %d, want 0", in, count.VisibleFractionDigits())
		}
		if !count.IsIntegerValued() {
			t.Errorf("Int(%d).IsIntegerValued() = false", in)
		}
	}

	// math.MinInt64 is where -n is not representable; the two's-complement
	// path exists for exactly this input.
	if got := i18n.Int(math.MinInt64).IntegerPart(); got != 1<<63 {
		t.Errorf("Int(math.MinInt64).IntegerPart() = %d, want %d", got, uint64(1)<<63)
	}
}

func TestDecimalDerivesTheCLDROperands(t *testing.T) {
	t.Parallel()

	type tc struct {
		name              string
		value             float64
		digits            int
		integer, fraction uint64
		visible           int
	}

	cases := []tc{
		{name: "integer displayed without decimals", value: 1, digits: 0, integer: 1, visible: 0},
		{name: "integer displayed with decimals", value: 1, digits: 1, integer: 1, visible: 1},
		{name: "one and a half", value: 1.5, digits: 1, integer: 1, fraction: 5, visible: 1},
		{name: "trailing zero is visible", value: 1.5, digits: 2, integer: 1, fraction: 50, visible: 2},
		{name: "negative discards the sign", value: -2.25, digits: 2, integer: 2, fraction: 25, visible: 2},
		// Rounding carries exactly as the printed string would: 0.999 shown
		// with two digits IS "1.00", and reporting an integer part of 0 would
		// make the SDK disagree with the number beside it on the page.
		{name: "rounding carries into the integer part", value: 0.999, digits: 2, integer: 1, fraction: 0, visible: 2},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			count, err := i18n.Decimal(c.value, c.digits)
			if err != nil {
				t.Fatalf("Decimal(%v, %d) = %v", c.value, c.digits, err)
			}
			if count.IntegerPart() != c.integer {
				t.Errorf("IntegerPart() = %d, want %d", count.IntegerPart(), c.integer)
			}
			if count.FractionValue() != c.fraction {
				t.Errorf("FractionValue() = %d, want %d", count.FractionValue(), c.fraction)
			}
			if count.VisibleFractionDigits() != c.visible {
				t.Errorf("VisibleFractionDigits() = %d, want %d", count.VisibleFractionDigits(), c.visible)
			}
			if got := count.IsIntegerValued(); got != (c.fraction == 0) {
				t.Errorf("IsIntegerValued() = %v, want %v", got, c.fraction == 0)
			}
		})
	}
}

func TestDecimalRefusesWhatItCannotDescribe(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		value  float64
		digits int
	}{
		"NaN":                {value: math.NaN(), digits: 0},
		"positive infinity":  {value: math.Inf(1), digits: 0},
		"negative infinity":  {value: math.Inf(-1), digits: 0},
		"negative digits":    {value: 1, digits: -1},
		"too many digits":    {value: 1, digits: 10},
		"integer overflows":  {value: math.MaxInt64 * 2.0, digits: 0},
		"exactly at the max": {value: math.MaxInt64, digits: 0},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := i18n.Decimal(c.value, c.digits); !errs.HasCode(err, i18n.CodeInvalidCount) {
				t.Errorf("Decimal(%v, %d) = %v, want CodeInvalidCount", c.value, c.digits, err)
			}
		})
	}
}

func TestValidateKeyRefusesWhatCannotBeShown(t *testing.T) {
	t.Parallel()

	if err := i18n.ValidateKey("checkout.button.pay"); err != nil {
		t.Fatalf("ValidateKey on an ordinary key = %v", err)
	}
	// A key is printed on screen when the translation is missing, so the two
	// characters that make printing unsafe are refused.
	for name, key := range map[string]i18n.Key{
		"empty":   "",
		"newline": "checkout\nbutton",
		"escape":  "checkout\x1b[2Kbutton",
		"nul":     "checkout\x00button",
		"delete":  "checkout\x7fbutton",
	} {
		if err := i18n.ValidateKey(key); !errs.HasCode(err, i18n.CodeInvalidKey) {
			t.Errorf("ValidateKey(%s) = %v, want CodeInvalidKey", name, err)
		}
	}
}

// compiled builds a message the way the compiler in internal/service/app/i18n
// hands it over: spans for `other`, and spans per plural category.
func compiled(t *testing.T, other []i18n.PartValue, plural map[i18n.Form][]i18n.PartValue) i18n.MessageValue {
	t.Helper()

	body, err := i18n.NewPattern(other)
	if err != nil {
		t.Fatalf("NewPattern(other) = %v", err)
	}
	var forms []i18n.FormPatternValue
	for _, form := range []i18n.Form{i18n.FormZero, i18n.FormOne, i18n.FormTwo, i18n.FormFew, i18n.FormMany} {
		parts, ok := plural[form]
		if !ok {
			continue
		}
		formBody, formErr := i18n.NewPattern(parts)
		if formErr != nil {
			t.Fatalf("NewPattern(%v) = %v", form, formErr)
		}
		forms = append(forms, i18n.FormPatternValue{Form: form, Body: formBody})
	}
	message, err := i18n.NewCompiledMessage(body, forms)
	if err != nil {
		t.Fatalf("NewCompiledMessage = %v", err)
	}
	return message
}

// lit and ph spell one literal span and one placeholder span.
func lit(text string) i18n.PartValue { return i18n.PartValue{Text: text} }

func ph(name string) i18n.PartValue { return i18n.PartValue{Name: name} }

func TestMessageFormatSubstitutesNamedPlaceholders(t *testing.T) {
	t.Parallel()

	message := compiled(t, []i18n.PartValue{lit("Welcome back, "), ph("name"), lit(". You have "), ph("count"), lit(" messages.")}, nil)

	got, err := message.Format(i18n.FormOther, i18n.Args{"name": "Ada", "count": "3"})
	if err != nil {
		t.Fatalf("Format = %v", err)
	}
	if want := "Welcome back, Ada. You have 3 messages."; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
}

func TestMessageFormatNeverRescansASubstitutedValue(t *testing.T) {
	t.Parallel()

	// A value is untrusted. If substitution were a second pass, a user whose
	// display name is "{admin_token}" would read an argument the caller never
	// meant to show them. It is one pass, and this is the proof.
	message := compiled(t, []i18n.PartValue{lit("Hello, "), ph("name")}, nil)

	got, err := message.Format(i18n.FormOther, i18n.Args{
		"name":        "{admin_token}",
		"admin_token": secret,
	})
	if err != nil {
		t.Fatalf("Format = %v", err)
	}
	if want := "Hello, {admin_token}"; got != want {
		t.Errorf("Format = %q, want %q — a substituted value must not be rescanned", got, want)
	}
	if strings.Contains(got, secret) {
		t.Fatal("the rendered message disclosed an argument the pattern never named")
	}
}

func TestMessageFormatRefusesAMissingArgumentAndIgnoresASurplusOne(t *testing.T) {
	t.Parallel()

	message := compiled(t, []i18n.PartValue{lit("Hello, "), ph("name")}, nil)

	// Missing is a hole in the sentence being rendered now.
	if _, err := message.Format(i18n.FormOther, i18n.Args{}); !errs.HasCode(err, i18n.CodeArgumentMissing) {
		t.Errorf("Format with no args = %v, want CodeArgumentMissing", err)
	}

	// Surplus is a placeholder another language uses; refusing it would make
	// the English render fail for a reason that lives in the French file.
	got, err := message.Format(i18n.FormOther, i18n.Args{"name": "Ada", "gender": "f"})
	if err != nil {
		t.Fatalf("Format with a surplus arg = %v, want it ignored", err)
	}
	if want := "Hello, Ada"; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
}

func TestNewPatternRefusesSpansThatAreNotAPattern(t *testing.T) {
	t.Parallel()

	// The compiler never produces these; the value refuses them anyway, so no
	// path into a MessageValue carries a span a render could not honour.
	cases := map[string][]i18n.PartValue{
		"a span that is both":       {{Text: "Hello, ", Name: "name"}},
		"a span that is neither":    {{}},
		"a name with a space":       {ph("first name")},
		"a positional name":         {ph("0")},
		"a format specifier":        {ph("count:02d")},
		"a name after good spans":   {lit("Hello, "), ph("name"), ph("x|upper")},
		"an empty placeholder name": {lit("a"), {Name: ""}, lit("b")},
	}

	for name, parts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := i18n.NewPattern(parts); !errs.HasCode(err, i18n.CodeInvalidPattern) {
				t.Errorf("NewPattern(%v) = %v, want CodeInvalidPattern", parts, err)
			}
		})
	}
}

func TestValidPlaceholderIsTheNameGrammar(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"n", "name", "_private", "count2", "A_B_c"} {
		if !i18n.ValidPlaceholder(name) {
			t.Errorf("ValidPlaceholder(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "1st", "0", "first name", "count:02d", "name|upper", "é"} {
		if i18n.ValidPlaceholder(name) {
			t.Errorf("ValidPlaceholder(%q) = true, want false", name)
		}
	}
}

func TestNewCompiledMessageRequiresOtherAndOrderedCategories(t *testing.T) {
	t.Parallel()

	files, err := i18n.NewPattern([]i18n.PartValue{ph("n"), lit(" files")})
	if err != nil {
		t.Fatalf("NewPattern = %v", err)
	}
	file, err := i18n.NewPattern([]i18n.PartValue{ph("n"), lit(" file")})
	if err != nil {
		t.Fatalf("NewPattern = %v", err)
	}

	// `other` is the only category every language defines, so a message whose
	// other body was never compiled has no pattern for most quantities.
	var never i18n.PatternValue
	if _, err := i18n.NewCompiledMessage(never, nil); !errs.HasCode(err, i18n.CodePluralFormMissing) {
		t.Errorf("NewCompiledMessage(zero other) = %v, want CodePluralFormMissing", err)
	}

	refusals := map[string][]i18n.FormPatternValue{
		"other as a plural category": {{Form: i18n.FormOther, Body: file}},
		"a category outside the six": {{Form: i18n.Form(200), Body: file}},
		"categories out of order":    {{Form: i18n.FormMany, Body: file}, {Form: i18n.FormOne, Body: file}},
		"a category twice":           {{Form: i18n.FormOne, Body: file}, {Form: i18n.FormOne, Body: file}},
	}
	for name, plural := range refusals {
		if _, err := i18n.NewCompiledMessage(files, plural); !errs.HasCode(err, i18n.CodeInvalidForm) {
			t.Errorf("%s: NewCompiledMessage = %v, want CodeInvalidForm", name, err)
		}
	}
	if _, err := i18n.NewCompiledMessage(files, []i18n.FormPatternValue{{Form: i18n.FormOne, Body: never}}); !errs.HasCode(err, i18n.CodePluralFormMissing) {
		t.Errorf("NewCompiledMessage(uncompiled one) = %v, want CodePluralFormMissing", err)
	}

	message, err := i18n.NewCompiledMessage(files, []i18n.FormPatternValue{{Form: i18n.FormOne, Body: file}})
	if err != nil {
		t.Fatalf("NewCompiledMessage = %v", err)
	}
	if !message.IsPlural() {
		t.Error("IsPlural() = false for a message with two categories")
	}
	if !message.HasForm(i18n.FormOne) || !message.HasForm(i18n.FormOther) {
		t.Error("HasForm did not report a category the message carries")
	}
	if message.HasForm(i18n.FormFew) {
		t.Error("HasForm reported a category the message does not carry")
	}
	if plain := compiled(t, []i18n.PartValue{lit("Hello")}, nil); plain.IsPlural() {
		t.Error("IsPlural() = true for a message with only other")
	}
}

func TestFormatRefusesAFormTheMessageDoesNotCarryRatherThanFallingBack(t *testing.T) {
	t.Parallel()

	// Falling back to `other` here is the silent wrong sentence the whole
	// domain is built to refuse: a Polish reader would see a number agreement
	// error, and nothing in the system would know.
	message := compiled(t, []i18n.PartValue{ph("n"), lit(" pliku")},
		map[i18n.Form][]i18n.PartValue{i18n.FormOne: {ph("n"), lit(" plik")}})

	got, err := message.Format(i18n.FormFew, i18n.Args{"n": "3"})
	if !errs.HasCode(err, i18n.CodePluralFormMissing) {
		t.Fatalf("Format(FormFew) = %q, %v — want CodePluralFormMissing", got, err)
	}
	if got != "" {
		t.Errorf("Format returned %q alongside the refusal, want empty", got)
	}
}

func TestTheZeroMessageIsNotAnEmptyTranslation(t *testing.T) {
	t.Parallel()

	// A zero Message that rendered as "" would be indistinguishable from a
	// translation someone deliberately left blank.
	var zero i18n.MessageValue

	if _, err := zero.Format(i18n.FormOther, nil); !errs.HasCode(err, i18n.CodePluralFormMissing) {
		t.Errorf("zero Message Format = %v, want CodePluralFormMissing", err)
	}

	// A deliberately empty translation, by contrast, renders as empty and
	// succeeds.
	empty := compiled(t, nil, nil)
	got, err := empty.Format(i18n.FormOther, nil)
	if err != nil || got != "" {
		t.Errorf("empty message Format = %q, %v — want \"\", nil", got, err)
	}
}

func TestNoErrorEverNamesAnArgumentValue(t *testing.T) {
	t.Parallel()

	// The security property, stated in errors.go and proved here: a message
	// pattern is trusted (the developer wrote it) and an argument value is
	// not (a user typed it). Identifiers may travel in an error; values never
	// may — not in Error(), not in Public, not in Private, not in a field.
	plural := compiled(t, []i18n.PartValue{ph("n"), lit(" files")},
		map[i18n.Form][]i18n.PartValue{i18n.FormOne: {ph("n"), lit(" file")}})
	plain := compiled(t, []i18n.PartValue{lit("Hello, "), ph("name")}, nil)

	// Every error path in this package that can be reached with an Args map
	// holding a secret.
	var failures []error
	if _, argErr := plain.Format(i18n.FormOther, i18n.Args{"other_name": secret}); argErr != nil {
		failures = append(failures, argErr)
	}
	if _, formErr := plural.Format(i18n.FormFew, i18n.Args{"n": secret}); formErr != nil {
		failures = append(failures, formErr)
	}
	if len(failures) != 2 {
		t.Fatalf("expected both error paths to fire, got %d", len(failures))
	}

	for _, failure := range failures {
		assertNoSecret(t, failure)
	}
}

// assertNoSecret fails when the secret appears anywhere the error can be read
// from.
func assertNoSecret(t *testing.T, err error) {
	t.Helper()

	if strings.Contains(err.Error(), secret) {
		t.Errorf("Error() disclosed an argument value: %q", err.Error())
	}
	if strings.Contains(errs.PublicOf(err), secret) {
		t.Errorf("Public disclosed an argument value: %q", errs.PublicOf(err))
	}
	if strings.Contains(errs.PrivateOf(err), secret) {
		t.Errorf("Private disclosed an argument value: %q", errs.PrivateOf(err))
	}
	for _, field := range errs.FieldsOf(err) {
		if strings.Contains(field.StringValue(), secret) {
			t.Errorf("field %q disclosed an argument value: %q", field.Key(), field.StringValue())
		}
	}
}

func TestEveryPublicIsWireSafe(t *testing.T) {
	t.Parallel()

	// SDK rule 4: a Public is a string literal of at most 120 runes with no
	// newline, because it is rendered into a response and into a log line.
	sentinels := map[string]error{
		"InvalidTag":        i18n.InvalidTag,
		"InvalidKey":        i18n.InvalidKey,
		"InvalidPattern":    i18n.InvalidPattern,
		"ArgumentMissing":   i18n.ArgumentMissing,
		"MessageNotFound":   i18n.MessageNotFound,
		"PluralFormMissing": i18n.PluralFormMissing,
		"InvalidCount":      i18n.InvalidCount,
		"InvalidForm":       i18n.InvalidForm,
		// The five a concrete catalogue raises, declared here since ADR 0160.
		"UnsupportedLanguage":   i18n.UnsupportedLanguage,
		"CatalogInvalid":        i18n.CatalogInvalid,
		"CatalogLoadFailed":     i18n.CatalogLoadFailed,
		"TranslationIncomplete": i18n.TranslationIncomplete,
		"NegotiationEmpty":      i18n.NegotiationEmpty,
	}

	for name, sentinel := range sentinels {
		public := errs.PublicOf(sentinel)
		if public == "" {
			t.Errorf("%s carries an empty Public", name)
		}
		if utf8.RuneCountInString(public) > maxPublicRunes {
			t.Errorf("%s Public is %d runes, want at most %d", name, utf8.RuneCountInString(public), maxPublicRunes)
		}
		if strings.ContainsAny(public, "\n\r") {
			t.Errorf("%s Public contains a newline", name)
		}
	}
}
