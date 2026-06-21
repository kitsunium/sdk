package crypto

import (
	"testing"
)

func Test_phcID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		phc    string
		wantID string
		wantOK bool
	}{
		{"well-formed PHC yields its id", "$pbkdf2-sha256$i=1$s$d", "pbkdf2-sha256", true},
		{"id with an empty trailing field still parses", "$id$", "id", true},
		{"no leading dollar is not a PHC", "pbkdf2-sha256$x", "", false},
		{"empty id segment is rejected", "$$rest", "", false},
		{"a bare dollar is rejected", "$", "", false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			id, ok := phcID(c.phc)
			//: id + ok must both match the expected parse outcome.
			if id != c.wantID || ok != c.wantOK {
				t.Errorf("phcID(%q)=(%q,%v) want (%q,%v)", c.phc, id, ok, c.wantID, c.wantOK)
			}
		})
	}
}
