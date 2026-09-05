// Package main — genindex extracts a symbol search index from a Go module's
// public packages and emits it as JSON for the static docs site to consume
// client-side.
//
// The output schema is consumed by docs/site/src/components/Search.astro (via
// MiniSearch) to power the "Symbols" tab of the search modal.
//
// Pipeline: invoked from docs/site/package.json's `prebuild` step, runs strictly
// stdlib (no external deps — zero pollution of any go.sum) per tools/CLAUDE.md's
// "single-purpose scripts" convention.
//
// Usage:
//
//	go run github.com/kitsunium/sdk/tools/genindex \
//	    -input ../pkg/v1 \
//	    -output ../docs/site/src/data/symbols.json \
//	    -url-base /v1/local
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// outputDirPerm is the mode the output directory is created with: readable by
// everyone, writable by the build user. The file itself is served publicly.
const outputDirPerm os.FileMode = 0o755

// stdoutTargets are the -output values that mean "write to stdout" rather than
// to a file of that name.
var stdoutTargets = map[string]struct{}{"": {}, "-": {}}

// main parses the flags, extracts the index and writes it out.
//
// Every failure is fatal and reported on stderr: a partial index is worse than
// none, because the docs site would ship a search box that silently cannot find
// half the API.
func main() {
	input := flag.String("input", "", "module root (e.g. ../pkg/v1)")
	output := flag.String("output", "", "output JSON file (omit for stdout)")
	urlBase := flag.String("url-base", "/v1/local", "URL prefix for symbol anchors")
	module := flag.String("module", "github.com/kitsunium/sdk/pkg/v1", "Go module path of -input")
	repoRoot := flag.String("repo-root", "", "filesystem path of the repo root (used to compute the source path; defaults to two levels above -input)")
	sourceURLPrefix := flag.String("source-url-prefix", "", "if set, build SourceURL = prefix + repo-relative-path#L<line> (e.g. https://github.com/kitsunium/sdk/blob/<sha>/)")
	flag.Parse()

	//: without a module root there is nothing to index.
	if *input == "" {
		exitErr("genindex: -input is required")
	}

	//: an unresolvable root leaves the field empty, which disables source links
	//: rather than emitting ones that point nowhere.
	root, _ := defaultRepoRoot(*repoRoot, *input)
	opts := &indexOptions{
		root:            *input,
		modulePath:      *module,
		urlBase:         *urlBase,
		repoRoot:        root,
		sourceURLPrefix: *sourceURLPrefix,
	}

	syms, err := collect(opts)
	//: a directory that would not parse means the index would be missing
	//: symbols nobody would notice were gone.
	if err != nil {
		exitErr("genindex: %v", err)
	}

	//: a written index that cannot be read back is the same failure as one that
	//: was never written.
	if werr := writeIndex(*output, buildIndex(opts, syms)); werr != nil {
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
