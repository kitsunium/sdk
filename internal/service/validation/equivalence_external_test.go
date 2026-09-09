package validation_test

import (
	"testing"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
	svcvalidation "github.com/kitsunium/sdk/internal/service/validation"
)

// TestTagAndCodePathsAgree pins the property that makes the two front ends one
// domain: the same rule, expressed either way, produces the same violation —
// same path, same rule name, same code. Without it, moving a rule from a tag
// into code would silently change what a client sees.
func TestTagAndCodePathsAgree(t *testing.T) {
	t.Parallel()
	tagged := svcvalidation.Must(svcvalidation.Struct[address](svcvalidation.StructConfig{}))
	coded := svcvalidation.Must(svcvalidation.Field("zip",
		func(a address) string { return a.Zip },
		svcvalidation.Required[string](),
		svcvalidation.Must(svcvalidation.Length(4, svcvalidation.Unbounded)),
	))
	value := address{Zip: "x"}
	fromTag := tagged(corevalidation.RootPath, value)
	fromCode := coded(corevalidation.RootPath, value)
	if len(fromTag) != 1 || len(fromCode) != 1 {
		t.Fatalf("both paths must report one violation, got %v and %v", fromTag, fromCode)
	}
	if fromTag[0].Path != fromCode[0].Path {
		t.Errorf("path: tag %q vs code %q", fromTag[0].Path, fromCode[0].Path)
	}
	if fromTag[0].Rule != fromCode[0].Rule {
		t.Errorf("rule: tag %q vs code %q", fromTag[0].Rule, fromCode[0].Rule)
	}
	if fromTag[0].Code != fromCode[0].Code {
		t.Errorf("code: tag %v vs code %v", fromTag[0].Code, fromCode[0].Code)
	}
}

// TestTagsAndCodeComposeIntoOneReport is the reason Struct returns a
// Constraint rather than a bespoke validator type: a cross-field rule that no
// tag can express lives beside the tags, in one report.
func TestTagsAndCodeComposeIntoOneReport(t *testing.T) {
	t.Parallel()
	tags := svcvalidation.Must(svcvalidation.Struct[user](svcvalidation.StructConfig{}))
	//: a cross-field rule reports at the ROOT path — no single field is at fault.
	crossField := func(path string, value user) corevalidation.ReportValue {
		//: an aged user with no name is inconsistent, and no single field is
		//: individually wrong — which is exactly what RootPath is for.
		if value.Age > 0 && value.Name == "" {
			//: located at the value as a whole.
			return corevalidation.ReportValue{{
				Path: path, Rule: "consistency", Message: "an aged user must be named",
			}}
		}
		//: consistent.
		return nil
	}
	report := svcvalidation.All(tags, corevalidation.Constraint[user](crossField))(
		corevalidation.RootPath, user{Age: 36})
	rules := map[string]bool{}
	for _, violation := range report {
		rules[violation.Rule] = true
	}
	if !rules["required"] || !rules["consistency"] {
		t.Errorf("both front ends must appear in one report, got %v", report)
	}
}
