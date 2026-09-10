// Package i18n — the concrete catalogue, and everything it refuses at load.
package i18n

import (
	"maps"
	"slices"
	"strings"

	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Store is an immutable, concurrent-safe [corei18n.Catalog] built from one
// catalogue per language.
//
// It implements the two ADR 0039 siblings as well: [corei18n.KeyLister], so a
// caller's own test can assert no translation is missing, and
// [corei18n.Fallbacker], so a renderer can find the language that stands in
// without the fallback being a second constructor argument everywhere.
//
// Nothing mutates after [NewStore] returns. There is no Add, no Reload and no
// Set: a catalogue that can change under a request is a catalogue where two
// paragraphs of one page can come from two different versions of the text, and
// reloading translations is a process restart or a second Store swapped in by
// the caller — both of which the caller can already do, and neither of which
// needs a lock on the render path.
type Store struct {
	// fallback is the language a key is looked for in when the requested one
	// does not hold it. It is never the zero tag: NewStore refuses that.
	fallback corei18n.TagValue
	// byTag holds one compiled message map per language.
	byTag map[corei18n.TagValue]map[corei18n.Key]corei18n.MessageValue
	// tags is the sorted key set of byTag, computed once.
	tags []corei18n.TagValue
}

// NewStore compiles catalogues into a [Store], or refuses.
//
// # Everything it refuses, and why each is refused HERE
//
//   - A zero fallback [corei18n.TagValue] — [CatalogInvalid]. ADR 0031's
//     refuse half: there is no language the SDK could pick, and picking
//     English would mean a Japanese-only product silently shipping English.
//   - A fallback the catalogues do not hold — [CatalogInvalid]. A fallback
//     that resolves to nothing is not a fallback; every miss would return the
//     key, and the misconfiguration would look exactly like a missing
//     translation.
//   - A language with no reviewed CLDR rule — [UnsupportedLanguage].
//   - An empty or control-bearing key — [corei18n.InvalidKey].
//   - A malformed pattern — [corei18n.InvalidPattern].
//   - An unknown CLDR category name — [corei18n.InvalidForm].
//   - A counted message missing a category its language can produce —
//     [TranslationIncomplete].
//
// Every one of them is a startup failure. That is the whole design: at startup
// a human is present, the catalogue file is in front of them, and the fix is
// an edit. At render time the caller is a request, and the only repairs
// available are to show something wrong or to show nothing.
func NewStore(fallback corei18n.TagValue, catalogues map[corei18n.TagValue]Catalogue) (store *Store, err error) {
	//: a fallback is mandatory and is never guessed.
	if fallback.IsZero() {
		//: ADR 0031: refuse where any SDK-chosen value would be arbitrary.
		return nil, errs.Wrap(CatalogInvalid, errs.WrapParams{}, errs.String("detail", "the fallback tag is unset"))
	}
	//: the fallback must be a language the store can actually answer in.
	if _, ok := catalogues[fallback]; !ok {
		//: otherwise every miss returns the key and looks like a translation
		//: gap rather than a wiring fault.
		return nil, errs.Wrap(CatalogInvalid, errs.WrapParams{},
			errs.String("tag", fallback.String()), errs.String("detail", "the fallback language has no catalogue"))
	}
	//: compile every language, refusing at the first defect.
	compiled, err := compileCatalogues(catalogues)
	//: a catalogue defect stops construction.
	if err != nil {
		//: already carries the tag, the key and the detail.
		return nil, err
	}
	//: the immutable store.
	return &Store{fallback: fallback, byTag: compiled, tags: sortedTags(compiled)}, nil
}

// compileCatalogues compiles every language's entries, in a deterministic
// order so the FIRST defect reported for a given input is always the same one.
func compileCatalogues(catalogues map[corei18n.TagValue]Catalogue) (compiled map[corei18n.TagValue]map[corei18n.Key]corei18n.MessageValue, err error) {
	//: one compiled map per language.
	compiled = make(map[corei18n.TagValue]map[corei18n.Key]corei18n.MessageValue, len(catalogues))
	//: fix the order before compiling — Go map iteration is randomised, and a
	//: build that reports a different one of two defects each run is a build
	//: nobody can bisect.
	for _, tag := range sortedTagKeys(catalogues) {
		//: the language's rules decide which categories are required.
		messages, langErr := compileCatalogue(tag, catalogues[tag])
		//: a defect in any language refuses the whole store.
		if langErr != nil {
			//: already carries the tag and the key.
			return nil, langErr
		}
		//: store it.
		compiled[tag] = messages
	}
	//: every language compiled.
	return compiled, nil
}

// compileCatalogue compiles one language's entries.
func compileCatalogue(tag corei18n.TagValue, catalogue Catalogue) (messages map[corei18n.Key]corei18n.MessageValue, err error) {
	//: a language with no reviewed rule is refused before anything is
	//: compiled, so the message a maintainer reads names the language rather
	//: than a key inside it.
	rules, err := requireRules(tag)
	//: UnsupportedLanguage.
	if err != nil {
		//: refuse.
		return nil, err
	}
	//: one compiled message per key.
	messages = make(map[corei18n.Key]corei18n.MessageValue, len(catalogue))
	//: deterministic order, for the same reason compileCatalogues fixes one.
	for _, key := range slices.Sorted(maps.Keys(catalogue)) {
		//: compile and check this entry.
		message, entryErr := compileEntry(tag, key, catalogue[key], rules)
		//: a defect refuses the store.
		if entryErr != nil {
			//: already carries the tag, the key and the detail.
			return nil, entryErr
		}
		//: store it.
		messages[key] = message
	}
	//: the language's compiled catalogue.
	return messages, nil
}

// compileEntry validates a key, compiles its patterns and — for a counted
// entry — checks it against the language's own categories.
func compileEntry(tag corei18n.TagValue, key corei18n.Key, entry EntryValue, rules PluralValue) (message corei18n.MessageValue, err error) {
	//: the key is shown on screen when a translation is missing, so it is
	//: validated even though nothing here reads it as a path or a name.
	if keyErr := corei18n.ValidateKey(key); keyErr != nil {
		//: InvalidKey, with the tag added so the file is identifiable.
		return corei18n.MessageValue{}, errs.Wrap(keyErr, errs.WrapParams{}, errs.String("tag", tag.String()))
	}
	//: an uncounted entry is one pattern and no completeness question.
	if entry.Forms == nil {
		//: compile it.
		return compilePlain(tag, key, entry.Pattern)
	}
	//: a counted entry declares categories, so it is compiled and checked.
	return compileCounted(tag, key, entry.Forms, rules)
}

// compilePlain compiles an uncounted entry.
func compilePlain(tag corei18n.TagValue, key corei18n.Key, text string) (message corei18n.MessageValue, err error) {
	//: parse the placeholders once, here.
	message, err = corei18n.NewMessage(text)
	//: a malformed pattern is a catalogue defect.
	if err != nil {
		//: add the coordinates a maintainer needs to find the line.
		return corei18n.MessageValue{}, errs.Wrap(err, errs.WrapParams{},
			errs.String("tag", tag.String()), errs.String("key", string(key)))
	}
	//: compiled.
	return message, nil
}

// compileCounted compiles a counted entry and checks it against the
// categories the language's rules can produce.
func compileCounted(tag corei18n.TagValue, key corei18n.Key, forms map[string]string, rules PluralValue) (message corei18n.MessageValue, err error) {
	//: translate the CLDR names into Forms, refusing an unknown one.
	byForm, err := parseForms(tag, key, forms)
	//: InvalidForm.
	if err != nil {
		//: refuse.
		return corei18n.MessageValue{}, err
	}
	//: compile every declared category.
	message, err = corei18n.NewPluralMessage(byForm)
	//: a missing `other`, or a malformed pattern.
	if err != nil {
		//: add the coordinates.
		return corei18n.MessageValue{}, errs.Wrap(err, errs.WrapParams{},
			errs.String("tag", tag.String()), errs.String("key", string(key)))
	}
	//: THE check: every category this language can produce must be present.
	if missingErr := requireComplete(tag, key, message, rules); missingErr != nil {
		//: TranslationIncomplete, naming the form.
		return corei18n.MessageValue{}, missingErr
	}
	//: compiled and complete.
	return message, nil
}

// parseForms turns the CLDR category NAMES a catalogue file carries into
// [corei18n.Form] values.
func parseForms(tag corei18n.TagValue, key corei18n.Key, forms map[string]string) (byForm map[corei18n.Form]string, err error) {
	//: one entry per declared category.
	byForm = make(map[corei18n.Form]string, len(forms))
	//: the key is reported in a field when a category name is refused;
	//: converting once keeps the loop free of a per-iteration conversion.
	keyText := string(key)
	//: deterministic order so a file with two bad names always reports the
	//: same one first.
	for _, name := range slices.Sorted(maps.Keys(forms)) {
		//: an unknown category name is refused, never dropped — a dropped
		//: "otehr" leaves the message with no catch-all at all.
		form, formErr := corei18n.ParseForm(name)
		//: InvalidForm.
		if formErr != nil {
			//: add the coordinates.
			return nil, errs.Wrap(formErr, errs.WrapParams{},
				errs.String("tag", tag.String()), errs.String("key", keyText))
		}
		//: keep the pattern under its category.
		byForm[form] = forms[name]
	}
	//: the translated categories.
	return byForm, nil
}

// requireComplete reports [TranslationIncomplete] for the first category the
// language's rules can produce and the message does not carry.
func requireComplete(tag corei18n.TagValue, key corei18n.Key, message corei18n.MessageValue, rules PluralValue) error {
	//: converted once, outside the loop that may report it.
	keyText := string(key)
	//: the categories this language's rule can return.
	for _, form := range rules.Forms() {
		//: each must have a pattern, or the message is a sentence the rule
		//: can ask for and the catalogue cannot answer.
		if !message.HasForm(form) {
			//: name the language, the key and the missing category — the
			//: three things needed to fix the file.
			return errs.Wrap(TranslationIncomplete, errs.WrapParams{},
				errs.String("tag", tag.String()), errs.String("key", keyText), errs.String("form", form.String()))
		}
	}
	//: complete for its language.
	return nil
}

// sortedTagKeys returns a catalogue map's languages in canonical order.
func sortedTagKeys(catalogues map[corei18n.TagValue]Catalogue) []corei18n.TagValue {
	//: TagValue is a struct, so it orders by its canonical spelling.
	return slices.SortedFunc(maps.Keys(catalogues), compareTags)
}

// sortedTags returns the compiled map's languages in canonical order.
func sortedTags(compiled map[corei18n.TagValue]map[corei18n.Key]corei18n.MessageValue) []corei18n.TagValue {
	//: the same order, so Tags() is stable across runs.
	return slices.SortedFunc(maps.Keys(compiled), compareTags)
}

// compareTags orders two tags by their canonical spelling.
func compareTags(a, b corei18n.TagValue) int {
	//: the canonical form is what a reader sees in an error message.
	return strings.Compare(a.String(), b.String())
}

// Lookup returns the message registered for exactly (tag, key). It performs no
// fallback and no plural selection — see [corei18n.Catalog].
func (s *Store) Lookup(tag corei18n.TagValue, key corei18n.Key) (message corei18n.MessageValue, ok bool) {
	//: the language's compiled map, if the store holds one.
	messages, known := s.byTag[tag]
	//: an unknown language is a miss, not an error: the renderer's chain
	//: decides what to do next.
	if !known {
		//: miss.
		return corei18n.MessageValue{}, false
	}
	//: the exact key.
	message, ok = messages[key]
	//: hit or miss.
	return message, ok
}

// Tags returns every language the store answers for, sorted, as a copy.
//
// A copy because the internal slice is read by every renderer in the process
// and a caller that sorted it in place would reorder them all. It is a startup
// path — a renderer resolves a tag once, not per request — so the allocation
// is paid where it is not measured.
func (s *Store) Tags() []corei18n.TagValue {
	//: clone out.
	return slices.Clone(s.tags)
}

// Keys returns the keys held for tag, sorted, as a copy, or nil when the store
// does not hold the language at all.
//
// It is the [corei18n.KeyLister] sibling, and its purpose is a test rather
// than a request: compare a language's keys against the fallback's and fail
// the build when one is missing. See [Store.Missing], which does exactly that.
func (s *Store) Keys(tag corei18n.TagValue) []corei18n.Key {
	//: an unknown language has no keys, and nil says so more clearly than an
	//: empty slice, which reads as "translated, with nothing in it".
	messages, known := s.byTag[tag]
	//: unknown.
	if !known {
		//: nil.
		return nil
	}
	//: sorted, and the caller's own.
	return slices.Sorted(maps.Keys(messages))
}

// Fallback returns the language a key is looked for in when the requested one
// does not hold it. It is the [corei18n.Fallbacker] sibling and is never the
// zero tag.
func (s *Store) Fallback() corei18n.TagValue {
	//: fixed at construction.
	return s.fallback
}

// Missing returns the keys the fallback language holds and tag does not,
// sorted.
//
// This is the SDK's answer to "which strings were never translated", and it is
// deliberately a QUERY rather than a render-time behaviour. At render time a
// missing translation has already happened and something has to be shown; in a
// test it is a build that does not go out:
//
//	for _, tag := range store.Tags() {
//		if gaps := store.Missing(tag); len(gaps) != 0 {
//			t.Errorf("%s is missing %d keys: %v", tag, len(gaps), gaps)
//		}
//	}
//
// It also answers the question the renderer deliberately does not: a render
// that fell back does not report which language answered, so a caller who
// needs Content-Language to be exact proves the gap is empty here instead.
func (s *Store) Missing(tag corei18n.TagValue) []corei18n.Key {
	//: the reference set is the fallback's, since that is what a miss
	//: actually resolves to.
	reference := s.byTag[s.fallback]
	//: the language under test.
	messages := s.byTag[tag]
	//: collect what is absent; nil when nothing is.
	var gaps []corei18n.Key
	//: every key the fallback can answer.
	for key := range reference {
		//: present in the language under test?
		if _, ok := messages[key]; !ok {
			//: no — a gap.
			gaps = append(gaps, key)
		}
	}
	//: canonical order, so a failing test prints the same list every run.
	slices.Sort(gaps)
	//: the gaps.
	return gaps
}
