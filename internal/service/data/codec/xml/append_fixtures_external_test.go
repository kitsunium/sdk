package xml_test

import "strings"

// appendItem is an element with attributes, text and children.
type appendItem struct {
	ID    string   `xml:"id,attr"`
	Name  string   `xml:"name"`
	Notes []string `xml:"note"`
	Text  string   `xml:",chardata"`
}

// appendFixtures are the values whose Append must write Marshal's bytes.
func appendFixtures() map[string]any {
	return map[string]any{
		"element":      appendItem{ID: "1", Name: "a<b>&", Notes: []string{"x", "y"}, Text: "t"},
		"pointer":      &appendItem{ID: "2"},
		"one kibibyte": kibValue(),
	}
}

// appendFailure is a value encoding/xml refuses after it has started: a
// channel member.
func appendFailure() any {
	return struct {
		Big string
		Bad chan int
	}{Big: strings.Repeat("x", 64<<10), Bad: make(chan int)}
}

// kibValue is an element of about 1 KiB of XML.
func kibValue() *appendItem {
	notes := make([]string, 0, 60)
	for range 60 {
		notes = append(notes, "a note")
	}
	return &appendItem{ID: "k", Name: "kib", Notes: notes}
}
