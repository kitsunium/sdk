package app_test

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ADR 0180 §6: a product's exported tree, its release binary and its
// execution carry no occurrence of the tool's name. This file is the SDK's
// guard of that rule, on testdata/traceprobe — a whole product on
// framework/app, every subsystem but the Studio imported.
//
// The token. An occurrence of the three letters k, i, t, in any case, is a
// trace, wherever it lies — alone (kit), at the head or the tail of a word
// (kitTable, studiokit, KIT_DATA_DIR, kit-data-key, /_kit/, kit/v1), or
// inside one — but in two places only, both lifted out of the text before it
// is read:
//
//   - kitsunium, the vendor's name, which the owner accepts as the name of the
//     SDK a product depends on (§Context);
//   - a word of a natural language in which the letters are part of another
//     word — kitten, kitchen, skittles: in a product, the only such words are
//     those of the SDK's common-password list (pkg/v1/crypto/password's
//     IsCommon, ADR 0143), which the framework links to refuse a weak
//     password, so the list is the dictionary — each of its words that holds
//     the letters is lifted out, read from the list itself.
//
// Nothing else is lifted: a word is not judged by its spelling, so a new
// compound naming the tool is a trace whatever surrounds it. In a binary the
// string data has no separators — two literals lie end to end, "app-data-key"
// then "kit-data-key" read keykit- —, which is why the rule cannot ask for a
// word boundary and does not.

// passwordList is the SDK's common-password list, from framework/app: the
// natural-language words a product links.
const passwordList = "../../internal/service/crypto/password/commonpw/xato-net-10-million-passwords-10000.txt"

// traceprobe is the product the guard builds and runs.
const traceprobe = "./testdata/traceprobe"

// productPrefix is the probe's variables' prefix: its name, upper-cased.
const productPrefix = "SHOP_"

// formerInBinary is what a release binary of the framework still holds of
// the tool's name, each with the step of ADR 0180 or the window that admits
// it and when it goes. The ADR's aim is this list empty; it may only shrink —
// an entry the binary no longer holds fails the test, so that it is taken
// out the change that makes it stale. Every entry is a former name the
// framework reads, refuses or moves for the transition window Q12 decided
// (two minors after the step that renamed it), or a former name at rest a
// former release left, read until nothing is left under it.
var formerInBinary = []allowance{
	{"KIT_", "step 4, Q12: a former operator variable set non-empty is refused at the start, naming its new name; the refusal goes with the minor that ends the window"},
	{"/_kit/", "step 4, Q12: the former probes answer, logged as deprecated, and the former route space stays reserved, its refusals naming it, until the minor that ends the window"},
	{`is tagged kit:\"{value}\"`, "step 4, Q12: a field still tagged with the former key is refused, the refusal naming it; goes with the minor that ends the window"},
	{`\"app\", \"kit\"`, "step 5c-2: the module name kit stays reserved while a former release's data may be met under it, the refusal naming it; goes with the minor that ends the window"},
	{"kit_migrations", "step 5a: the former version table, moved at the first start; goes with the minor that ends the window"},
	{"kit_tables", "step 5a: the former registry, moved at the first start; goes with the minor that ends the window"},
	{"kit_queue", "step 5a: a consumer's former queue table, moved at the first start; goes with the minor that ends the window"},
	{"the kit_* tables", "step 5a: a move the database refused names the former tables; goes with the minor that ends the window"},
	{".kit/data", "step 5a: dev's former data directory, moved at the first start; goes with the minor that ends the window"},
	{"kit.privacy", "steps 5a and 5c-1: the privacy service's former id, moved, and bound by what its stores sealed; read while a former journal is kept (§Step 5c-1, done) — the last entry to go"},
	{"kit-", "step 5b: the former secret prefix — kit-data-key, kit-index-key, kit-smtp-url —, copied or re-wrapped at the first start and reserved; goes with the minor that ends the window"},
	{"kit/v1 sealed at rest", "step 5b: the former label of a box sealed under data-key itself, read; goes with the minor that ends the window, which refuses to start while a box still bears it"},
	{"kit/v1 subject reference", "step 5c: the former label a person's references were derived under, read and erased under; goes once no box and no hold names a former reference (§Step 5c-2, done)"},
	{"kit/v1 record reference", "step 5c: the former label a record's references were derived under; goes once no box and no hold names a former reference (§Step 5c-2, done)"},
	{"kit/v1 database index key", "step 5c: the former label of a database's index rows, re-filed when a store opens; goes with the minor that ends the window"},
	{bareFormerName, "steps 4 and 5: the former name alone (formerName), from which the reserved module name, the former tag key, the former tables and labels are made; goes with the last of the entries above"},
}

