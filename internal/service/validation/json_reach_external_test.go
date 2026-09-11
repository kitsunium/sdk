// Package validation_test — a rule on a field no JSON key reaches.
package validation_test

import (
	"encoding/json"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcvalidation "github.com/kitsunium/sdk/internal/service/validation"
)

// ZipA and ZipB each promote a "zip" key, tagged, at the same depth.
type (
	ZipA struct {
		Zip string `json:"zip" validate:"required"`
	}
	ZipB struct {
		Zip string `json:"zip" validate:"required"`
	}
)

// PlainZip promotes a "zip" key and carries no rule.
type PlainZip struct {
	Zip string `json:"zip"`
}

// TaggedKey and UntaggedKey both promote "Key" at one depth: one by its tag,
// one by its Go name. The pair with the rule on the other side carries none.
type (
	TaggedKey struct {
		Other string `json:"Key" validate:"required"`
	}
	UntaggedKey struct {
		Key string `validate:"required"`
	}
	PlainTaggedKey struct {
		Other string `json:"Key"`
	}
	PlainUntaggedKey struct {
		Key string
	}
)

// The composites, one per resolution encoding/json makes.
type (
	// withAmbiguousZip: two tagged candidates at one depth — "zip" decodes
	// into neither. The embeddings are pointers, which encoding/json follows
	// as it follows a value and go vet's duplicate-tag check does not.
	withAmbiguousZip struct {
		*ZipA `validate:"dive"`
		*ZipB `validate:"dive"`
	}
	// withHiddenZip: the struct's own zip is shallower and takes the key.
	withHiddenZip struct {
		Common `validate:"dive"`
		Zip    string `json:"zip"`
	}
	// withRuleOnTheShallowerField: the shallower field carries the rule.
	withRuleOnTheShallowerField struct {
		PlainZip
		Zip string `json:"zip" validate:"required"`
	}
	// withRuleOnTheTaggedWinner: the tagged candidate wins a tie and has the rule.
	withRuleOnTheTaggedWinner struct {
		TaggedKey `validate:"dive"`
		PlainUntaggedKey
	}
	// withRuleOnTheUntaggedLoser: the untagged candidate loses the tie and has the rule.
	withRuleOnTheUntaggedLoser struct {
		PlainTaggedKey
		UntaggedKey `validate:"dive"`
	}
	// withTwoDirectKeys: nothing embedded, and two fields answering to "Zip"
	// — one whose tag names nothing, so it answers by its Go name, and one
	// named by its tag, which wins.
	withTwoDirectKeys struct {
		Zip      string `json:",omitempty" validate:"required"`
		Postcode string `json:"Zip"`
	}
)

// reachCase is one type, the one-key object an operator would write for it,
// and what encoding/json leaves in the field that carries the rule.
type reachCase struct {
	name        string
	input       string
	key         string
	compile     func() error
	constrained func(tb testing.TB, input string) string
}

// TestARuleIsRefusedExactlyWhenNoJSONKeyReachesItsField pins the refusal of a
// rule on a field encoding/json never decodes a key into — the promoted field
// a shallower one of the same name hides, and the two a tie at one depth makes
// ambiguous, which JSON decodes into neither and says nothing. Such a rule
// judges a value no input can set and reports it at a path whose key belongs
// to another field: in the ambiguous case two violations at the same "zip",
// so an operator can neither satisfy them nor tell which failed.
//
// The oracle is encoding/json itself, not the resolution the compiler mirrors:
// each row decodes its input and asserts the compiler refuses the type exactly
// when the rule's field came back empty.
//
// Seen failing both ways: with the reach check removed from compileStruct the
// four unreached rows compiled, and with the tagged tie-break removed from the
// resolution the tagged side of a tie was refused although encoding/json
// decodes the key into it. The direct-fields row is the one that would pass
// if the shortcut for types that embed nothing missed a repeated key.
func TestARuleIsRefusedExactlyWhenNoJSONKeyReachesItsField(t *testing.T) {
	t.Parallel()
	tests := []reachCase{
		{
			name: "two tagged embeddings promote the same key", input: `{"zip":"set"}`, key: "zip",
			compile: compileOf[withAmbiguousZip],
			constrained: func(tb testing.TB, input string) string {
				//: a key JSON drops never allocates the embedding it would fill.
				if zip := decoded[withAmbiguousZip](tb, input).ZipA; zip != nil {
					return zip.Zip
				}
				return ""
			},
		},
		{
			name: "a shallower field hides the promoted one", input: `{"zip":"set"}`, key: "zip",
			compile: compileOf[withHiddenZip],
			constrained: func(tb testing.TB, input string) string {
				return decoded[withHiddenZip](tb, input).Common.Zip
			},
		},
		{
			name: "the untagged side of a tie", input: `{"Key":"set"}`, key: "Key",
			compile: compileOf[withRuleOnTheUntaggedLoser],
			constrained: func(tb testing.TB, input string) string {
				return decoded[withRuleOnTheUntaggedLoser](tb, input).Key
			},
		},
		{
			name: "the tagged side of a tie", input: `{"Key":"set"}`, key: "Key",
			compile: compileOf[withRuleOnTheTaggedWinner],
			constrained: func(tb testing.TB, input string) string {
				return decoded[withRuleOnTheTaggedWinner](tb, input).Other
			},
		},
		{
			name: "the shallower field of two", input: `{"zip":"set"}`, key: "zip",
			compile: compileOf[withRuleOnTheShallowerField],
			constrained: func(tb testing.TB, input string) string {
				return decoded[withRuleOnTheShallowerField](tb, input).Zip
			},
		},
		{
			name: "the untagged one of two direct fields", input: `{"Zip":"set"}`, key: "Zip",
			compile: compileOf[withTwoDirectKeys],
			constrained: func(tb testing.TB, input string) string {
				return decoded[withTwoDirectKeys](tb, input).Zip
			},
		},
		{
			name: "a lone promoted member", input: `{"zip":"set"}`, key: "zip",
			compile: compileOf[withEmbedded],
			constrained: func(tb testing.TB, input string) string {
				return decoded[withEmbedded](tb, input).Zip
			},
		},
	}
	runCase := func(t *testing.T, c reachCase) {
		t.Helper()
		reached := c.constrained(t, c.input) == "set"
		err := c.compile()
		if reached {
			if err != nil {
				t.Fatalf("refused (%v), yet encoding/json decodes %q into the field with the rule", err, c.key)
			}
			return
		}
		if !errs.HasCode(err, svcvalidation.CodeInvalidRule) {
			t.Fatalf("err = %v, want INVALID_RULE: encoding/json never decodes %q into the field with the rule", err, c.key)
		}
		if field := renderFields(err)["field"]; field != c.key {
			t.Errorf("field = %q, want the key no input can reach, %q", field, c.key)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// decoded is what encoding/json makes of input as a T.
func decoded[T any](tb testing.TB, input string) T {
	tb.Helper()
	var value T
	if err := json.Unmarshal([]byte(input), &value); err != nil {
		tb.Fatalf("json.Unmarshal(%s): %v", input, err)
	}
	return value
}
