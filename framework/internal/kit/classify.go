package kit

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
)

// Classification (ADR 0006). A field says what it holds with its kit tag,
//
//	kit:"[class][,option]..."
//
// and kit keeps what the class promises: it redacts wherever it shows a
// value — the Studio, the spans, the logs, the terminal — and, for what a
// store keeps, it files each record under its subject, erases and deletes on
// the store's retention, exports and erases on a person's request.
//
// A type is read once: its rules (which members are classified, reached
// how, for a value walk that follows recursion) and its members (each
// classified member as a JSON pointer, for the model, the register and the
// modules). Declaring never fails: what a tag gets wrong is a problem the
// start reports, all at once, at the declaration that uses the type.

// The words of a kit tag besides the classes (model.Class constants).
const (
	optSubject   = "subject"
	optModerated = "moderated"
	optPlain     = "plain"
	optErased    = "erased"
	optHistory   = "history"
	// historyLimit bounds history=N (ADR 0007).
	historyLimit int = 100
)

// The marks a kit tag may write.
const (
	// markSubject is "subject": the field identifies the person the record
	// is about.
	markSubject tagMarks = 1 << iota
	// markModerated is "moderated": the field is one a moderation reads.
	markModerated
	// markPlain is "plain": the field is kept plain, never sealed at rest.
	markPlain
	// markErased is "erased": kit stamps the field with the instant of the
	// record's erasure.
	markErased
)

// tagMarks are the words of a kit tag besides the class, as bits.
type tagMarks uint8

// markWords are the words of a kit tag that set a mark.
var markWords = map[string]tagMarks{optSubject: markSubject, optModerated: markModerated, optPlain: markPlain, optErased: markErased}

// fieldTag is what one field's kit tag says.
type fieldTag struct {
	// raw is the tag as written, for the messages.
	raw string
	// class is the class written, "" when none.
	class string
	// marks are the words besides the class the tag writes.
	marks tagMarks
	// history is how many former values the field keeps.
	history int
	// problems are what the tag gets wrong, said once the field's name is
	// known.
	problems []func(field string) phrase
}

// isClass reports whether word is one of the four classes.
func isClass(word string) bool {
	switch word {
	case model.ClassPublic, model.ClassPersonal, model.ClassSpecial, model.ClassSecret:
		return true
	}
	return false
}

// parseTag reads a kit tag. An empty word is skipped, and every word is
// read: a tag with several mistakes reports each.
func parseTag(raw string) fieldTag {
	t := fieldTag{raw: raw}
	for word := range strings.SplitSeq(raw, ",") {
		t.read(strings.TrimSpace(word))
	}
	t.checkOptions()
	return t
}

// bad records what the tag gets wrong, said once the field's name is known.
func (t *fieldTag) bad(p func(field string) phrase) { t.problems = append(t.problems, p) }

// read takes one word of the tag.
func (t *fieldTag) read(word string) {
	switch mark, isMark := markWords[word]; {
	case word == "":
	case isClass(word):
		t.readClass(word)
	case isMark:
		t.marks |= mark
	case word == optHistory || strings.HasPrefix(word, optHistory+"="):
		t.readHistory(word)
	default:
		raw := t.raw
		t.bad(func(field string) phrase {
			return say("classify.unknown", "field", field, "tag", raw, "word", word)
		})
	}
}

// readClass takes a class: a field holds one kind of data.
func (t *fieldTag) readClass(word string) {
	if first := t.class; first != "" {
		raw := t.raw
		t.bad(func(field string) phrase {
			return say("classify.two-classes", "field", field, "tag", raw, "first", first, "second", word)
		})
		return
	}
	t.class = word
}

// readHistory takes history=N, N from 1 to historyLimit.
func (t *fieldTag) readHistory(word string) {
	n, err := strconv.Atoi(strings.TrimPrefix(word, optHistory+"="))
	if err != nil || n < 1 || n > historyLimit {
		raw := t.raw
		t.bad(func(field string) phrase {
			return say("classify.history", "field", field, "tag", raw, "max", historyLimit)
		})
		return
	}
	t.history = n
}

// checkOptions refuses the options the class contradicts.
func (t *fieldTag) checkOptions() {
	raw, class := t.raw, t.class
	if t.marks.has(markSubject) && (class == model.ClassPublic || class == model.ClassSecret) {
		t.bad(func(field string) phrase {
			return say("classify.subject-class", "field", field, "tag", raw, "class", class)
		})
	}
	if t.marks.has(markModerated) && class == model.ClassSecret {
		t.bad(func(field string) phrase { return say("classify.secret-moderated", "field", field, "tag", raw) })
	}
	if t.marks.has(markPlain) && class == model.ClassSpecial {
		t.bad(func(field string) phrase { return say("classify.special-plain", "field", field, "tag", raw) })
	}
}

// effective is the class the field holds: the one written, or personal for
// a subject that names none.
func (t *fieldTag) effective() string {
	if t.class == "" && t.marks.has(markSubject) {
		return model.ClassPersonal
	}
	return t.class
}

// sensitive reports whether kit clears the field on an erasure and never
// shows it: personal, special, secret, or the subject.
func (t *fieldTag) sensitive() bool {
	switch t.effective() {
	case model.ClassPersonal, model.ClassSpecial, model.ClassSecret:
		return true
	}
	return false
}

// personal reports whether the field holds data about a person: personal,
// special, or the subject — a secret is a credential.
func (t *fieldTag) personal() bool {
	c := t.effective()
	return c == model.ClassPersonal || c == model.ClassSpecial
}

// any reports whether the tag says anything.
func (t *fieldTag) any() bool {
	return t.class != "" || t.marks.has(markSubject) || t.marks.has(markModerated) || t.marks.has(markPlain) || t.marks.has(markErased) || t.history > 0 || len(t.problems) > 0
}

// sealedAtRest reports whether kit keeps the field sealed where it keeps it
// at rest (seal.go): a personal, special or secret member, or the subject,
// unless it is plain — which special refuses.
func (t *fieldTag) sealedAtRest() bool {
	return t.sensitive() && (!t.marks.has(markPlain) || t.class == model.ClassSpecial)
}

// redactedByTag reports whether a kit tag makes its field redacted wherever
// kit shows it: a sensitive class or the subject option among its words,
// whatever else the tag says — the redactor's comma rule, so that
// kit:"other,personal" redacts even though the start refuses "other".
func redactedByTag(raw string) bool {
	for word := range strings.SplitSeq(raw, ",") {
		switch strings.TrimSpace(word) {
		case model.ClassPersonal, model.ClassSpecial, model.ClassSecret, optSubject:
			return true
		}
	}
	return false
}

// classify fills a schema field with what its kit tag says. sealed says the
// schema is of what kit keeps sealed at rest — a store's entity on disk, a
// message in a queue on disk —: a field is marked sealed only there.
func classify(field *model.Field, tag reflect.StructTag, sealed bool) {
	t := parseTag(tag.Get("kit"))
	field.Class = t.effective()
	field.Subject = t.marks.has(markSubject)
	field.Moderated = t.marks.has(markModerated)
	field.Sealed = sealed && t.sealedAtRest()
	field.Erased = t.marks.has(markErased)
	field.History = t.history
}

// has reports whether m holds the mark.
func (m tagMarks) has(mark tagMarks) bool { return m&mark != 0 }
