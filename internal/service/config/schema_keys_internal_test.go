// Package config — the key grammar and its resolution against a target type.
package config

import (
	"net"
	"reflect"
	"slices"
	"testing"
	"time"
)

// TestSplitKeyRefusesAnUnnamedLevel pins the grammar. Every refused spelling
// would produce a nesting level with no name — something the merged map cannot
// represent and an operator cannot type.
func TestSplitKeyRefusesAnUnnamedLevel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key      string
		wantOK   bool
		wantSegs []string
	}{
		{key: "port", wantOK: true, wantSegs: []string{"port"}},
		{key: "database.max_conns", wantOK: true, wantSegs: []string{"database", "max_conns"}},
		{key: "a.b.c", wantOK: true, wantSegs: []string{"a", "b", "c"}},
		{key: "", wantOK: false},
		{key: ".", wantOK: false},
		{key: ".port", wantOK: false},
		{key: "port.", wantOK: false},
		{key: "a..b", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			segments, ok := splitKey(tc.key)
			if ok != tc.wantOK {
				t.Fatalf("splitKey(%q) ok = %v, want %v", tc.key, ok, tc.wantOK)
			}
			if ok && !slices.Equal(segments, tc.wantSegs) {
				t.Errorf("splitKey(%q) = %v, want %v", tc.key, segments, tc.wantSegs)
			}
		})
	}
}

// TestNestKeyRefusesAKeyThatIsBothAValueAndATable pins the one structural
// contradiction two declarations can produce. Resolving it by guessing would
// mean picking which of two declared defaults the author meant.
func TestNestKeyRefusesAKeyThatIsBothAValueAndATable(t *testing.T) {
	t.Parallel()
	//: a scalar where a level is needed.
	scalarFirst := map[string]any{}
	if ok := nestKey(scalarFirst, []string{"database"}, 1); !ok {
		t.Fatal("nestKey refused a plain key")
	}
	if ok := nestKey(scalarFirst, []string{"database", "host"}, "x"); ok {
		t.Error("nestKey descended through a scalar")
	}

	//: a level where a scalar is being written.
	tableFirst := map[string]any{}
	if ok := nestKey(tableFirst, []string{"database", "host"}, "x"); !ok {
		t.Fatal("nestKey refused a nested key")
	}
	if ok := nestKey(tableFirst, []string{"database"}, 1); ok {
		t.Error("nestKey overwrote a table with a scalar")
	}

	//: two keys under one table share the level.
	shared := map[string]any{}
	if ok := nestKey(shared, []string{"db", "host"}, "h"); !ok {
		t.Fatal("nestKey refused the first key under a table")
	}
	if ok := nestKey(shared, []string{"db", "port"}, 1); !ok {
		t.Fatal("nestKey refused a sibling under an existing table")
	}
	level, isTable := shared["db"].(map[string]any)
	if !isTable || len(level) != 2 {
		t.Errorf("db = %#v, want a table with two members", shared["db"])
	}
}

// leafBearing exercises every shape the key walk has to classify.
type leafBearing struct {
	//: a plain scalar, named by its json tag.
	Port int `json:"port"`
	//: json:"-" removes the field from every document, so nothing addresses it.
	Hidden string `json:"-"`
	//: an option-only tag leaves the Go name in force.
	OptionOnly string `json:",omitempty"`
	//: unexported fields never decode.
	unexported string //nolint:unused // the walk must skip it
	//: a nested table contributes its own key AND its members'.
	Nested nestedTable `json:"nested"`
	//: a type that decodes ITSELF is a leaf however many fields it has.
	At time.Time `json:"at"`
	//: so is one that decodes from text.
	Addr net.IP `json:"addr"`
	//: a map has no statically-named members.
	Labels map[string]string `json:"labels"`
	//: neither does a slice.
	Hosts []string `json:"hosts"`
	//: a pointer to a struct addresses the same members as the struct.
	Optional *nestedTable `json:"optional"`
	//: an embedded field with no json name is INLINED by encoding/json.
	embeddedInline
	//: an embedded field WITH a name is an ordinary nested table.
	EmbeddedNamed embeddedInline `json:"named"`
}

// plainNaming carries the untagged case alone. A field with no json tag keeps
// its Go name, which is the only name it has — and the rule that says so has
// nothing to do with the tagged fields around it, so it is pinned on a type
// that declares no tag at all.
type plainNaming struct {
	//: an untagged field keeps its Go name.
	Untagged string
	//: and so does a second one, at the same level.
	AlsoUntagged int
}

// nestedTable is the ordinary nested case.
type nestedTable struct {
	Host string `json:"host"`
}

// embeddedInline is embedded both ways in leafBearing.
type embeddedInline struct {
	Inlined string `json:"inlined"`
}

// namedEmbedding embeds an UNEXPORTED struct type under a json name.
// encoding/json addresses it as a level of its own and populates it, which is
// measured rather than assumed — the reflect field is not exported, so an
// IsExported gate alone would miss both this level and the inlined one above.
type namedEmbedding struct {
	embeddedInline `json:"emb"`
}

