// Package i18n_test — the placeholder syntax a translator writes, compiled.
package i18n_test

import (
	"testing"

	corei18n "github.com/kitsunium/sdk/internal/core/app/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svci18n "github.com/kitsunium/sdk/internal/service/app/i18n"
)

func TestNewMessageCompilesNamedPlaceholders(t *testing.T) {
	t.Parallel()

	message, err := svci18n.NewMessage("Welcome back, {name}. You have {count} messages.")
	if err != nil {
		t.Fatalf("NewMessage = %v", err)
	}

	got, err := message.Format(corei18n.FormOther, corei18n.Args{"name": "Ada", "count": "3"})
	if err != nil {
		t.Fatalf("Format = %v", err)
	}
	if want := "Welcome back, Ada. You have 3 messages."; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
}

func TestNewMessageEscapesDoubledBraces(t *testing.T) {
	t.Parallel()

	message, err := svci18n.NewMessage("Use {{name}} to interpolate {name}")
	if err != nil {
		t.Fatalf("NewMessage = %v", err)
	}

	got, err := message.Format(corei18n.FormOther, corei18n.Args{"name": "Ada"})
	if err != nil {
		t.Fatalf("Format = %v", err)
	}
	if want := "Use {name} to interpolate Ada"; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
}

func TestPatternRefusalsAreEachNamed(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"unclosed placeholder":  "Hello, {name",
		"unmatched close":       "Hello, name}",
		"empty placeholder":     "Hello, {}",
		"format specifier":      "Hello, {name:>10}",
		"positional index":      "Hello, {0}",
		"ICU plural construct":  "{n, plural, one{# file} other{# files}}",
		"filter call":           "Hello, {name|upper}",
		"space in name":         "Hello, {first name}",
		"leading digit in name": "Hello, {1st}",
	}

	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := svci18n.NewMessage(text); !errs.HasCode(err, corei18n.CodeInvalidPattern) {
				t.Errorf("NewMessage(%q) = %v, want CodeInvalidPattern", text, err)
			}
		})
	}
}

func TestPluralMessageRequiresTheOtherForm(t *testing.T) {
	t.Parallel()

	// `other` is the only category every language defines, so a plural
	// message without it has no pattern for the quantities no clause matches.
	_, err := svci18n.NewPluralMessage(map[corei18n.Form]string{corei18n.FormOne: "{n} file"})
	if !errs.HasCode(err, corei18n.CodePluralFormMissing) {
		t.Fatalf("NewPluralMessage without other = %v, want CodePluralFormMissing", err)
	}

	message, err := svci18n.NewPluralMessage(map[corei18n.Form]string{
		corei18n.FormMany:  "{n} files!",
		corei18n.FormOne:   "{n} file",
		corei18n.FormOther: "{n} files",
	})
	if err != nil {
		t.Fatalf("NewPluralMessage = %v", err)
	}
	if !message.IsPlural() || !message.HasForm(corei18n.FormOne) || !message.HasForm(corei18n.FormMany) {
		t.Error("the compiled message does not carry the categories it was given")
	}
	got, err := message.Format(corei18n.FormOne, corei18n.Args{"n": "1"})
	if err != nil || got != "1 file" {
		t.Errorf("Format(FormOne) = %q, %v — want \"1 file\", nil", got, err)
	}

	// A malformed sibling pattern, and a category outside the six, stop it.
	if _, err := svci18n.NewPluralMessage(map[corei18n.Form]string{corei18n.FormOther: "{n}", corei18n.FormOne: "{n"}); !errs.HasCode(err, corei18n.CodeInvalidPattern) {
		t.Errorf("NewPluralMessage with a malformed one = %v, want CodeInvalidPattern", err)
	}
	if _, err := svci18n.NewPluralMessage(map[corei18n.Form]string{corei18n.FormOther: "{n}", corei18n.Form(200): "{n}"}); !errs.HasCode(err, corei18n.CodeInvalidForm) {
		t.Errorf("NewPluralMessage with a category outside the six = %v, want CodeInvalidForm", err)
	}
}

func TestAnEmptyPatternCompilesToAnEmptyTranslation(t *testing.T) {
	t.Parallel()

	// The zero MessageValue refuses to render; an empty pattern is a choice
	// a translator made, and renders as one.
	empty, err := svci18n.NewMessage("")
	if err != nil {
		t.Fatalf("NewMessage(\"\") = %v", err)
	}
	got, err := empty.Format(corei18n.FormOther, nil)
	if err != nil || got != "" {
		t.Errorf("empty message Format = %q, %v — want \"\", nil", got, err)
	}
}
