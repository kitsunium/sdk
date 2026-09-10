// Package i18n_test — loading a catalogue directory through the codec domain.
package i18n_test

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	_ "github.com/kitsunium/sdk/internal/service/codec/json" // register the "json" Format
	svci18n "github.com/kitsunium/sdk/internal/service/i18n"
)

// jsonFormat is the Format the blank import above registers. It is spelled as
// a literal rather than taken from pkg/v1/codec, which internal/service must
// not import.
const jsonFormat = "json"

// englishJSON is a complete English catalogue file, in both entry shapes.
const englishJSON = `{
	"greeting": "Hello, {name}",
	"cart.items": {"one": "{n} item", "other": "{n} items"}
}`

// polishJSON is the same catalogue, complete for Polish's four categories.
const polishJSON = `{
	"greeting": "Cześć, {name}",
	"cart.items": {"one": "{n} produkt", "few": "{n} produkty", "many": "{n} produktów", "other": "{n} produktu"}
}`

func TestLoadFSReadsOneFilePerLanguage(t *testing.T) {
	t.Parallel()

	// io/fs is internal/core/vfs.FS unchanged (ADR 0056), so an embed.FS, an
	// os.DirFS, a vfs root and this test double are the same call and this
	// package imports neither vfs nor os.
	dir := fstest.MapFS{
		"locales/en.json":    {Data: []byte(englishJSON)},
		"locales/pl.json":    {Data: []byte(polishJSON)},
		"locales/nested/x":   {Data: []byte("ignored")},
		"locales/nested/y.z": {Data: []byte("ignored")},
	}

	store, err := svci18n.LoadFS(dir, "locales", jsonFormat, mustTag(t, "en"))
	if err != nil {
		t.Fatalf("LoadFS = %v", err)
	}

	tags := store.Tags()
	if len(tags) != 2 || tags[0].String() != "en" || tags[1].String() != "pl" {
		t.Fatalf("Tags() = %v, want [en pl] — a subdirectory must be ignored, not walked", tags)
	}

	printer, err := svci18n.NewPrinter(store, mustTag(t, "pl"))
	if err != nil {
		t.Fatalf("NewPrinter = %v", err)
	}
	got, err := printer.RenderCount("cart.items", corei18n.Int(5), corei18n.Args{"n": "5"})
	if err != nil {
		t.Fatalf("RenderCount = %v", err)
	}
	if want := "5 produktów"; got != want {
		t.Errorf("RenderCount = %q, want %q", got, want)
	}
}

func TestLoadFSTakesTheLanguageFromTheFilename(t *testing.T) {
	t.Parallel()

	// The whole ParseTag refusal list applies to filenames, including the
	// POSIX spelling — accepting "fr_FR.json" would mint a second Tag for
	// French and split the catalogue in half.
	cases := map[string]struct {
		name string
		code errs.Code
	}{
		"POSIX separator": {name: "fr_FR.json", code: corei18n.CodeInvalidTag},
		"not a tag":       {name: "messages.json", code: corei18n.CodeInvalidTag},
		"extension only":  {name: ".json", code: corei18n.CodeInvalidTag},
		"variant":         {name: "de-CH-1901.json", code: corei18n.CodeInvalidTag},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := fstest.MapFS{
				"locales/en.json":   {Data: []byte(englishJSON)},
				"locales/" + c.name: {Data: []byte(`{"greeting":"x"}`)},
			}
			_, err := svci18n.LoadFS(dir, "locales", jsonFormat, mustTag(t, "en"))
			if !errs.HasCode(err, c.code) {
				t.Errorf("LoadFS with %q = %v, want %v", c.name, err, c.code)
			}
		})
	}

	// A tag is canonicalised, so "PT-pt.json" is European Portuguese.
	dir := fstest.MapFS{"locales/PT-pt.json": {Data: []byte(`{"greeting":"Olá"}`)}}
	store, err := svci18n.LoadFS(dir, "locales", jsonFormat, mustTag(t, "pt-PT"))
	if err != nil {
		t.Fatalf("LoadFS = %v", err)
	}
	if tags := store.Tags(); len(tags) != 1 || tags[0].String() != "pt-PT" {
		t.Errorf("Tags() = %v, want [pt-PT]", tags)
	}
}

