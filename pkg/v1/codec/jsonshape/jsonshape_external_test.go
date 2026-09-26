// Package jsonshape_test — the facade as a consumer meets it.
package jsonshape_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/codec/jsonshape"
)

// Audit is embedded through a pointer: its fields may be missing.
type Audit struct {
	Created time.Time `json:"created"`
}

// Order is what an API documents.
type Order struct {
	ID    int64    `json:"id,string"`
	Lines []string `json:"lines,omitempty" validate:"max=10"`
	*Audit
}

// TestTheShortestUsefulDescription describes a type the way an API
// documentation page would, through the public names only.
func TestTheShortestUsefulDescription(t *testing.T) {
	t.Parallel()
	shape := jsonshape.For[Order]()
	//: an object of three members.
	if shape.Kind != jsonshape.Object || len(shape.Fields) != 3 {
		t.Fatalf("Order = %v with %d fields", shape.Kind, len(shape.Fields))
	}
	id, lines, created := shape.Fields[0], shape.Fields[1], shape.Fields[2]
	//: an int64 written as a quoted string.
	if id.Name != "id" || id.Shape.Kind != jsonshape.Integer || id.Shape.Format != "int64" || !id.Quoted {
		t.Errorf("id = %+v", id)
	}
	//: an optional array of strings, with its rules.
	if lines.Shape.Kind != jsonshape.Array || lines.Shape.Items.Kind != jsonshape.String || !lines.Optional || lines.Rules != "max=10" {
		t.Errorf("lines = %+v", lines)
	}
	//: a date-time promoted through a pointer, so optional.
	if created.Name != "created" || created.Shape.Format != "date-time" || !created.Optional {
		t.Errorf("created = %+v", created)
	}
	//: the Go field behind it, through the embedding.
	if reflect.TypeFor[Order]().FieldByIndex(created.Index).Name != "Created" {
		t.Errorf("created.Index = %v does not reach Audit.Created", created.Index)
	}
}

// TestOfAndForAgree pins the two entry points, and the facade's kind names.
func TestOfAndForAgree(t *testing.T) {
	t.Parallel()
	byType, err := json.Marshal(jsonshape.Of(reflect.TypeFor[Order]()))
	//: encodable.
	if err != nil {
		t.Fatal(err)
	}
	byParam, err := json.Marshal(jsonshape.For[Order]())
	//: encodable.
	if err != nil {
		t.Fatal(err)
	}
	//: the same description.
	if string(byType) != string(byParam) {
		t.Errorf("Of = %s, For = %s", byType, byParam)
	}
	//: kinds read as their names.
	if jsonshape.Unsupported.String() != "unsupported" || jsonshape.Any.String() != "any" {
		t.Error("kind names changed")
	}
}