// bareFormerName is the allowance of the former name as a literal of its
// own: three letters that, in a binary's string data, run into the
// literals around them — appkitsql —, so it is admitted only where it is
// joined to no punctuation, which every other former name above is.
const bareFormerName = "kit"

// formerLiterals is what the framework's own sources still write of the
// tool's name outside the Studio subsystem, which only a dev build links:
// the string literals of framework/app and framework/internal/app and the
// keys and texts of the catalogue the core embeds. An entry is a whole
// literal, or a catalogue key; it goes with the binary's entry it makes.
var formerLiterals = []allowance{
	{"kit", "formerName (renamed.go): the former name, step 4 and 5"},
	{"KIT", "formerPrefix (renamed.go): the former variables' prefix, step 4, Q12"},
	{"/_kit/", "formerReserved (renamed.go): the former route space, step 4, Q12"},
	{"classify.tag-renamed", "the refusal of a field tagged with the former key, step 4, Q12"},
	{"module.reserved", "the refusal of the reserved module name kit, step 5c-2"},
	{"mount.prefix", "the refusal of a module prefix under the former route space, step 4, Q12"},
	{"route.reserved", "the refusal of a route under the former route space, step 4, Q12"},
	{"static.prefix", "the refusal of a frontend under the former route space, step 4, Q12"},
}

// allowance is a former name the guard admits, and why.
type allowance struct {
	// text is the name as it lies where it is admitted.
	text string
	// why is the step or the window of ADR 0180 that admits it, and when it
	// goes.
	why string
}

// A product made on the framework carries no trace of the tool: its tree,
// its release binary — symbols, string data, build information — and what it
// says and serves when it runs (ADR 0180 §6, step 6). The binary's former
// names are formerInBinary's alone; the tree and the execution admit none.
func TestAProductCarriesNoTraceOfTheTool(t *testing.T) {
	goTool := goToList(t)
	words := naturalWords(t)
	t.Run("tree", func(t *testing.T) { treeCarriesNoTrace(t, words) })
	bin := buildRelease(t, goTool, traceprobe)
	t.Run("binary", func(t *testing.T) { binaryCarriesNoTrace(t, goTool, bin, words) })
	t.Run("execution", func(t *testing.T) { executionCarriesNoTrace(t, bin, words) })
}

