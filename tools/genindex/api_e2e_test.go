// Package main — -write-api and -check-api end to end, through the go
// command, on the fixture repository under testdata/apirepo: the documents,
// the doc edits a check catches, and every case the pin markers and the
// digests report, each by its name.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	// fixtureRepo is the fixture repository, relative to this package.
	fixtureRepo string = "testdata/apirepo"
	// fixtureModule is its module's path.
	fixtureModule string = "example.com/fixture"
	// fixtureDesign is the design file its generated pins name.
	fixtureDesign string = "design/lock.yaml"
)

// needFixture skips a test that reads the fixture repository where it is not
// staged: Bazel stages this package's data alone, and testdata/apirepo is not
// among it (gazelle excludes testdata). A go command on the sandbox's PATH
// says nothing about the fixture — GitHub's ubuntu runner has one there, a
// Mac whose go comes from Homebrew has none — so this, not needGo, keeps the
// end-to-end tests out of `bazel test`. `go test` runs them — CI's test-386
// job and e2e-cross over every module of the census, tools/genindex
// included, and any local run (rule 12).
func needFixture(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(fixtureRepo); err != nil {
		t.Skipf("needs the fixture repository (%s): `go test` in tools/genindex runs this test", fixtureRepo)
	}
}

// needGo skips a test that runs the go command where there is none on the
// PATH. `go test` runs these, as needFixture says.
func needGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command on PATH: `go test` in tools/genindex runs this test")
	}
}

// fixtureCopy copies the fixture repository into a fresh directory, writes
// its docs/api, and returns the copy's root and cells.
func fixtureCopy(t *testing.T) (root string, cells []platform) {
	t.Helper()
	needFixture(t)
	needGo(t)
	root = t.TempDir()
	if err := os.CopyFS(root, os.DirFS(fixtureRepo)); err != nil {
		t.Fatalf("copying the fixture: %v", err)
	}
	cells, err := readPlatforms(filepath.Join(root, "scripts", "ci", "platforms.sh"))
	if err != nil {
		t.Fatalf("the fixture's table: %v", err)
	}
	var out bytes.Buffer
	if status := runWriteAPI(apiOptions{root: root, cells: cells}, &out); status != 0 {
		t.Fatalf("-write-api = %d:\n%s", status, out.String())
	}
	return root, cells
}

// checkFixture runs -check-api over a copy and returns its status and report.
func checkFixture(t *testing.T, root string, cells []platform, markers, digests bool) (int, string) {
	t.Helper()
	var out bytes.Buffer
	status := runCheckAPI(apiOptions{root: root, cells: cells, markers: markers, digests: digests}, &out)
	return status, out.String()
}

// readFixtureDoc reads the copy's document.
func readFixtureDoc(t *testing.T, root string) apiDocument {
	t.Helper()
	raw, err := os.ReadFile(documentPath(root, "fixture"))
	if err != nil {
		t.Fatalf("reading the document: %v", err)
	}
	var doc apiDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding the document: %v", err)
	}
	return doc
}

// edit replaces old by new in a file of the copy, which must hold it once.
// The file is read with LF line endings: e2e-cross runs this suite on a
// Windows checkout, where git may have written the fixture with CRLF.
func edit(t *testing.T, root, file, old, new string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(file))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.Count(text, old) != 1 {
		t.Fatalf("%s does not hold %q once", file, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(text, old, new, 1)), 0o600); err != nil {
		t.Fatalf("writing %s: %v", file, err)
	}
}

// Test_writeAPI_fixture pins the document the fixture writes: one per module,
// a symbol only Windows declares on that cell alone, an alias's owner, the
// layers and families, the same bytes twice, and a check that passes on what
// it just wrote.
func Test_writeAPI_fixture(t *testing.T) {
	t.Parallel()
	root, cells := fixtureCopy(t)
	first, err := os.ReadFile(documentPath(root, "fixture"))
	if err != nil {
		t.Fatalf("no document: %v", err)
	}
	doc := readFixtureDoc(t, root)
	core := fixtureModule + "/internal/core/lock"
	byID := func(id string) apiSymbol {
		i := slices.IndexFunc(doc.Symbols, func(s apiSymbol) bool { return s.ID == "go:"+id })
		if i < 0 {
			t.Fatalf("no record go:%s", id)
		}
		return doc.Symbols[i]
	}
	if got := byID(core + ".Mandatory").Platforms; !slices.Equal(got, []string{"windows/amd64"}) {
		t.Fatalf("a Windows-only constant's platforms = %q", got)
	}
	if got := byID(fixtureModule + "/pkg/v1/lock.Acquirer").Owner; got != "go:"+core+".Acquirer" {
		t.Fatalf("the facade alias's owner = %q", got)
	}
	if s := byID(core + ".(*LeaseConfig).Fence"); s.Layer != "core" || s.Family != "lock" || s.File != "internal/core/lock/lock.go" {
		t.Fatalf("the method's place and file = %q %q %q", s.Layer, s.Family, s.File)
	}
	if status := runWriteAPI(apiOptions{root: root, cells: cells}, &bytes.Buffer{}); status != 0 {
		t.Fatal("a second -write-api failed")
	}
	second, err := os.ReadFile(documentPath(root, "fixture"))
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("two -write-api runs wrote different bytes")
	}
	if status, report := checkFixture(t, root, cells, false, false); status != 0 {
		t.Fatalf("-check-api on a fresh document = %d:\n%s", status, report)
	}
}

