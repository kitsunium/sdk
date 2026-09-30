package kit

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

// remembering has former values at every kind of place (ADR 0007).
type remembering struct {
	ID    string `json:"id"`
	Nick  string `json:"nick" kit:"public,history=2"`
	Note  string `json:"note" kit:"history=2"`
	Token string `json:"token" kit:"history=2"`
	Card  struct {
		Holder string `json:"holder" kit:"personal"`
		Brand  string `json:"brand"`
	} `json:"card" kit:"history=2"`
	Creds struct {
		Hash string `json:"hash" kit:"secret,history=2"`
		Hint string `json:"hint" kit:"history=2"`
	} `json:"creds" kit:"personal"`
}

// A former value takes the stricter of its field's class and the classes of
// the fields it is in: a secret inside a personal struct stays secret.
func TestAFormerValueTakesTheStricterClass(t *testing.T) {
	p := planOf(reflect.TypeFor[remembering]())
	for pointer, want := range map[string]string{
		"/nick":        model.ClassPublic,
		"/note":        "",
		"/creds/hash":  model.ClassSecret,
		"/creds/hint":  model.ClassPersonal,
		"/gone":        model.ClassSecret,
		"/card/holder": model.ClassPersonal,
	} {
		if got := p.classOf(pointer); got != want {
			t.Errorf("%s is %q, want %q", pointer, got, want)
		}
	}
}

// The Studio shows a former value as the data browser shows its field: a
// public or unclassified one as it was — its own classified members and a
// name the redactor knows redacted —, a personal one never, a secret's not
// at all.
func TestAFormerValueIsShownAsItsField(t *testing.T) {
	p := planOf(reflect.TypeFor[remembering]())
	for _, c := range []struct{ pointer, value, want string }{
		{"/nick", `"annie"`, `"annie"`},
		{"/note", `"kept"`, `"kept"`},
		{"/token", `"t0k3n"`, `"[redacted]"`},
		{"/card", `{"holder":"Ann","brand":"visa"}`, `{"holder":"[redacted]","brand":"visa"}`},
		{"/creds/hint", `"first pet"`, `"[redacted]"`},
		{"/creds/hash", `"$pbkdf2$…"`, ``},
		{"/gone", `"x"`, ``},
	} {
		if got := string(p.shownFormer(c.pointer, json.RawMessage(c.value))); got != c.want {
			t.Errorf("%s shows %s, want %s", c.pointer, got, c.want)
		}
	}
}

// A value is pushed at the head of its field's former values; a head equal
// to it — left by a write that did not stand — goes first, and a zero value
// is no former value.
func TestAValueIsPushedOnce(t *testing.T) {
	at := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	entry := func(v string) formerEntry { return formerEntry{Value: json.RawMessage(v), Until: at} }
	values := func(list []formerEntry) []string {
		var out []string
		for _, e := range list {
			out = append(out, string(e.Value))
		}
		return out
	}
	// A store that seals nothing keeps its former values as they are.
	h := &historied[struct{}]{}
	pushed := func(list []formerEntry, was memberValue) []formerEntry {
		out, err := h.pushed(t.Context(), formerAt{key: "k", pointer: "/x"}, list, was, formerEntry{Until: at})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	list := []formerEntry{entry(`"b"`), entry(`"a"`)}
	if got := values(pushed(list, memberValue{raw: json.RawMessage(`"c"`)})); !slices.Equal(got, []string{`"c"`, `"b"`, `"a"`}) {
		t.Errorf("pushed: %v", got)
	}
	if got := values(pushed(list, memberValue{raw: json.RawMessage(`"b"`)})); !slices.Equal(got, []string{`"b"`, `"a"`}) {
		t.Errorf("a duplicate head: %v", got)
	}
	if got := values(pushed(list, memberValue{raw: json.RawMessage(`""`), zero: true})); !slices.Equal(got, []string{`"b"`, `"a"`}) {
		t.Errorf("a zero value: %v", got)
	}
	before := map[string]memberValue{"/a": {raw: json.RawMessage(`1`)}, "/b": {raw: json.RawMessage(`2`)}, "/c": {raw: json.RawMessage(`3`)}}
	after := map[string]memberValue{"/a": {raw: json.RawMessage(`9`)}, "/b": {raw: json.RawMessage(`2`)}, "/c": {raw: json.RawMessage(`9`)}}
	if got := changedFields(before, after, "/c"); !slices.Equal(got, []string{"/a"}) {
		t.Errorf("changed: %v", got)
	}
}
