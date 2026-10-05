// Package main — the READMEs written from docs/api (ADR 0167).
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixtureReadmes writes the copy's READMEs and returns the facade's.
func writeFixtureReadmes(t *testing.T, root string) string {
	t.Helper()
	var out bytes.Buffer
	if status := runWriteReadmes(root, &out); status != 0 {
		t.Fatalf("-write-readmes = %d:\n%s", status, out.String())
	}
	raw, err := os.ReadFile(filepath.Join(root, "pkg", "v1", "lock", readmeFile))
	if err != nil {
		t.Fatalf("the facade's README: %v", err)
	}
	return string(raw)
}

// TestWriteReadmes_fixture pins what a README holds: the header, the title
// and the import, the package comment, the index, each symbol's declaration
// and doc — an alias with the declaration, the field docs and the methods
// of the type it names, which go/doc could not show — a doc link to an
// anchor of the page, a source link, the examples of the package's tests
// with their output; a README for the packages under pkg/ and framework/
// alone; the same bytes twice.
func TestWriteReadmes_fixture(t *testing.T) {
	t.Parallel()
	root, _ := fixtureCopy(t)
	stage(t, filepath.Join(root, "pkg", "v1", "lock"), map[string]string{"lock_example_test.go": `package lock_test

import (
	"fmt"

	"example.com/fixture/pkg/v1/lock"
)

// ExampleOpen opens a configuration.
func ExampleOpen() {
	c, _ := lock.Open("locks")
	fmt.Println(c.Name)
	// Output: locks
}
`})
	readme := writeFixtureReadmes(t, root)
	for _, want := range []string{
		readmeHeader + "\n\n# lock\n\n```go\nimport \"example.com/fixture/pkg/v1/lock\"\n```\n\nPackage lock is the fixture's facade: aliases and a forwarder.\n",
		"## Index\n\n- [func Open\\(dir string\\) \\(\\*LeaseConfig, error\\)](<#Open>)\n- [type Acquirer](<#Acquirer>)\n  - [func \\(Acquirer\\) Acquire",
		"<a name=\"Open\"></a>\n## func [Open](<" + sourceBase + "pkg/v1/lock/lock.go>)\n\n```go\nfunc Open(dir string) (*LeaseConfig, error)\n```\n\nOpen forwards to the core.\n",
		"```go\ntype Acquirer = corelock.Acquirer\n```\n\nAcquirer is an alias of `example.com/fixture/internal/core/lock.Acquirer`, declared as:\n\n```go\ntype Acquirer interface {\n\t// Acquire takes the named lock.\n\tAcquire(name string) (*LeaseConfig, error)\n}\n```",
		"<a name=\"Acquirer.Acquire\"></a>\n### func \\(Acquirer\\) [Acquire](<" + sourceBase + "internal/core/lock/lock.go>)",
		"type LeaseConfig struct {\n\t// Name is the lock's name.\n\tName string `json:\"name\"`\n}",
		"```go\nfunc (*LeaseConfig) Fence() uint64\n```\n\nFence returns the lease's fencing token.",
		"<details><summary>Example</summary>\n<p>\n\nExampleOpen opens a configuration.\n\n```go\n",
		"#### Output\n\n```\nlocks\n```",
		"Generated from [docs/api](<" + sourceBase + "docs/api>) by tools/genindex.\n",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("the README does not hold %q:\n%s", want, readme)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "core", "lock", readmeFile)); err == nil {
		t.Fatal("a README was written for an internal package outside pkg/ and framework/")
	}
	if again := writeFixtureReadmes(t, root); again != readme {
		t.Fatal("-write-readmes wrote other bytes the second time")
	}
}