// treeCarriesNoTrace reads the probe's tree as a product's exported tree: its
// every file but its prose, path and content, and the go.mod its build reads.
func treeCarriesNoTrace(t *testing.T, words [][]byte) {
	t.Helper()
	files := 0
	err := filepath.WalkDir(traceprobe, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		refuse(t, "the path "+path, []byte(path), words)
		if d.IsDir() || strings.HasSuffix(path, ".md") {
			// A product's prose is its owner's, out of the check (§6).
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		refuse(t, path, data, words)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 2 {
		t.Fatalf("the probe's tree holds %d files: its source and its embedded site at least", files)
	}
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	refuse(t, "go.mod", mod, words)
}

// binaryCarriesNoTrace reads the release binary: its symbols, its string
// data, and what go version -m says of it.
func binaryCarriesNoTrace(t *testing.T, goTool, bin string, words [][]byte) {
	t.Helper()
	nm, err := exec.Command(goTool, "tool", "nm", bin).Output()
	if err != nil {
		t.Fatalf("go tool nm: %v", err)
	}
	if !bytes.Contains(nm, []byte("framework/internal/app.")) {
		t.Fatal("the probe links nothing of the framework: the check would test nothing")
	}
	refuse(t, "go tool nm", nm, words)
	version, err := exec.Command(goTool, "version", "-m", bin).Output()
	if err != nil {
		t.Fatalf("go version -m: %v", err)
	}
	refuse(t, "go version -m", version, words)
	data := stringData(t, bin)
	used := make([]bool, len(formerInBinary))
	for name, section := range data {
		for _, at := range traces(section, words) {
			if i := admitted(section, at); i >= 0 {
				used[i] = true
				continue
			}
			t.Errorf("the binary's %s names the tool: %q", name, around(section, at))
		}
	}
	for i, u := range used {
		if !u {
			t.Errorf("formerInBinary admits %q, which the binary no longer holds: take it out of the list (ADR 0180 step 6)", formerInBinary[i].text)
		}
	}
}

// stringData is the binary's data a reader of its strings sees, by section:
// every section loaded in memory but its code — the code holds no string
// the data does not, and its bytes read as letters by chance — and the
// debug sections, compressed and never loaded.
func stringData(t *testing.T, bin string) map[string][]byte {
	t.Helper()
	f, err := elf.Open(bin)
	if err != nil {
		t.Skipf("the binary is not ELF (%v): its sections are read on Linux, where CI runs the guard", err)
	}
	defer func() { _ = f.Close() }()
	data := map[string][]byte{}
	for _, s := range f.Sections {
		if s.Flags&elf.SHF_ALLOC == 0 || s.Flags&elf.SHF_EXECINSTR != 0 || s.Type == elf.SHT_NOBITS {
			continue
		}
		b, err := s.Data()
		if err != nil {
			t.Fatalf("reading %s: %v", s.Name, err)
		}
		data[s.Name] = b
	}
	if len(data[".rodata"]) == 0 {
		t.Fatal("the binary has no .rodata: the check would test nothing")
	}
	return data
}

// admitted is the index in formerInBinary of the entry the trace at at lies
// in, or -1.
func admitted(text []byte, at int) int {
	for i, a := range formerInBinary {
		if a.text == bareFormerName {
			if bare(text, at) {
				return i
			}
			continue
		}
		for j := range len(a.text) - 2 {
			if !strings.EqualFold(a.text[j:j+3], "kit") {
				continue
			}
			if start := at - j; start >= 0 && bytes.HasPrefix(text[start:], []byte(a.text)) {
				return i
			}
		}
	}
	return -1
}

// bare reports whether the trace at at is the former name as a literal of
// its own, run into the literals around it: lower-case, and joined to
// nothing but letters, digits or non-printable bytes on either side.
func bare(text []byte, at int) bool {
	if string(text[at:at+3]) != bareFormerName {
		return false
	}
	joined := func(c byte) bool {
		return c < 0x20 || c >= 0x7f || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}
	return (at == 0 || joined(text[at-1])) && (at+3 == len(text) || joined(text[at+3]))
}

// executionCarriesNoTrace runs the probe as an operator does — its help, its
// graph, its configuration, its secrets, then served — and refuses the token
// in everything it says and serves and in the names of the files it writes.
func executionCarriesNoTrace(t *testing.T, bin string, words [][]byte) {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"-h"}, {"graph"}, {"graph", "-format", "mermaid"}, {"config"}, {"secrets", "list"}} {
		out := run(t, bin, dir, args...)
		refuse(t, "probe "+strings.Join(args, " "), out, words)
		switch args[0] {
		case "-h":
			mustSay(t, "-h", out, productPrefix+"DATA_DIR", "/_app/")
		case "graph":
			mustSay(t, "graph", out, "/orders/{id}/ship", "orders", "lifecycle", "departures", "receipts")
		case "config":
			mustSay(t, "config", out, productPrefix+"DATA_DIR", productPrefix+"GREETING")
		}
	}
	answered := serve(t, bin, dir)
	refuse(t, "what the served probe answered and logged", answered, words)
	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		refuse(t, "the file the probe made "+rel, []byte(rel), words)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// served lists the requests the guard makes of the running probe: its
// probes, its frontend, an operation, its webhook, the reserved route space
// and a file it does not have.
var served = []struct{ method, path string }{
	{http.MethodGet, "/_app/health/live"},
	{http.MethodGet, "/_app/health/ready"},
	{http.MethodGet, "/_app/health/startup"},
	{http.MethodGet, "/"},
	{http.MethodGet, "/orders/o-1"},
	{http.MethodPost, "/orders/o-1/ship"},
	{http.MethodPost, "/hooks/carrier"},
	{http.MethodGet, "/_app/"},
	{http.MethodGet, "/_app/api/graph"},
	{http.MethodGet, "/missing.js"},
}

// serve starts the probe in production with a data directory under dir,
// asks it every request of served, stops it, and returns everything it
// answered — status, headers and bodies — and logged.
func serve(t *testing.T, bin, dir string) []byte {
	t.Helper()
	addr := freeAddr(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var logs bytes.Buffer
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = dir
	cmd.Env = productEnv(productPrefix+"ADDR="+addr, productPrefix+"DATA_DIR="+filepath.Join(dir, "data"))
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	defer stop()
	client := &http.Client{Timeout: 10 * time.Second}
	base := "http://" + addr
	if !ready(ctx, client, base) {
		stop()
		t.Fatalf("the probe never answered on %s:\n%s", addr, logs.String())
	}
	var out bytes.Buffer
	for _, r := range served {
		out.Write(ask(t, client, r.method, base+r.path))
	}
	stop()
	out.Write(logs.Bytes())
	mustSay(t, "the served probe", out.Bytes(), "HTTP/1.1 200", "The shop.")
	return out.Bytes()
}

// ready waits until the probe's liveness probe answers 200.
func ready(ctx context.Context, client *http.Client, base string) bool {
	for ctx.Err() == nil {
		resp, err := client.Get(base + "/_app/health/live")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// ask makes one request and returns the answer as written on the wire: its
// status line, headers and body.
func ask(t *testing.T, client *http.Client, method, url string) []byte {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var wire bytes.Buffer
	if err := resp.Write(&wire); err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return wire.Bytes()
}

// freeAddr is a loopback address no one listens on now.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// run runs the probe with args in dir and returns what it wrote, standard
// output then standard error; it must exit 0.
func run(t *testing.T, bin, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = productEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("probe %s: %v\n%s%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return append(stdout.Bytes(), stderr.Bytes()...)
}

// productEnv is the test's environment without a variable of the probe's
// nor of the former prefix — which the start would read or refuse —, with
// extra.
func productEnv(extra ...string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, productPrefix) || strings.HasPrefix(strings.ToUpper(kv), "KIT_")
	})
	return append(env, extra...)
}

// mustSay fails unless out holds every one of says: what the guard reads is
// what it means to read, so that it cannot pass by reading nothing.
func mustSay(t *testing.T, what string, out []byte, says ...string) {
	t.Helper()
	for _, s := range says {
		if !bytes.Contains(out, []byte(s)) {
			t.Errorf("%s does not say %q: the guard reads the wrong output\n%s", what, s, out)
		}
	}
}

// buildRelease builds the program at dir as a release is built — the
// trimmed paths, no build tag, no overlay — and returns the binary. The
// version control stamp is left out: it names a commit, never the tool, and
// the guard runs where no repository is.
func buildRelease(t *testing.T, goTool, dir string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), filepath.Base(dir))
	if runtime.GOOS == "windows" {
		// Windows runs a program by its extension.
		bin += ".exe"
	}
	build := exec.Command(goTool, "build", "-trimpath", "-buildvcs=false", "-o", bin, dir)
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", dir, err, out)
	}
	return bin
}

