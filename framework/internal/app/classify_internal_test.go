package app

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

// Every class and every option parses, alone and combined; a subject is
// personal unless it is special.
func TestTheKitTagParses(t *testing.T) {
	for _, c := range []struct {
		tag                                      string
		class                                    string
		subject, moderated, plain, erased, sense bool
		history                                  int
	}{
		{tag: ""},
		{tag: "public", class: "public"},
		{tag: "personal", class: "personal", sense: true},
		{tag: "special", class: "special", sense: true},
		{tag: "secret", class: "secret", sense: true},
		{tag: "subject", class: "personal", subject: true, sense: true},
		{tag: "special,subject", class: "special", subject: true, sense: true},
		{tag: "personal,moderated", class: "personal", moderated: true, sense: true},
		{tag: "public,moderated", class: "public", moderated: true},
		{tag: "moderated", moderated: true},
		{tag: "personal,plain", class: "personal", plain: true, sense: true},
		{tag: "erased", erased: true},
		{tag: "subject,history=3", class: "personal", subject: true, history: 3, sense: true},
		{tag: " personal , moderated ,", class: "personal", moderated: true, sense: true},
		{tag: "history=100", history: 100},
	} {
		got := parseTag(c.tag)
		if len(got.problems) > 0 {
			t.Errorf("%q: %d problems", c.tag, len(got.problems))
		}
		if got.effective() != c.class || got.marks.has(markSubject) != c.subject || got.marks.has(markModerated) != c.moderated || got.marks.has(markPlain) != c.plain ||
			got.marks.has(markErased) != c.erased || got.history != c.history || got.sensitive() != c.sense {
			t.Errorf("%q = %+v, want %+v", c.tag, got, c)
		}
	}
}

// Every refusal is said, with the field it concerns.
func TestTheKitTagRefuses(t *testing.T) {
	for tag, want := range map[string]string{
		"privat":             `unknown word "privat"`,
		"personal,secret":    "two classes, personal and secret",
		"special,plain":      "never plain",
		"secret,moderated":   "nobody moderates it",
		"public,subject":     "cannot be public",
		"secret,subject":     "cannot be secret",
		"history=0":          "from 1 to 100",
		"history=101":        "from 1 to 100",
		"history":            "from 1 to 100",
		"personal,history=x": "from 1 to 100",
	} {
		got := parseTag(tag)
		if len(got.problems) != 1 {
			t.Errorf("%q: %d problems", tag, len(got.problems))
			continue
		}
		if said := got.problems[0]("x.T.F").String(); !strings.Contains(said, want) || !strings.Contains(said, "x.T.F") {
			t.Errorf("%q says %q, want %q", tag, said, want)
		}
	}
	// Several mistakes in one tag are all said.
	if got := parseTag("privat,personal,secret,history=0"); len(got.problems) != 3 {
		t.Errorf("three mistakes, %d problems", len(got.problems))
	}
}

type classifiedAddress struct {
	Street string `json:"street" app:"personal"`
	City   string `json:"city"`
}

type classifiedComment struct {
	Body    string              `json:"body" app:"personal,moderated"`
	Replies []classifiedComment `json:"replies,omitempty"`
}

type classifiedEmbedded struct {
	Phone string `json:"phone" app:"personal"`
}

type classifiedEntity struct {
	ID      string                       `json:"id"`
	Owner   string                       `json:"owner" app:"subject"`
	Home    classifiedAddress            `json:"home"`
	Work    *classifiedAddress           `json:"work,omitempty"`
	Tags    map[string]classifiedAddress `json:"tags,omitempty"`
	Thread  []classifiedComment          `json:"thread,omitempty"`
	Secret  string                       `json:"secret" app:"secret"`
	Erased  *time.Time                   `json:"erased,omitempty" app:"erased"`
	Kept    int                          `json:"kept"`
	Created time.Time                    `json:"created" app:"public"`
	*classifiedEmbedded
}

// The members of a type: a class applies wherever the field sits — in a
// nested struct, a pointer, every element of a slice, every value of a map,
// an embedded struct —, and a recursive type is listed down to its
// recursion.
func TestTheMembersOfAType(t *testing.T) {
	p := planOf(reflect.TypeFor[classifiedEntity]())
	if len(p.problems) != 0 {
		t.Fatalf("problems: %v", p.problems)
	}
	var pointers []string
	for _, m := range p.members {
		pointers = append(pointers, m.pointer)
	}
	want := []string{"/owner", "/home/street", "/work/street", "/tags/*/street", "/thread/*/body", "/secret", "/erased", "/created", "/phone"}
	if !slices.Equal(pointers, want) {
		t.Errorf("members %v, want %v", pointers, want)
	}
	if p.subjectPointer() != "/owner" {
		t.Errorf("subject %q", p.subjectPointer())
	}
}