// TestCheckReadmes_drift pins every finding -check-readmes reports, each by
// its name: READMEs that match pass; one edited by hand differs; one removed
// is missing; one genindex wrote for a package docs/api no longer records is
// stale, and -write-readmes removes it.
func TestCheckReadmes_drift(t *testing.T) {
	t.Parallel()
	root, _ := fixtureCopy(t)
	writeFixtureReadmes(t, root)
	check := func() (int, string) {
		var out bytes.Buffer
		status := runCheckReadmes(root, &out)
		return status, out.String()
	}
	if status, report := check(); status != 0 || !strings.Contains(report, "the 1 READMEs are what docs/api writes") {
		t.Fatalf("-check-readmes on fresh READMEs = %d:\n%s", status, report)
	}
	path := filepath.Join(root, "pkg", "v1", "lock", readmeFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, "An edit by hand.\n"...), readmePerm); err != nil {
		t.Fatal(err)
	}
	if status, report := check(); status != 1 || !strings.Contains(report, "pkg/v1/lock/README.md: differs from what docs/api writes") {
		t.Fatalf("an edited README = %d:\n%s", status, report)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if status, report := check(); status != 1 || !strings.Contains(report, "pkg/v1/lock/README.md: missing") {
		t.Fatalf("a missing README = %d:\n%s", status, report)
	}
	stage(t, filepath.Join(root, "pkg", "v1", "gone"), map[string]string{readmeFile: readmeHeader + "\n\n# gone\n"})
	writeFixtureReadmes(t, root)
	stage(t, filepath.Join(root, "pkg", "v1", "gone"), map[string]string{readmeFile: readmeHeader + "\n\n# gone\n"})
	if status, report := check(); status != 1 || !strings.Contains(report, "pkg/v1/gone/README.md: stale") {
		t.Fatalf("a stale README = %d:\n%s", status, report)
	}
	writeFixtureReadmes(t, root)
	if _, err := os.Stat(filepath.Join(root, "pkg", "v1", "gone", readmeFile)); err == nil {
		t.Fatal("-write-readmes kept a stale README")
	}
	stage(t, filepath.Join(root, "pkg", "v1", "gone"), map[string]string{readmeFile: "# gone, by hand\n"})
	if status, report := check(); status != 0 {
		t.Fatalf("a README genindex did not write is the owner's = %d:\n%s", status, report)
	}
}

// Test_declOf pins each kind's declaration, as gofmt starts it.
func Test_declOf(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		s    apiSymbol
		recv string
		want string
	}{
		{apiSymbol{Kind: kindFunc, Spelled: "func[K comparable](k K) T"}, "", "func F[K comparable](k K) T"},
		{apiSymbol{ID: "go:p.(*T).F", Kind: kindMethod, Recv: "T", Spelled: "func() error"}, "A", "func (*A) F() error"},
		{apiSymbol{ID: "go:p.T.F", Kind: kindMethod, Recv: "T", Spelled: "func()"}, "", "func (T) F()"},
		{apiSymbol{Kind: kindAlias, Spelled: "[K comparable] map[K]int"}, "", "type F[K comparable] = map[K]int"},
		{apiSymbol{Kind: kindConst, Spelled: "untyped int", Value: "3"}, "", "const F = 3"},
		{apiSymbol{Kind: kindConst, Spelled: "Code", Value: "0x01"}, "", "const F Code = 0x01"},
		{apiSymbol{Kind: kindVar, Spelled: "*errs.Error"}, "", "var F *errs.Error"},
		{apiSymbol{Kind: kindType, Spelled: "uint32"}, "", "type F uint32"},
		{apiSymbol{Kind: kindType, Spelled: "interface{A(); B() error}"}, "", "type F interface {\n\tA()\n\tB() error\n}"},
		{apiSymbol{Kind: kindType, Spelled: "struct{}"}, "", "type F struct{}"},
		{
			apiSymbol{Kind: kindType, Spelled: "struct{…}", Fields: []apiField{
				{Name: "Name", Type: "string", Tag: `json:"name"`}, {Name: "N", Type: "int"}, {Name: "Reader", Type: "io.Reader", Embedded: true},
			}},
			"",
			"type F struct {\n\tName string `json:\"name\"`\n\tN    int\n\tio.Reader\n}",
		},
	} {
		if got := declOf(&c.s, "F", c.recv); got != c.want {
			t.Errorf("declOf(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}

// Test_memberName pins which lines of a declaration are a member's.
func Test_memberName(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]string{
		"\tName string": "Name", "\tAcquire(name string) error": "Acquire", "\tio.Reader": "Reader",
		"\t// a comment": "", "type T struct {": "", "}": "", "\t\tnested": "",
	} {
		if got, _ := memberName(line); got != want {
			t.Errorf("memberName(%q) = %q, want %q", line, got, want)
		}
	}
}

// Test_escapeMarkdown pins what a link's text escapes.
func Test_escapeMarkdown(t *testing.T) {
	t.Parallel()
	if got, want := escapeMarkdown("func F(m map[K]*V) <-chan T"), `func F\(m map\[K\]\*V\) \<\-chan T`; got != strings.ReplaceAll(want, `\-`, "-") {
		t.Fatalf("escapeMarkdown = %q", got)
	}
}