// refuse fails for every trace of the tool's name in text, which nothing
// admits.
func refuse(t *testing.T, what string, text []byte, words [][]byte) {
	t.Helper()
	for _, at := range traces(text, words) {
		t.Errorf("%s names the tool: %q", what, around(text, at))
	}
}

// traces is the offsets of the tool's name in text, by the rule this file
// opens with: every "kit", in any case, but inside kitsunium and inside a
// natural word of words.
func traces(text []byte, words [][]byte) []int {
	low := asciiLower(text)
	lifted := make([]bool, len(text))
	lift := func(word []byte) {
		for i := 0; ; {
			j := bytes.Index(low[i:], word)
			if j < 0 {
				return
			}
			for k := range word {
				lifted[i+j+k] = true
			}
			i += j + 1
		}
	}
	lift([]byte("kitsunium"))
	for _, w := range words {
		lift(w)
	}
	var at []int
	for i := 0; ; {
		j := bytes.Index(low[i:], []byte("kit"))
		if j < 0 {
			return at
		}
		if !lifted[i+j] {
			at = append(at, i+j)
		}
		i += j + 1
	}
}

// asciiLower is text with its ASCII letters lower-cased, byte for byte.
func asciiLower(text []byte) []byte {
	low := make([]byte, len(text))
	for i, c := range text {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		low[i] = c
	}
	return low
}