// classifiedValue is an entity whose classified members hold a value at
// every depth.
func classifiedValue() classifiedEntity {
	return classifiedEntity{
		ID: "e1", Owner: "ann", Home: classifiedAddress{Street: "1 rue", City: "Paris"}, Work: &classifiedAddress{Street: "2 rue", City: "Lyon"},
		Tags:   map[string]classifiedAddress{"x": {Street: "3 rue", City: "Nice"}},
		Thread: []classifiedComment{{Body: "hi", Replies: []classifiedComment{{Body: "deep", Replies: []classifiedComment{{Body: "deeper"}}}}}},
		Secret: "s", Kept: 7, classifiedEmbedded: &classifiedEmbedded{Phone: "0600"},
	}
}

// An erasure's walk reaches every classified member of a value, as deep as
// the value goes, stamps the erased field and keeps the rest.
func TestAnErasureReachesEveryMember(t *testing.T) {
	p := planOf(reflect.TypeFor[classifiedEntity]())
	v := classifiedValue()
	rv := reflect.ValueOf(&v).Elem()
	if got, _ := p.subjectOf(rv); got != "ann" || p.cleared(rv) {
		t.Fatalf("subjectOf = %q, cleared %v", got, p.cleared(rv))
	}
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	p.clearSensitive(rv)
	p.stampErased(rv, stamp)
	replies := v.Thread[0].Replies
	if left := v.Owner + v.Home.Street + v.Work.Street + v.Tags["x"].Street + v.Secret + v.Phone + v.Thread[0].Body + replies[0].Body + replies[0].Replies[0].Body; left != "" {
		t.Errorf("personal data survived: %+v", v)
	}
	if kept := fmt.Sprint(v.Home.City, "|", v.Work.City, "|", v.Tags["x"].City, "|", v.Kept, "|", v.ID); kept != "Paris|Lyon|Nice|7|e1" {
		t.Errorf("what is kept was lost: %s", kept)
	}
	if v.Erased == nil || !v.Erased.Equal(stamp) || !p.cleared(rv) {
		t.Errorf("erased stamp %v, cleared %v", v.Erased, p.cleared(rv))
	}
}

// One member is cleared by its pointer, in every element.
func TestAMemberIsClearedByItsPointer(t *testing.T) {
	p := planOf(reflect.TypeFor[classifiedEntity]())
	w := classifiedEntity{Tags: map[string]classifiedAddress{"a": {Street: "x", City: "y"}, "b": {Street: "z"}}}
	clearPath(reflect.ValueOf(&w).Elem(), p.member("/tags/*/street").path)
	if got := [3]string{w.Tags["a"].Street, w.Tags["b"].Street, w.Tags["a"].City}; got != [3]string{"", "", "y"} {
		t.Errorf("clearPath: %+v", w.Tags)
	}
}

type twoSubjects struct {
	A string `json:"a" app:"subject"`
	B string `json:"b" app:"subject"`
}

type listedSubject struct {
	People []struct {
		Email string `json:"email" app:"subject"`
	} `json:"people"`
}

type badSubject struct {
	Who  []string `json:"who" app:"subject"`
	When string   `json:"when" app:"erased"`
}

// listedHistory keeps the former values of every element of a list: an
// element has no identity from one version to the next (ADR 0007).
type listedHistory struct {
	Replies []struct {
		Body string `json:"body" app:"personal,history=2"`
	} `json:"replies"`
}

// The type's own refusals: two subjects, a subject in a list — where the
// type rests —, a subject that is not an identity, an erased field that is
// not a time, former values kept inside a list.
func TestATypeIsJudgedWhole(t *testing.T) {
	for typ, want := range map[reflect.Type]struct{ everywhere, resting []string }{
		reflect.TypeFor[twoSubjects]():   {everywhere: []string{"two subject fields"}},
		reflect.TypeFor[listedSubject](): {resting: []string{"inside a list or a map"}},
		reflect.TypeFor[badSubject]():    {everywhere: []string{"holds a []string", "the framework stamps a time.Time"}},
		reflect.TypeFor[listedHistory](): {everywhere: []string{"sits inside a list or a map"}},
	} {
		p := planOf(typ)
		saysAll(t, typ, "everywhere", p.problems, want.everywhere)
		saysAll(t, typ, "at rest", p.resting, want.resting)
		if p.subject != nil {
			t.Errorf("%s has a subject the framework can use", typ)
		}
	}
}

// saysAll fails t unless problems say each of want, in order, and nothing
// else.
func saysAll(t *testing.T, typ reflect.Type, where string, problems []phrase, want []string) {
	t.Helper()
	if len(problems) != len(want) {
		t.Errorf("%s %s: %v", typ, where, problems)
		return
	}
	for i, w := range want {
		if !strings.Contains(problems[i].String(), w) {
			t.Errorf("%s says %q %s, want %q", typ, problems[i], where, w)
		}
	}
}

// listedPerson is a record about one person, as a store keeps it.
type listedPerson struct {
	Email string `json:"email" app:"subject"`
	Name  string `json:"name" app:"personal"`
}

// listedPage is what a list answers: the records of several people, and
// the one person who asked.
type listedPage struct {
	Asker  string         `json:"asker" app:"subject"`
	People []listedPerson `json:"people"`
	Others []listedPerson `json:"others"`
}