func TestLoadFSRefusesTwoFilesForOneLanguage(t *testing.T) {
	t.Parallel()

	// One would win, the choice would depend on directory order, and half
	// the strings would vanish.
	dir := fstest.MapFS{
		"locales/en.json": {Data: []byte(englishJSON)},
		"locales/EN.json": {Data: []byte(`{"greeting":"Hi, {name}"}`)},
	}

	_, err := svci18n.LoadFS(dir, "locales", jsonFormat, mustTag(t, "en"))
	if !errs.HasCode(err, svci18n.CodeCatalogInvalid) {
		t.Fatalf("LoadFS = %v, want CodeCatalogInvalid", err)
	}
}

func TestLoadFSRefusesAnUnregisteredFormatBeforeTouchingTheFilesystem(t *testing.T) {
	t.Parallel()

	// The fix is a blank import, and reporting it as a wiring fault beats
	// reporting it after a directory read that was never going to be usable.
	dir := fstest.MapFS{"locales/en.json": {Data: []byte(englishJSON)}}

	_, err := svci18n.LoadFS(dir, "locales", "not-a-registered-format", mustTag(t, "en"))
	if !errs.HasCode(err, svci18n.CodeCatalogLoadFailed) {
		t.Fatalf("LoadFS = %v, want CodeCatalogLoadFailed", err)
	}
}

func TestLoadFSKeepsTheFilesystemCauseInTheChain(t *testing.T) {
	t.Parallel()

	// The same discipline internal/service/vfs applies: the cause is wrapped
	// rather than replaced, so errors.Is against fs.ErrNotExist keeps
	// answering and a caller can tell "no catalogue directory" from "a
	// catalogue that will not compile".
	_, err := svci18n.LoadFS(fstest.MapFS{}, "absent", jsonFormat, mustTag(t, "en"))
	if !errs.HasCode(err, svci18n.CodeCatalogLoadFailed) {
		t.Fatalf("LoadFS = %v, want CodeCatalogLoadFailed", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("errors.Is(err, fs.ErrNotExist) = false; the cause was replaced rather than wrapped")
	}
}

func TestLoadFSRefusesAnEntryThatIsNeitherShape(t *testing.T) {
	t.Parallel()

	// Every coercion here has a plausible wrong answer: a JSON 1.0 would
	// become "1", a `false` would become "false", and both would render as a
	// translation the file does not contain.
	cases := map[string]string{
		"number":           `{"greeting": 42}`,
		"boolean":          `{"greeting": false}`,
		"null":             `{"greeting": null}`,
		"list":             `{"greeting": ["a","b"]}`,
		"nested object":    `{"greeting": {"one": {"deeper": "x"}}}`,
		"form is a number": `{"cart.items": {"one": 1, "other": "{n} items"}}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := fstest.MapFS{"locales/en.json": {Data: []byte(body)}}
			_, err := svci18n.LoadFS(dir, "locales", jsonFormat, mustTag(t, "en"))
			if !errs.HasCode(err, svci18n.CodeCatalogInvalid) {
				t.Errorf("LoadFS with %s = %v, want CodeCatalogInvalid", name, err)
			}
		})
	}
}

func TestLoadFSRefusesUndecodableBytes(t *testing.T) {
	t.Parallel()

	dir := fstest.MapFS{"locales/en.json": {Data: []byte("{ this is not json")}}

	_, err := svci18n.LoadFS(dir, "locales", jsonFormat, mustTag(t, "en"))
	if !errs.HasCode(err, svci18n.CodeCatalogLoadFailed) {
		t.Fatalf("LoadFS = %v, want CodeCatalogLoadFailed", err)
	}
}

func TestAnIncompleteTranslationFailsAtLoadNotAtRender(t *testing.T) {
	t.Parallel()

	// The whole posture, end to end and through a real file: a Polish
	// catalogue with only `one` and `other` looks finished to the person who
	// wrote it, and the program does not start.
	dir := fstest.MapFS{
		"locales/en.json": {Data: []byte(englishJSON)},
		"locales/pl.json": {Data: []byte(`{
			"greeting": "Cześć, {name}",
			"cart.items": {"one": "{n} produkt", "other": "{n} produktu"}
		}`)},
	}

	_, err := svci18n.LoadFS(dir, "locales", jsonFormat, mustTag(t, "en"))
	if !errs.HasCode(err, svci18n.CodeTranslationIncomplete) {
		t.Fatalf("LoadFS = %v, want CodeTranslationIncomplete", err)
	}
}