// around is the printable text around the trace at at, for a failure.
func around(text []byte, at int) string {
	lo, hi := max(0, at-40), min(len(text), at+40)
	return string(text[lo:hi])
}

// naturalWords is the dictionary of §the token: the words of the SDK's
// common-password list that hold the letters, lower-cased.
func naturalWords(t *testing.T) [][]byte {
	t.Helper()
	list, err := os.ReadFile(passwordList)
	if err != nil {
		t.Fatalf("the SDK's password list, the guard's dictionary: %v", err)
	}
	var words [][]byte
	for w := range bytes.FieldsSeq(asciiLower(list)) {
		if bytes.Contains(w, []byte("kit")) && !bytes.Equal(w, []byte("kit")) {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		t.Fatal("the password list holds no word with the letters: the dictionary moved")
	}
	return words
}

// The framework's own sources write the tool's name in no string a product
// links, but formerLiterals' (ADR 0180 §6, step 6): the string literals of
// framework/app and framework/internal/app outside the Studio subsystem —
// framework/app/studio and studioengine, which only a dev build links — and
// the keys and texts of the catalogue the core embeds. Each entry must still
// be met, so that the list only shrinks.
func TestTheFrameworksLiteralsNameNoTool(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("under Bazel: the framework's sources are not the test's runfiles")
	}
	words := naturalWords(t)
	used := make([]bool, len(formerLiterals))
	// admit judges text, written at where: a trace in it is admitted when
	// name — the literal itself, or a catalogue's key — is an entry.
	admit := func(where, name, text string) {
		if len(traces([]byte(text), words)) == 0 {
			return
		}
		if i := slices.IndexFunc(formerLiterals, func(a allowance) bool { return a.text == name }); i >= 0 {
			used[i] = true
			return
		}
		t.Errorf("%s: %q names the tool", where, text)
	}
	for _, root := range []string{".", "../internal/app"} {
		literalsIn(t, root, func(where, lit string) { admit(where, lit, lit) })
	}
	catalogue, err := os.ReadFile("../internal/app/locales/en.json")
	if err != nil {
		t.Fatal(err)
	}
	var english map[string]string
	if err := json.Unmarshal(catalogue, &english); err != nil {
		t.Fatal(err)
	}
	if len(english) == 0 {
		t.Fatal("the catalogue holds no sentence: the check would test nothing")
	}
	for key, text := range english {
		admit("locales/en.json", key, key)
		admit("locales/en.json "+key, key, text)
	}
	for i, u := range used {
		if !u {
			t.Errorf("formerLiterals admits %q, which no source writes any more: take it out of the list (ADR 0180 step 6)", formerLiterals[i].text)
		}
	}
}

// literalsIn hands admit every string literal of the production Go files
// under root, but the Studio subsystem's and the test programs'.
func literalsIn(t *testing.T, root string, admit func(where, lit string)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && slices.Contains([]string{"testdata", "studio", "studioengine"}, d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if b, ok := n.(*ast.BasicLit); ok && b.Kind == token.STRING {
				s, err := strconv.Unquote(b.Value)
				if err != nil {
					t.Errorf("%s: %v", fset.Position(b.Pos()), err)
					return true
				}
				admit(fset.Position(b.Pos()).String(), s)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
