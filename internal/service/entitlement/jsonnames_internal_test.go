package entitlement

import "testing"

// Test_checkNoDuplicateNames pins the scan at every depth a roster reaches, and
// pins the negative rows just as hard: the same name at DIFFERENT depths is
// ordinary JSON, and a scanner that refused it would refuse every roster, since
// "exp" appears at the top level and inside every subject.
func Test_checkNoDuplicateNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		want   string
		found  bool
		reason string
	}{
		{
			name:   "no duplicate at all",
			input:  `{"iat":"a","exp":"b","subjects":{"u1":{"fp":"x","exp":"c"}}}`,
			found:  false,
			reason: "the ordinary shape of a roster",
		},
		{
			name:   "the same name at different depths is legal",
			input:  `{"exp":"top","subjects":{"u1":{"fp":"x","exp":"inner"}}}`,
			found:  false,
			reason: "exp is both a roster field and a subject field; refusing this would refuse every roster",
		},
		{
			name:   "duplicate at the root",
			input:  `{"iat":"a","iat":"b"}`,
			want:   "iat",
			found:  true,
			reason: "two readers disagree on the issuing instant of a correctly signed document",
		},
		{
			name:   "duplicate one level down",
			input:  `{"subjects":{"u1":{"fp":"x"},"u1":{"fp":"y"}}}`,
			want:   "u1",
			found:  true,
			reason: "one entry authorising a key and a second at the same uuid is the equivocation that matters",
		},
		{
			name:   "duplicate two levels down",
			input:  `{"subjects":{"u1":{"fp":"x","fp":"y"}}}`,
			want:   "fp",
			found:  true,
			reason: "a top-level scan never sees this, which is why the scan is recursive",
		},
		{
			name:   "duplicate inside an array element",
			input:  `{"keys":[{"kid":"a","kid":"b"}]}`,
			want:   "kid",
			found:  true,
			reason: "the JWKS shape: kid selects the key a token is verified with",
		},
		{
			name:   "duplicate of an array-valued member",
			input:  `{"keys":[{"kid":"a"}],"keys":[{"kid":"b"}]}`,
			want:   "keys",
			found:  true,
			reason: "the second array replaces the first, so a substituted key set wins silently",
		},
		{
			name:   "duplicate after a nested object closed",
			input:  `{"a":{"x":1},"b":2,"a":{"y":3}}`,
			want:   "a",
			found:  true,
			reason: "the frame must be restored on the closing brace, or the scan loses its place",
		},
		{
			name:   "duplicate after a nested array closed",
			input:  `{"a":[1,2],"b":3,"a":[4]}`,
			want:   "a",
			found:  true,
			reason: "same restoration, through an array",
		},
		{
			name:   "a value that looks like a name is not one",
			input:  `{"a":"b","c":"a"}`,
			found:  false,
			reason: "a string in value position must never enter the name set",
		},
		{
			name:   "names repeated across sibling objects are legal",
			input:  `{"subjects":{"u1":{"fp":"x"},"u2":{"fp":"y"}}}`,
			found:  false,
			reason: "every subject carries fp; the sets must be per-object",
		},
		{
			name:   "malformed input is not this function's verdict",
			input:  `{"a":`,
			found:  false,
			reason: "json.Unmarshal reports the syntax error; two verdicts on one input contradict each other",
		},
		{
			name:   "an empty object",
			input:  `{}`,
			found:  false,
			reason: "nothing to compare",
		},
		{
			name:   "a top-level array of objects",
			input:  `[{"a":1},{"a":2}]`,
			found:  false,
			reason: "two sibling objects each naming a once is legal",
		},
		{
			name:   "a duplicate deep inside a top-level array",
			input:  `[{"a":1,"a":2}]`,
			want:   "a",
			found:  true,
			reason: "the top level is not a frame, and the scan must still work under it",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, found := checkNoDuplicateNames([]byte(tt.input))
			if found != tt.found {
				t.Fatalf("checkNoDuplicateNames(%s) found = %v, want %v (%s)", tt.input, found, tt.found, tt.reason)
			}
			if found && got != tt.want {
				t.Errorf("checkNoDuplicateNames(%s) = %q, want %q (%s)", tt.input, got, tt.want, tt.reason)
			}
			//: A verdict of "clean" must carry no name, so a caller that reads
			//: only the string cannot act on a stale one.
			if !found && got != "" {
				t.Errorf("checkNoDuplicateNames(%s) = %q with found=false, want no name (%s)", tt.input, got, tt.reason)
			}
		})
	}
}
