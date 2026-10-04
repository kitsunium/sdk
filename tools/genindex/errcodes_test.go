// Package main — docs/error-codes.yaml written from docs/api and checked
// against it, on fixtures; and the repository's file against a census of the
// sources with go/parser alone, which shares no code with docs/api's writer.
package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/token"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// facadeErrsPath is pkg/v1/errs, whose Code aliases the kernel's: the
// framework declares its codes with it.
const facadeErrsPath string = "github.com/kitsunium/sdk/pkg/v1/errs"

// entryKey names an entry the way the check reports it: package.Name code.
func entryKey(c errorCode) string {
	return c.dir + "." + c.name + " " + c.dotted()
}

// codeSym is a const record with a code, as docs/api writes one.
func codeSym(pkgPath, name, quad, init string) apiSymbol {
	return apiSymbol{
		ID: "go:" + pkgPath + "." + name, Kind: kindConst, Package: pkgPath, Name: name,
		Spelled: "errs.Code", Canonical: errsPath + ".Code", Init: init, Code: &apiCode{Value: quad},
	}
}

// fixtureDocs is an SDK-like module and a vendor module: an iota meta-code
// and a mask in errs, codes out of value order in the core, a re-export in
// pkg/v1, a sentinel, a constant that is no code, one code read on two cells
// with two docs, and a vendor module's code under its module's directory.
func fixtureDocs() []*apiDocument {
	const sdk = "github.com/kitsunium/sdk"
	lockPath := sdk + "/internal/core/app/lock"
	facade := sdk + "/pkg/v1/app/lock"
	onLinux := codeSym(lockPath, "CodeLockNotHeld", "0.2.21.2", "")
	onLinux.Doc, onLinux.Platforms = "linux", []string{"linux/amd64"}
	onDarwin := codeSym(lockPath, "CodeLockNotHeld", "0.2.21.2", "")
	onDarwin.Doc, onDarwin.Platforms = "darwin", []string{"darwin/arm64"}
	sentinel := apiSymbol{
		ID: "go:" + lockPath + ".LockNotHeld", Kind: kindVar, Package: lockPath, Name: "LockNotHeld",
		Code: &apiCode{Value: "0.2.21.2", Reason: "LOCK_NOT_HELD", Public: "the lock is not held"},
	}
	plain := apiSymbol{ID: "go:" + lockPath + ".Poll", Kind: kindConst, Package: lockPath, Name: "Poll", Value: "5"}
	return []*apiDocument{
		{
			Format: apiFormat, Module: sdk, Dir: ".",
			Packages: []apiPackage{
				{Path: errsPath, Name: "errs", Dir: "internal/kernel/errs"},
				{Path: lockPath, Name: "lock", Dir: "internal/core/app/lock"},
				{Path: facade, Name: "lock", Dir: "pkg/v1/app/lock"},
			},
			Symbols: []apiSymbol{
				codeSym(errsPath, "CodeInvalidCode", "0.0.0.1", ""),
				codeSym(errsPath, "MaskExact", "255.255.255.255", ""),
				codeSym(lockPath, "CodeLockTen", "0.2.10.1", ""),
				onLinux, onDarwin,
				codeSym(lockPath, "CodeLockNine", "0.2.9.1", ""),
				codeSym(facade, "CodeLockNotHeld", "0.2.21.2", "go:"+lockPath+".CodeLockNotHeld"),
				sentinel, plain,
			},
		},
		{
			Format: apiFormat, Module: sdk + "/third-party/aws", Dir: "third-party/aws",
			Packages: []apiPackage{{Path: sdk + "/third-party/aws/writer/s3", Name: "s3", Dir: "writer/s3"}},
			Symbols:  []apiSymbol{codeSym(sdk+"/third-party/aws/writer/s3", "CodeS3Failed", "64.3.1.1", "")},
		},
	}
}

// Test_errorCodes pins the rule: a constant with a code, no initializer and
// a Code… name is an entry, once whatever the cells, under its package's
// directory, in value order — 0.2.9.1 before 0.2.10.1; a re-export, a mask,
// a sentinel and a constant with no code are none.
func Test_errorCodes(t *testing.T) {
	t.Parallel()
	got, err := errorCodes(fixtureDocs())
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, c := range got {
		keys = append(keys, entryKey(c)+" "+c.hex())
	}
	want := []string{
		"internal/kernel/errs.CodeInvalidCode 0.0.0.1 0x00_00_00_01",
		"internal/core/app/lock.CodeLockNine 0.2.9.1 0x00_02_09_01",
		"internal/core/app/lock.CodeLockTen 0.2.10.1 0x00_02_0A_01",
		"internal/core/app/lock.CodeLockNotHeld 0.2.21.2 0x00_02_15_02",
		"third-party/aws/writer/s3.CodeS3Failed 64.3.1.1 0x40_03_01_01",
	}
	if !slices.Equal(keys, want) {
		t.Errorf("entries:\n  got  %q\n  want %q", keys, want)
	}
}