// Test_checkAPI_docEdits pins that a doc edit with no `make api` fails the
// check, naming the record: a facade's doc, a core interface method's doc
// behind the facade's alias, and the doc of a symbol only a _windows.go file
// declares, judged on the cell that compiles it.
func Test_checkAPI_docEdits(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the seed.
		name string
		// file, old and new are the edit.
		file, old, new string
		// want is the record the report names.
		want string
	}
	tests := []tc{
		{name: "a facade's doc", file: "pkg/v1/lock/lock.go", old: "// Open forwards to the core.", new: "// Open forwards to the core, unchanged.", want: "changed go:" + fixtureModule + "/pkg/v1/lock.Open"},
		{name: "a core method's doc behind an alias", file: "internal/core/lock/lock.go", old: "// Acquire takes the named lock.", new: "// Acquire takes the named lock, or waits.", want: "changed go:" + fixtureModule + "/internal/core/lock.Acquirer.Acquire"},
		{name: "a Windows-only symbol's doc", file: "internal/core/lock/lock_windows.go", old: "is mandatory.", new: "is mandatory, always.", want: "changed go:" + fixtureModule + "/internal/core/lock.Mandatory on windows/amd64"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root, cells := fixtureCopy(t)
		edit(t, root, c.file, c.old, c.new)

		status, report := checkFixture(t, root, cells, false, false)

		if status != 1 || !strings.Contains(report, "differs from the code") || !strings.Contains(report, c.want) {
			t.Fatalf("-check-api after %s = %d, want 1 naming %q:\n%s", c.name, status, c.want, report)
		}
		if status := runWriteAPI(apiOptions{root: root, cells: cells}, &bytes.Buffer{}); status != 0 {
			t.Fatal("-write-api failed")
		}
		if status, report := checkFixture(t, root, cells, false, false); status != 0 {
			t.Fatalf("-check-api after make api = %d:\n%s", status, report)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// writePins writes, as kit would from the design, the pin files of every
// package of a document: one file per distinct set of cells, its markers the
// document's records. With a design file named, each pin carries kit's
// header and its digests.
func writePins(t *testing.T, root string, doc *apiDocument, design string) {
	t.Helper()
	files := map[string][]string{}
	for _, s := range doc.Symbols {
		dir := strings.TrimPrefix(strings.TrimPrefix(s.Package, fixtureModule), "/")
		name := "api_gen_test.go"
		if len(s.Platforms) > 0 {
			name = "api_gen_1_test.go"
		}
		key := filepath.Join(dir, name)
		files[key] = append(files[key], fmt.Sprintf("// go:%s %s %s", strings.TrimPrefix(s.ID, idPrefix), s.Kind, s.Canonical))
	}
	var designBytes []byte
	if design != "" {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(design)))
		if err != nil {
			t.Fatalf("reading the design: %v", err)
		}
		designBytes = raw
	}
	for key, markers := range files {
		body := "\n"
		if strings.HasSuffix(key, "api_gen_1_test.go") {
			body += "//go:build windows\n\n"
		}
		body += "package " + filepath.Base(filepath.Dir(key)) + "_test\n\n" + strings.Join(markers, "\n") + "\n"
		src := body
		if design != "" {
			src = kitFile(design, designBytes, body)
		}
		stage(t, filepath.Join(root, filepath.Dir(key)), map[string]string{filepath.Base(key): src})
	}
}

// TestCheckAPIMarkers pins every case the pin markers report, each by its
// name, on a fixture whose pins a test writes from the document as kit
// writes them from the design: pins that match pass; a marker removed, a
// symbol added to the code alone, a marker the code lacks, a constraint
// widened, a struct tag, a pointer receiver turned value and a function
// turned variable each fail, naming the symbol — and a Windows-only marker
// removed fails on the Windows cell alone.
func TestCheckAPIMarkers(t *testing.T) {
	t.Parallel()
	core := "internal/core/lock/lock.go"
	type tc struct {
		// name describes the case.
		name string
		// change mutates the copy after its pins are written.
		change func(t *testing.T, root string)
		// want are fragments of the report.
		want []string
	}
	tests := []tc{
		{name: "pins that match the code", change: func(*testing.T, string) {}},
		{
			name: "a missing marker",
			change: func(t *testing.T, root string) {
				edit(t, root, "internal/core/lock/api_gen_test.go", "// go:"+fixtureModule+"/internal/core/lock.Default const untyped string\n", "")
			},
			want: []string{"go:" + fixtureModule + "/internal/core/lock.Default: undeclared"},
		},
		{
			name: "an undeclared symbol",
			change: func(t *testing.T, root string) {
				edit(t, root, core, "// Default is", "// Extra is new.\nconst Extra = 1\n\n// Default is")
			},
			want: []string{"go:" + fixtureModule + "/internal/core/lock.Extra: undeclared: the code declares it and no pin marker does — a symbol added without the design"},
		},
		{
			name: "a marker the code lacks",
			change: func(t *testing.T, root string) {
				edit(t, root, core, "// Default is the default locker's directory.\nconst Default = \"locks\"\n", "")
			},
			want: []string{"go:" + fixtureModule + "/internal/core/lock.Default: missing: a pin marker declares it and the code does not"},
		},
		{
			name:   "a constraint widened",
			change: func(t *testing.T, root string) { edit(t, root, core, "Keys[K comparable]", "Keys[K any]") },
			want:   []string{"go:" + fixtureModule + "/internal/core/lock.Keys[...]: constraint differs"},
		},
		{
			name:   "a struct tag",
			change: func(t *testing.T, root string) { edit(t, root, core, "`json:\"name\"`", "`json:\"lock\"`") },
			want:   []string{"go:" + fixtureModule + "/internal/core/lock.LeaseConfig: tag differs"},
		},
		{
			name: "a pointer receiver turned value",
			change: func(t *testing.T, root string) {
				edit(t, root, core, "func (c *LeaseConfig) Fence()", "func (c LeaseConfig) Fence()")
			},
			want: []string{"receiver differs: the code's method has a value receiver, the marker's a pointer one"},
		},
		{
			name: "a function turned variable",
			change: func(t *testing.T, root string) {
				edit(t, root, core, "func Open(dir string) (*LeaseConfig, error) {", "var Open = func(dir string) (*LeaseConfig, error) {")
			},
			want: []string{"go:" + fixtureModule + "/internal/core/lock.Open: kind differs: the code has a var, the marker a func"},
		},
		{
			name: "a Windows-only marker removed",
			change: func(t *testing.T, root string) {
				edit(t, root, "internal/core/lock/api_gen_1_test.go", "// go:"+fixtureModule+"/internal/core/lock.Mandatory const untyped bool\n", "")
			},
			want: []string{"go:" + fixtureModule + "/internal/core/lock.Mandatory: undeclared", "(on windows/amd64)"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root, cells := fixtureCopy(t)
		writePins(t, root, new(readFixtureDoc(t, root)), "")
		c.change(t, root)
		if status := runWriteAPI(apiOptions{root: root, cells: cells}, &bytes.Buffer{}); status != 0 {
			t.Fatal("-write-api failed")
		}

		status, report := checkFixture(t, root, cells, true, false)

		if len(c.want) == 0 {
			if status != 0 || !strings.Contains(report, "pin markers hold") {
				t.Fatalf("-check-api -markers = %d, want 0 and the markers holding:\n%s", status, report)
			}
			return
		}
		if status != 1 {
			t.Fatalf("-check-api -markers = %d, want 1:\n%s", status, report)
		}
		for _, w := range c.want {
			if !strings.Contains(report, w) {
				t.Fatalf("the report does not name %q:\n%s", w, report)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestCheckAPIDigests pins every case the digests report, each by its name,
// on a fixture whose pins carry kit's header over a design file: digests
// that hold pass; a design byte changed, a pin body edited and a design file
// removed each fail, naming the file.
func TestCheckAPIDigests(t *testing.T) {
	t.Parallel()
	pin := "internal/core/lock/api_gen_test.go"
	type tc struct {
		// name describes the case.
		name string
		// change mutates the copy after its pins are written.
		change func(t *testing.T, root string)
		// want is a fragment of the report.
		want string
	}
	tests := []tc{
		{name: "digests that hold", change: func(*testing.T, string) {}},
		{
			name:   "a changed design byte",
			change: func(t *testing.T, root string) { edit(t, root, fixtureDesign, "domain: lock", "domain: locks") },
			want:   pin + ": design file changed: " + fixtureDesign + " hashes to",
		},
		{
			name: "an edited body",
			change: func(t *testing.T, root string) {
				edit(t, root, pin, "package lock_test", "package lock_test // edited")
			},
			want: pin + ": body edited",
		},
		{
			name: "a missing design file",
			change: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(fixtureDesign))); err != nil {
					t.Fatalf("removing the design: %v", err)
				}
			},
			want: pin + ": design file missing: " + fixtureDesign + " does not exist",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root, cells := fixtureCopy(t)
		stage(t, filepath.Join(root, "design"), map[string]string{"lock.yaml": "domain: lock\n"})
		writePins(t, root, new(readFixtureDoc(t, root)), fixtureDesign)
		c.change(t, root)

		status, report := checkFixture(t, root, cells, true, true)

		if c.want == "" {
			if status != 0 || !strings.Contains(report, "generated-file digests hold") {
				t.Fatalf("-check-api -markers -digests = %d, want 0 and the digests holding:\n%s", status, report)
			}
			return
		}
		if status != 1 || !strings.Contains(report, c.want) {
			t.Fatalf("-check-api -digests = %d, want 1 naming %q:\n%s", status, c.want, report)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
