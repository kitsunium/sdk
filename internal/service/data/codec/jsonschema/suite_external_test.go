package jsonschema_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/jsonschema"
)

// minSuitePassed is the floor of documents the suite must pass: the count at
// the vendored commit, so a file deleted or a group wrongly skipped shows.
const minSuitePassed = 1024

// suiteRoot is the vendored JSON-Schema-Test-Suite (MIT, see its README.md).
const suiteRoot = "testdata/JSON-Schema-Test-Suite"

// suiteGroup is one group of a suite file: a schema and the documents it is
// tested on.
type suiteGroup struct {
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	Tests       []struct {
		Description string          `json:"description"`
		Data        json.RawMessage `json:"data"`
		Valid       bool            `json:"valid"`
	} `json:"tests"`
}

// suiteRemotes registers the suite's remotes at the URIs it serves them on.
func suiteRemotes(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	root := filepath.Join(suiteRoot, "remotes")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		doc, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, path)
		out["http://localhost:1234/"+filepath.ToSlash(rel)] = doc
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// suiteSkips are the groups #882 leaves out, each with its reason: the
// keywords refused by name, and the groups that need the 2020-12
// meta-schema, which uses $dynamicRef.
var suiteSkips = map[string]string{
	"unevaluatedItems":      "the unevaluated vocabulary is refused by name (KEYWORD_REFUSED): #882 compiles core, applicator and validation",
	"unevaluatedProperties": "the unevaluated vocabulary is refused by name (KEYWORD_REFUSED): #882 compiles core, applicator and validation",
	"dynamicRef":            "$dynamicRef and $dynamicAnchor are refused by name (KEYWORD_REFUSED), out of #882's scope",
	"content":               "the content vocabulary is refused by name (KEYWORD_REFUSED), out of #882's scope",
	"vocabulary":            "$vocabulary and custom meta-schemas are refused by name (KEYWORD_REFUSED)",
	"defs/validate definition against metaschema":                            "needs the 2020-12 meta-schema, which uses $dynamicRef",
	"ref/remote ref, containing refs itself":                                 "needs the 2020-12 meta-schema, which uses $dynamicRef",
	"not/collect annotations inside a 'not', even if collection is disabled": "uses unevaluatedProperties",
	"ref/ref creates new scope when adjacent to keywords":                    "uses unevaluatedProperties",
}

// skipReason returns why a group is skipped, or "".
func skipReason(file, group string) string {
	//: the whole file, then the group.
	if r, ok := suiteSkips[file]; ok {
		return r
	}
	return suiteSkips[file+"/"+group]
}

// TestJSONSchemaTestSuite runs the official suite's required 2020-12 tests:
// every group compiles, and every document is valid or not as the suite says.
func TestJSONSchemaTestSuite(t *testing.T) {
	t.Parallel()
	remotes := suiteRemotes(t)
	files, err := filepath.Glob(filepath.Join(suiteRoot, "tests", "draft2020-12", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no suite files: %v", err)
	}
	sort.Strings(files)
	passed, skipped := 0, 0
	for _, path := range files {
		file := strings.TrimSuffix(filepath.Base(path), ".json")
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatal(rerr)
		}
		var groups []suiteGroup
		if uerr := json.Unmarshal(raw, &groups); uerr != nil {
			t.Fatalf("%s: %v", file, uerr)
		}
		for _, g := range groups {
			if reason := skipReason(file, g.Description); reason != "" {
				skipped += len(g.Tests)
				continue
			}
			v, cerr := jsonschema.NewValidator(g.Schema, jsonschema.Config{Resources: remotes})
			if cerr != nil {
				t.Errorf("%s / %s: NewValidator: %v", file, g.Description, cerr)
				continue
			}
			for _, tc := range g.Tests {
				report, verr := v.Validate(tc.Data, 1<<20)
				if verr != nil {
					t.Errorf("%s / %s / %s: Validate: %v", file, g.Description, tc.Description, verr)
					continue
				}
				if report.OK() != tc.Valid {
					t.Errorf("%s / %s / %s: valid = %v, want %v (%v)", file, g.Description, tc.Description, report.OK(), tc.Valid, report.Paths())
					continue
				}
				passed++
			}
		}
	}
	t.Logf("suite: %d documents passed, %d skipped with a named reason", passed, skipped)
	if passed < minSuitePassed {
		t.Fatalf("only %d suite documents ran, want at least %d: a file went missing", passed, minSuitePassed)
	}
}
