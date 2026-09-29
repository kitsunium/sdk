package kit

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

type schemaNote struct {
	Text string `json:"text" validate:"required"`
}

type schemaItem struct {
	ID      string            `json:"id"`
	When    time.Time         `json:"when"`
	Took    time.Duration     `json:"took"`
	Raw     json.RawMessage   `json:"raw"`
	Tags    []string          `json:"tags"`
	Labels  []string          `json:"labels,omitempty"`
	Counts  map[string]int    `json:"counts"`
	Note    *schemaNote       `json:"note"`
	Later   *schemaNote       `json:"later,omitzero"`
	Next    *schemaItem       `json:"next,omitempty"`
	Headers map[string]string `json:"-"`
}

type schemaRequest struct {
	ID    string `path:"id"`
	Page  int    `query:"page"`
	Token string `header:"X-Token"`
	Body  string `json:"body" validate:"max=10"`
}

// A schema says what encoding/json writes: the members and their names,
// which may be left out and which may be null.
func TestSchemasSayWhatIsWritten(t *testing.T) {
	s := schemaOf(reflect.TypeFor[schemaItem]())
	if s.Type != "object" || s.Name != "github.com/kitsunium/sdk/framework/internal/kit.schemaItem" {
		t.Fatalf("schema %+v", s)
	}
	fields := map[string]model.Field{}
	var names []string
	for _, f := range s.Fields {
		fields[f.Name] = f
		names = append(names, f.Name)
	}
	if want := []string{"id", "when", "took", "raw", "tags", "labels", "counts", "note", "later", "next"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("members %v, want %v", names, want)
	}
	type schemaWant struct {
		typ, format, name  string
		optional, nullable bool
	}
	for _, tc := range []struct {
		name string
		want schemaWant
	}{
		{"when", schemaWant{"string", "date-time", "", false, false}},
		{"took", schemaWant{"integer", "duration-ns", "", false, false}},
		{"raw", schemaWant{"any", "", "", false, false}},
		{"tags", schemaWant{"array", "", "", false, true}},
		{"labels", schemaWant{"array", "", "", true, false}},
		{"counts", schemaWant{"map", "", "", false, true}},
		{"note", schemaWant{"object", "", "github.com/kitsunium/sdk/framework/internal/kit.schemaNote", false, true}},
		{"later", schemaWant{"object", "", "github.com/kitsunium/sdk/framework/internal/kit.schemaNote", true, false}},
		{"next", schemaWant{"object", "", "github.com/kitsunium/sdk/framework/internal/kit.schemaItem", true, false}},
	} {
		name, want := tc.name, tc.want
		f := fields[name]
		if f.Type.Type != want.typ || f.Type.Format != want.format || f.Type.Name != want.name || f.Optional != want.optional || f.Type.Nullable != want.nullable {
			t.Errorf("%s: %+v optional=%v, want %+v", name, *f.Type, f.Optional, want)
		}
	}
	if next := fields["next"].Type; next.Ref != "github.com/kitsunium/sdk/framework/internal/kit.schemaItem" || len(next.Fields) != 0 {
		t.Errorf("a type reaching itself is a reference: %+v", next)
	}
	if note := fields["note"].Type; len(note.Fields) != 1 || note.Fields[0].Rules != "required" {
		t.Errorf("the note's rules %+v", note.Fields)
	}

	r := requestSchemaOf(reflect.TypeFor[schemaRequest]())
	var got []string
	for _, f := range r.Fields {
		got = append(got, f.In+":"+f.Name)
	}
	if want := []string{"path:id", "query:page", "header:X-Token", "body:body"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("request fields %v, want %v", got, want)
	}
	if id := r.Fields[0]; id.Optional || id.Type.Type != "string" {
		t.Errorf("the path parameter %+v", id)
	}
	if page := r.Fields[1]; !page.Optional || page.Type.Type != "integer" {
		t.Errorf("the query parameter %+v", page)
	}
	if body := r.Fields[3]; body.Rules != "max=10" {
		t.Errorf("the body field %+v", body)
	}
}