// subjectManyAt returns the diagnostics of an app with one service
// declared by declare that refuse a subject in a list — or count one as a
// type's second subject.
func subjectManyAt(declare func(*Service)) []string {
	svc := NewService("listing", "")
	declare(svc)
	var said []string
	for _, d := range NewApp("listing", svc).classificationProblems(false) {
		if strings.Contains(d.Message, "inside a list or a map") || strings.Contains(d.Message, "two subject fields") {
			said = append(said, d.Node+": "+d.Message)
		}
	}
	return said
}

// A subject in a list is refused only where kit reads one person's
// identity from it — a store's entity, a topic's message, a queued
// command's input: the types that rest and are sealed. Where kit reads no
// identity — an endpoint's request and response, a query's input and
// result, a synchronous command's input and result, an auth handler's
// credentials and data — it is personal data for redaction, and accepted
// (ADR 0178, C5).
func TestASubjectInAListIsRefusedOnlyWhereKitReadsAnIdentity(t *testing.T) {
	answer := func(context.Context, EmptyValue) (listedPage, error) { return listedPage{}, nil }
	take := func(context.Context, listedPage) (EmptyValue, error) { return EmptyValue{}, nil }
	accepted := map[string]func(*Service){
		"endpoint response": func(s *Service) { s.Endpoint("GET /people", answer) },
		"endpoint request":  func(s *Service) { s.Endpoint("POST /people", take) },
		"query result":      func(s *Service) { s.Query("people", answer) },
		"query input":       func(s *Service) { s.Query("asked", take) },
		"command result":    func(s *Service) { s.Command("list", answer) },
		"command input":     func(s *Service) { s.Command("import", take) },
		"auth handler": func(s *Service) {
			s.AuthHandler("pages", func(context.Context, listedPage) (UID, listedPage, error) { return "", listedPage{}, nil })
		},
	}
	for where, declare := range accepted {
		if said := subjectManyAt(declare); len(said) != 0 {
			t.Errorf("%s: refused %q", where, said)
		}
	}
	refused := map[string]struct {
		declare func(*Service)
		node    string
	}{
		"store entity":         {func(s *Service) { s.Store("pages", func(p listedPage) string { return p.Asker }) }, "listing/store/pages"},
		"topic message":        {func(s *Service) { s.Topic[listedPage]("pages") }, "listing/topic/pages"},
		"queued command input": {func(s *Service) { s.Command("import", take, Queued()) }, "listing/command/import"},
	}
	for where, c := range refused {
		said := subjectManyAt(c.declare)
		if len(said) != 2 || !strings.HasPrefix(said[0], c.node+": ") {
			t.Errorf("%s: said %q, want both lists refused at %s", where, said, c.node)
		}
	}
	// A type a store keeps and an endpoint answers is refused at the store,
	// once per field, whichever declaration comes first.
	both := subjectManyAt(func(s *Service) {
		s.Endpoint("GET /pages", func(context.Context, EmptyValue) (listedPage, error) { return listedPage{}, nil })
		s.Store("pages", func(p listedPage) string { return p.Asker })
	})
	if len(both) != 2 || !strings.HasPrefix(both[0], "listing/store/pages: ") {
		t.Errorf("a type both kept and answered: said %q", both)
	}
}

type redactedByClass struct {
	Name     string `json:"name" app:"personal"`
	Health   string `json:"health" app:"special"`
	Who      string `json:"who" app:"subject"`
	Other    string `json:"other" app:"other,personal"`
	Category string `json:"category" app:"public"`
	Plain    string `json:"plain"`
}

// Redaction reads the classes, by the redactor's comma rule: an unknown
// word beside a class still redacts.
func TestRedactionReadsTheClasses(t *testing.T) {
	raw, _ := redactValue(redactedByClass{Name: "n1", Health: "h1", Who: "w1", Other: "o1", Category: "c1", Plain: "p1"}, payloadLimit)
	s := string(raw)
	for _, leaked := range []string{"n1", "h1", "w1", "o1"} {
		if strings.Contains(s, leaked) {
			t.Errorf("%s leaked: %s", leaked, s)
		}
	}
	for _, kept := range []string{"c1", "p1"} {
		if !strings.Contains(s, kept) {
			t.Errorf("%s was redacted: %s", kept, s)
		}
	}
}

// The model carries each field's class and options.
func TestTheSchemaCarriesTheClasses(t *testing.T) {
	s := schemaOf(reflect.TypeFor[classifiedEntity]())
	byName := map[string]model.Field{}
	for _, f := range s.Fields {
		byName[f.Name] = f
	}
	if f := byName["owner"]; f.Class != model.ClassPersonal || !f.Subject {
		t.Errorf("owner: %+v", f)
	}
	if f := byName["erased"]; !f.Erased {
		t.Errorf("erased: %+v", f)
	}
	if f := byName["kept"]; f.Class != "" {
		t.Errorf("kept: %+v", f)
	}
	if f := byName["thread"].Type.Items.Fields[0]; f.Class != model.ClassPersonal || !f.Moderated {
		t.Errorf("thread's body: %+v", f)
	}
}
