// Package main — genindex reads the SDK's Go code for the documentation, in
// four modes.
//
// The symbol index (the default) extracts a search index from a module's
// public packages and emits it as JSON for the static docs site to consume
// client-side: docs/site/src/components/Search.astro feeds it to MiniSearch
// for the "Symbols" tab of the search modal, and docs/site/package.json's
// `prebuild` step runs it.
//
//	go run github.com/kitsunium/sdk/tools/genindex \
//	    -input ../pkg/v1 \
//	    -output ../docs/site/src/data/symbols.json \
//	    -url-base /v1/local
//
// With -check-doclinks it emits no index: it walks the directories given as
// arguments and fails on every same-package doc link — a name, or a type and a
// member, in square brackets — that names no symbol the package declares, which
// go/doc renders as literal bracketed text (ADR 0138). `make lint-check` runs it
// over the repository.
//
// With -write-api it writes docs/api/<module>.json for every module of the
// workspace under -repo-root: every exported symbol, read from the code with
// go/types on each cell of the platforms table (`make api`). With -check-api
// it writes nothing: it regenerates the documents in memory and fails on any
// byte that differs from docs/api (`make api-check`, in `make lint-check`);
// -markers adds the comparison of every cell's symbols with the pin markers of
// api_gen*_test.go files, -digests the check of every generated file's header
// digests against the design files' bytes.
//
// Every mode is the standard library alone (tools/CLAUDE.md), so it adds no
// dependency to any go.sum.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// outputDirPerm is the mode the output directory is created with: readable
	// by everyone, writable by the build user. The file itself is served
	// publicly.
	outputDirPerm os.FileMode = 0o755
	// usageStatus is the exit status of a command line the flags refuse, as
	// the flag package's own.
	usageStatus int = 2
)

// stdoutTargets are the -output values that mean "write to stdout" rather than
// to a file of that name.
var stdoutTargets = map[string]struct{}{"": {}, "-": {}}

// main parses the flags and runs the mode they select.
//
// Every failure is fatal and reported on stderr: a partial index is worse than
// none, because the docs site would ship a search box that silently cannot find
// half the API — and a partial docs/api is worse still, since it is checked.
func main() {
	o, err := parseFlags(os.Args[1:])
	//: a flag the program does not know: the usage is printed already.
	if err != nil {
		os.Exit(usageStatus)
	}
	//: a mode reports its own exit status.
	if o.mode() != modeIndex {
		os.Exit(runMode(o))
	}
	runIndex(o)
}

// runIndex extracts the symbol index and writes it out.
func runIndex(o *cliOptions) {
	//: without a module root there is nothing to index.
	if o.input == "" {
		exitErr("genindex: -input is required")
	}

	//: an unresolvable root leaves the field empty, which disables source links
	//: rather than emitting ones that point nowhere.
	root, _ := defaultRepoRoot(o.repoRoot, o.input)
	opts := &indexOptions{
		root:            o.input,
		modulePath:      o.module,
		urlBase:         o.urlBase,
		repoRoot:        root,
		sourceURLPrefix: o.sourceURLPrefix,
	}

	syms, err := collect(opts)
	//: a directory that would not parse means the index would be missing
	//: symbols nobody would notice were gone.
	if err != nil {
		exitErr("genindex: %v", err)
	}

	//: a written index that cannot be read back is the same failure as one that
	//: was never written.
	if werr := writeIndex(o.output, buildIndex(opts, syms)); werr != nil {
		exitErr("genindex: %v", werr)
	}
}

// buildIndex wraps the extracted rows in the document the docs site loads.
func buildIndex(opts *indexOptions, syms []symbol) *index {
	//: the timestamp is stamped rather than derived, so a stale index is
	//: identifiable from the file alone.
	return &index{
		Schema:      schemaVersion,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Module:      opts.modulePath,
		Symbols:     syms,
	}
}

// defaultRepoRoot resolves the repository root the source links are made
// relative to, defaulting to two levels above the module root — the layout the
// SDK's own pkg/v1 has.
func defaultRepoRoot(configured, input string) (root string, ok bool) {
	//: an explicit root always wins.
	if configured != "" {
		//: the caller said where the repository is.
		return configured, true
	}
	abs, err := filepath.Abs(input)
	//: an unresolvable input yields no root, which disables source links rather
	//: than emitting ones that point nowhere.
	if err != nil {
		//: sourceURL declines to build a link without a root.
		return "", false
	}
	//: <input>/../.. — e.g. /repo/pkg/v1 becomes /repo.
	return filepath.Dir(filepath.Dir(abs)), true
}

// writeIndex encodes the document to the target, which is stdout when the
// caller named no file.
func writeIndex(target string, idx *index) (err error) {
	//: an omitted or dashed -output means stdout, which is what a pipeline uses.
	if _, toStdout := stdoutTargets[target]; toStdout {
		//: encode straight to stdout; there is nothing to create or close.
		return encodeIndex(os.Stdout, idx)
	}
	//: the docs site's data directory may not exist on a clean tree.
	if err := os.MkdirAll(filepath.Dir(target), outputDirPerm); err != nil {
		//: report the directory that could not be created.
		return fmt.Errorf("mkdir: %w", err)
	}
	f, cerr := os.Create(target)
	//: a file that cannot be created means the docs build has no index at all.
	if cerr != nil {
		//: report the path that could not be created.
		return fmt.Errorf("create %s: %w", target, cerr)
	}
	//: a failed close is a failed write, however clean the encode looked — the
	//: bytes may still be sitting in a buffer the kernel never took.
	defer func() {
		//: only report the close when nothing else went wrong; an encode
		//: failure is the more useful of the two.
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", target, closeErr)
		}
	}()
	//: report what went wrong while writing.
	if encErr := encodeIndex(f, idx); encErr != nil {
		//: the deferred close still runs and cannot mask this.
		return encErr
	}
	fmt.Fprintf(os.Stderr, "[genindex] wrote %s (%d symbols)\n", target, len(idx.Symbols))
	//: the index is on disk once the deferred close agrees.
	return nil
}

// encodeIndex writes the document as indented JSON.
func encodeIndex(w *os.File, idx *index) error {
	enc := json.NewEncoder(w)
	//: indented so a diff of the committed index is readable.
	enc.SetIndent("", "  ")
	//: whatever the encoder made of the document.
	if err := enc.Encode(idx); err != nil {
		//: report the encode failure to the caller.
		return fmt.Errorf("encode: %w", err)
	}
	//: the document was written in full.
	return nil
}

// exitErr prints a fatal message and stops with a non-zero status, which is what
// fails the docs build rather than shipping a broken search index.
func exitErr(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