// Test_errorCodesRefuses names a constant read with two values on two cells,
// and a code that is no dotted quad.
func Test_errorCodesRefuses(t *testing.T) {
	t.Parallel()
	const lockPath = "github.com/kitsunium/sdk/internal/core/app/lock"
	pkgs := []apiPackage{{Path: lockPath, Name: "lock", Dir: "internal/core/app/lock"}}
	for _, tc := range []struct {
		name string
		syms []apiSymbol
		want string
	}{
		{"two values", []apiSymbol{codeSym(lockPath, "CodeX", "0.2.21.1", ""), codeSym(lockPath, "CodeX", "0.2.21.9", "")}, "is 0.2.21.1 on some cells and 0.2.21.9 on others"},
		{"no dotted quad", []apiSymbol{codeSym(lockPath, "CodeX", "0.2.256.1", "")}, `"0.2.256.1" is no dotted quad`},
		{"three elements", []apiSymbol{codeSym(lockPath, "CodeX", "0.2.21", "")}, `"0.2.21" is no dotted quad`},
	} {
		_, err := errorCodes([]*apiDocument{{Format: apiFormat, Dir: ".", Packages: pkgs, Symbols: tc.syms}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.want)
		}
	}
}

// Test_encodeErrorCodes pins the bytes: the header, then four lines per
// entry, the shape every reader of the file parses.
func Test_encodeErrorCodes(t *testing.T) {
	t.Parallel()
	raw := encodeErrorCodes([]errorCode{{quad: [quadParts]uint64{0, 2, 21, 2}, name: "CodeLockNotHeld", dir: "internal/core/app/lock"}})
	want := errorCodesHeader + "  - code: \"0.2.21.2\"\n    const: CodeLockNotHeld\n    hex: \"0x00_02_15_02\"\n    package: internal/core/app/lock\n"
	if string(raw) != want {
		t.Errorf("bytes:\n%s\nwant:\n%s", raw, want)
	}
}