// TestCollectKeysSpeaksTheOperatorsVocabulary pins which dotted keys a schema
// may declare. Each classification is a fact about encoding/json — the decoder
// the loader actually runs — not a convention this package invented.
func TestCollectKeysSpeaksTheOperatorsVocabulary(t *testing.T) {
	t.Parallel()
	keys := collectKeys(reflect.TypeFor[leafBearing]())

	want := []string{
		"port", "OptionOnly",
		"nested", "nested.host",
		"at", "addr", "labels", "hosts",
		"optional", "optional.host",
		//: the embedded members appear at the PARENT level…
		"inlined",
		//: …while the named embedding is an ordinary table.
		"named", "named.inlined",
	}
	for _, key := range want {
		if _, ok := keys[key]; !ok {
			t.Errorf("key %q is not addressable, but encoding/json decodes it", key)
		}
	}

	notWant := []string{
		//: removed from every document.
		"Hidden",
		//: a self-decoding type receives raw bytes; its members are unreachable.
		"at.wall", "at.Location", "addr.IP",
		//: a map key is not part of this grammar.
		"labels.env",
		//: an element position is not either.
		"hosts.0",
		//: the embedded field itself is not a level.
		"embeddedInline", "embeddedInline.inlined",
		//: unexported fields never decode.
		"unexported",
	}
	for _, key := range notWant {
		if _, ok := keys[key]; ok {
			t.Errorf("key %q is addressable, but nothing decodes into it", key)
		}
	}
}

// TestAnUntaggedFieldKeepsItsGoName pins the fallback: encoding/json addresses
// a field with no tag by its Go name, so a schema declaring a default for it
// must spell it the same way — capital and all.
func TestAnUntaggedFieldKeepsItsGoName(t *testing.T) {
	t.Parallel()
	keys := collectKeys(reflect.TypeFor[plainNaming]())

	for _, key := range []string{"Untagged", "AlsoUntagged"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("key %q is not addressable, but encoding/json decodes it", key)
		}
	}
	if _, ok := keys["untagged"]; ok {
		t.Error("the lower-cased name is addressable, but encoding/json does not decode it")
	}
}

// TestAKeyIsClassifiedLeafOrTable pins the distinction the unknown-key pass
// rests on. A table's members are keys and the walk descends into them; a
// leaf owns everything below it, so descending would report a legitimate
// document's contents as a typo.
func TestAKeyIsClassifiedLeafOrTable(t *testing.T) {
	t.Parallel()
	keys := collectKeys(reflect.TypeFor[leafBearing]())

	cases := []struct {
		key  string
		want keyKind
	}{
		{key: "port", want: keyLeaf},
		{key: "nested", want: keyTable},
		{key: "optional", want: keyTable},
		{key: "named", want: keyTable},
		//: a type that decodes itself receives raw bytes.
		{key: "at", want: keyLeaf},
		//: so does one that decodes from text.
		{key: "addr", want: keyLeaf},
		//: a map has no statically-named members.
		{key: "labels", want: keyLeaf},
		//: neither does a slice.
		{key: "hosts", want: keyLeaf},
	}
	for _, tc := range cases {
		got, ok := keys[tc.key]
		if !ok {
			t.Errorf("key %q is not addressable at all", tc.key)
			continue
		}
		if got != tc.want {
			t.Errorf("kind of %q = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// TestAnUnexportedEmbeddingIsStillAddressable pins the encoding/json rule an
// IsExported gate gets wrong: an anonymous field of unexported STRUCT type
// still decodes, inlined when it has no json name and as its own level when it
// has one.
func TestAnUnexportedEmbeddingIsStillAddressable(t *testing.T) {
	t.Parallel()
	keys := collectKeys(reflect.TypeFor[namedEmbedding]())
	for _, key := range []string{"emb", "emb.inlined"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("key %q is not addressable, but encoding/json decodes it", key)
		}
	}
	if _, ok := keys["inlined"]; ok {
		t.Error("a NAMED embedding was also inlined; it is one level, not two")
	}
}

// recursiveTable reaches itself, so the walk must terminate rather than
// enumerate an unbounded key set.
type recursiveTable struct {
	Name  string          `json:"name"`
	Child *recursiveTable `json:"child"`
}

// diamondTable reaches the same type twice on two different branches, which is
// not a cycle and must be walked on both.
type diamondTable struct {
	Left  nestedTable `json:"left"`
	Right nestedTable `json:"right"`
}

// TestCollectKeysTerminatesOnRecursionAndStillWalksADiamond pins the two cases
// a naive visited-set conflates: a type on the CURRENT path is a cycle, while a
// type seen on a sibling branch is an ordinary repeat.
func TestCollectKeysTerminatesOnRecursionAndStillWalksADiamond(t *testing.T) {
	t.Parallel()
	//: terminates, and the first level is still addressable.
	recursive := collectKeys(reflect.TypeFor[recursiveTable]())
	for _, key := range []string{"name", "child"} {
		if _, ok := recursive[key]; !ok {
			t.Errorf("key %q is not addressable on a recursive type", key)
		}
	}
	if _, ok := recursive["child.child"]; ok {
		t.Error("the walk descended into a cycle; the key set is unbounded")
	}

	//: both branches of a diamond are walked.
	diamond := collectKeys(reflect.TypeFor[diamondTable]())
	for _, key := range []string{"left.host", "right.host"} {
		if _, ok := diamond[key]; !ok {
			t.Errorf("key %q is not addressable; a repeat was mistaken for a cycle", key)
		}
	}
}

// TestDecodesItselfIsAPropertyNotAList pins the rule that makes time.Time a
// leaf: it is the presence of the unmarshaler, checked on the pointer receiver
// where the methods live, rather than a hard-coded set of known type names.
func TestDecodesItselfIsAPropertyNotAList(t *testing.T) {
	t.Parallel()
	if !decodesItself(reflect.TypeFor[time.Time]()) {
		t.Error("time.Time is walked into; encoding/json never reaches its members")
	}
	if !decodesItself(reflect.TypeFor[net.IP]()) {
		t.Error("net.IP is walked into; it decodes from text")
	}
	if decodesItself(reflect.TypeFor[nestedTable]()) {
		t.Error("a plain struct was treated as a leaf")
	}
}