// stageRepository writes the fixture documents under a temporary
// repository's docs/api, and the file when given.
func stageRepository(t *testing.T, file []byte) string {
	t.Helper()
	root := t.TempDir()
	for i, doc := range fixtureDocs() {
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		name := []string{"sdk", "sdk/third-party/aws"}[i]
		target := documentPath(root, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if file != nil {
		if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(errorCodesFile)), file, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Test_writeThenCheckErrorCodes: -write-error-codes writes what the check
// then passes; a file missing an entry, holding one docs/api does not
// declare, or absent fails the check, naming the entry and the remedy; a
// docs/api of another format is refused.
func Test_writeThenCheckErrorCodes(t *testing.T) {
	t.Parallel()
	root := stageRepository(t, nil)
	var out bytes.Buffer
	if st := runWriteErrorCodes(root, &out); st != 0 {
		t.Fatalf("write: status %d: %s", st, out.String())
	}
	if !strings.Contains(out.String(), "(5 codes)") {
		t.Errorf("write: %q, want the count", out.String())
	}
	written, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(errorCodesFile)))
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if st := runCheckErrorCodes(root, &out); st != 0 {
		t.Fatalf("check after write: status %d: %s", st, out.String())
	}
	entry := "  - code: \"0.2.9.1\"\n    const: CodeLockNine\n    hex: \"0x00_02_09_01\"\n    package: internal/core/app/lock\n"
	stale := "  - code: \"0.2.9.9\"\n    const: CodeGone\n    hex: \"0x00_02_09_09\"\n    package: internal/core/app/lock\n"
	for _, tc := range []struct {
		name string
		file []byte
		want []string
	}{
		{"an entry missing", bytes.Replace(written, []byte(entry), nil, 1), []string{"declared, not listed: internal/core/app/lock.CodeLockNine 0.2.9.1", "run `make error-codes`"}},
		{"an entry docs/api does not declare", append(slices.Clone(written), stale...), []string{"listed, not declared: internal/core/app/lock.CodeGone 0.2.9.9"}},
		{"the header edited", bytes.Replace(written, []byte("DO NOT EDIT"), []byte("edit"), 1), []string{"differs from docs/api"}},
	} {
		repo := stageRepository(t, tc.file)
		out.Reset()
		if st := runCheckErrorCodes(repo, &out); st != 1 {
			t.Errorf("%s: status %d, want 1", tc.name, st)
		}
		for _, w := range tc.want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%s: output %q lacks %q", tc.name, out.String(), w)
			}
		}
	}
	out.Reset()
	if st := runCheckErrorCodes(stageRepository(t, nil), &out); st != 1 || !strings.Contains(out.String(), "cannot be read") {
		t.Errorf("no file: status %d, output %q", st, out.String())
	}
	other := stageRepository(t, written)
	raw, err := os.ReadFile(documentPath(other, "sdk"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(documentPath(other, "sdk"), bytes.Replace(raw, []byte(apiFormat), []byte("sdk.api/v2"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if st := runCheckErrorCodes(other, &out); st != 1 || !strings.Contains(out.String(), `is format "sdk.api/v2"`) {
		t.Errorf("another format: status %d, output %q", st, out.String())
	}
}

// Test_errorCodesCensus holds the repository's docs/error-codes.yaml to the
// sources read with go/parser alone: every constant some cell compiles whose
// name starts with Code and whose type is errs.Code — written, or carried
// down an iota group — and that re-exports no other package's constant, by
// package and name; a hex literal's value as well. What the YAML lists
// through docs/api is what the sources declare.
func Test_errorCodesCensus(t *testing.T) {
	t.Parallel()
	needRepository(t)
	cells := sdkCells(t)
	declared := map[string]string{}
	for dir, mod := range workModules(t) {
		for _, pkg := range parseModule(t, dir, mod, cells) {
			rel := strings.TrimPrefix(strings.TrimPrefix(pkg.path, mod), "/")
			for name, value := range sourceCodes(t, pkg) {
				declared[path.Join(dir, rel)+"."+name] = value
			}
		}
	}
	listed := map[string]string{}
	for entry := range readErrorCodes(t) {
		key, quad, _ := strings.Cut(entry, " ")
		listed[key] = quad
	}
	for _, key := range slices.Sorted(maps.Keys(declared)) {
		quad, ok := listed[key]
		switch {
		case !ok:
			t.Errorf("the sources declare %s; docs/error-codes.yaml does not list it", key)
		case declared[key] != "" && declared[key] != quad:
			t.Errorf("%s is %s in the sources and %s in docs/error-codes.yaml", key, declared[key], quad)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(listed)) {
		if _, ok := declared[key]; !ok {
			t.Errorf("docs/error-codes.yaml lists %s; the sources declare no such code", key)
		}
	}
	t.Logf("%d codes in the sources, %d listed", len(declared), len(listed))
}

// sourceCodes are a package's code constants by name, with the dotted quad
// of a hex literal's value ("" for any other value, an iota row included).
func sourceCodes(t *testing.T, pkg parsedPkg) map[string]string {
	t.Helper()
	out := map[string]string{}
	inErrs := pkg.path == errsPath || pkg.path == facadeErrsPath
	for _, f := range pkg.files {
		errsNames := map[string]bool{}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: import %s: %v", pkg.path, imp.Path.Value, err)
			}
			if p == errsPath || p == facadeErrsPath {
				name := "errs"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				errsNames[name] = true
			}
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			var typ ast.Expr
			var values []ast.Expr
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				if vs.Type != nil || len(vs.Values) > 0 {
					typ, values = vs.Type, vs.Values
				}
				if !isCodeType(typ, inErrs, errsNames) {
					continue
				}
				for i, n := range vs.Names {
					if !n.IsExported() || !strings.HasPrefix(n.Name, codePrefix) {
						continue
					}
					var v ast.Expr
					if i < len(values) {
						v = values[i]
					}
					if _, reexport := v.(*ast.SelectorExpr); reexport && len(vs.Values) > 0 {
						continue
					}
					quad, _ := hexQuad(v, len(vs.Values) > 0)
					out[n.Name] = quad
				}
			}
		}
	}
	return out
}

// isCodeType reports whether a constant's written type is errs.Code — the
// kernel's or pkg/v1's alias of it: the qualified name under any import name,
// or Code inside either package.
func isCodeType(e ast.Expr, inErrs bool, errsNames map[string]bool) bool {
	switch e := e.(type) {
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		return ok && errsNames[x.Name] && e.Sel.Name == "Code"
	case *ast.Ident:
		return inErrs && e.Name == "Code"
	}
	return false
}

// hexQuad is a hex literal written on the constant's own row, as a dotted
// quad; ok is false for any other value, an iota row included.
func hexQuad(v ast.Expr, own bool) (quad string, ok bool) {
	lit, isLit := v.(*ast.BasicLit)
	if !own || !isLit || lit.Kind != token.INT || !strings.HasPrefix(strings.ToLower(lit.Value), "0x") {
		return "", false
	}
	n, err := strconv.ParseUint(strings.ReplaceAll(lit.Value[2:], "_", ""), 16, 32)
	if err != nil {
		return "", false
	}
	return errorCode{quad: [quadParts]uint64{n >> 24 & 0xff, n >> 16 & 0xff, n >> 8 & 0xff, n & 0xff}}.dotted(), true
}
