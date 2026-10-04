// Package main — the command line: the flags, and the mode they select.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

const (
	// modeIndex writes the go/doc symbol index, the docs site's before docs/api.
	modeIndex string = "index"
	// modeDocLinks checks every same-package doc link (ADR 0138).
	modeDocLinks string = "check-doclinks"
	// modeWriteAPI writes docs/api from the code.
	modeWriteAPI string = "write-api"
	// modeCheckAPI regenerates docs/api in memory and fails on any drift.
	modeCheckAPI string = "check-api"
	// modeWriteErrorCodes writes docs/error-codes.yaml from docs/api.
	modeWriteErrorCodes string = "write-error-codes"
	// modeCheckErrorCodes compares docs/error-codes.yaml with docs/api.
	modeCheckErrorCodes string = "check-error-codes"
)

// cliOptions are the parsed flags.
type cliOptions struct {
	// input is the module root the index is extracted from.
	input string
	// output is the index file; empty or - is stdout.
	output string
	// urlBase prefixes every symbol anchor of the index.
	urlBase string
	// module is the module path of input.
	module string
	// repoRoot is the repository's root.
	repoRoot string
	// sourceURLPrefix builds the index's forge links.
	sourceURLPrefix string
	// platforms is the platforms table's path.
	platforms string
	// args are the arguments after the flags.
	args []string
	// checkLinks selects the doc-link check.
	checkLinks bool
	// writeAPI selects writing docs/api.
	writeAPI bool
	// checkAPI selects checking docs/api.
	checkAPI bool
	// markers adds the pin markers to the check.
	markers bool
	// digests adds the generated files' digests to the check.
	digests bool
	// writeErrorCodes selects writing docs/error-codes.yaml.
	writeErrorCodes bool
	// checkErrorCodes selects checking docs/error-codes.yaml.
	checkErrorCodes bool
}

// parseFlags parses the command line's arguments.
func parseFlags(args []string) (*cliOptions, error) {
	o := &cliOptions{}
	fs := flag.NewFlagSet("genindex", flag.ContinueOnError)
	fs.StringVar(&o.input, "input", "", "module root (e.g. ../pkg/v1)")
	fs.StringVar(&o.output, "output", "", "output JSON file (omit for stdout)")
	fs.StringVar(&o.urlBase, "url-base", "/v1/local", "URL prefix for symbol anchors")
	fs.StringVar(&o.module, "module", "github.com/kitsunium/sdk/pkg/v1", "Go module path of -input")
	fs.StringVar(&o.repoRoot, "repo-root", "", "filesystem path of the repo root: the source path of the index, the repository -write-api and -check-api read (defaults to two levels above -input, or above the working directory)")
	fs.StringVar(&o.sourceURLPrefix, "source-url-prefix", "", "if set, build SourceURL = prefix + repo-relative-path#L<line> (e.g. https://github.com/kitsunium/sdk/blob/<sha>/)")
	fs.StringVar(&o.platforms, "platforms", defaultPlatforms, "the platforms table, scripts/ci/platforms.sh: the cells doc links are judged and docs/api is read on")
	fs.BoolVar(&o.checkLinks, "check-doclinks", false, "emit no index; fail on every same-package doc link under the directories given as arguments that names no declared symbol (ADR 0138)")
	fs.BoolVar(&o.writeAPI, "write-api", false, "emit no index; write docs/api/<module>.json for every module of the workspace under -repo-root, read from the code on every cell")
	fs.BoolVar(&o.checkAPI, "check-api", false, "emit no index; regenerate docs/api in memory and fail on any byte that differs from what is committed")
	fs.BoolVar(&o.markers, "markers", false, "with -check-api: compare, on every cell, the code's (id, kind, canonical signature) set with the markers of api_gen*_test.go pin files")
	fs.BoolVar(&o.digests, "digests", false, "with -check-api: check every generated file's header digests against the design files' bytes")
	fs.BoolVar(&o.writeErrorCodes, "write-error-codes", false, "emit no index; write docs/error-codes.yaml from the committed docs/api: every errs.Code constant a package declares")
	fs.BoolVar(&o.checkErrorCodes, "check-error-codes", false, "emit no index; fail when docs/error-codes.yaml is not what -write-error-codes writes from the committed docs/api")
	//: a flag the set does not know; the set printed the usage already.
	if err := fs.Parse(args); err != nil {
		//: as the flag package said it.
		return nil, err
	}
	o.args = fs.Args()
	//: the parsed flags.
	return o, nil
}

// mode is the mode the flags select; the index when none does.
func (o *cliOptions) mode() string {
	//: the first selector set, in a fixed order; usage() refuses two.
	switch {
	//: the doc-link check.
	case o.checkLinks:
		//: it.
		return modeDocLinks
	//: writing docs/api.
	case o.writeAPI:
		//: it.
		return modeWriteAPI
	//: checking docs/api.
	case o.checkAPI:
		//: it.
		return modeCheckAPI
	//: writing docs/error-codes.yaml.
	case o.writeErrorCodes:
		//: it.
		return modeWriteErrorCodes
	//: checking docs/error-codes.yaml.
	case o.checkErrorCodes:
		//: it.
		return modeCheckErrorCodes
	//: no selector.
	default:
		//: the index.
		return modeIndex
	}
}

// usage refuses flags that select two modes, or a check flag without its
// mode. ok is false with the problem.
func (o *cliOptions) usage() (problem string, ok bool) {
	n := 0
	//: count the mode selectors set.
	for _, set := range []bool{o.checkLinks, o.writeAPI, o.checkAPI, o.writeErrorCodes, o.checkErrorCodes} {
		//: one more.
		if set {
			n++
		}
	}
	//: one mode at a time.
	if n > 1 {
		//: refuse.
		return "-check-doclinks, -write-api, -check-api, -write-error-codes and -check-error-codes select one mode each: give one", false
	}
	//: the two surface checks belong to -check-api.
	if (o.markers || o.digests) && !o.checkAPI {
		//: refuse.
		return "-markers and -digests extend -check-api: give it too", false
	}
	//: usable.
	return "", true
}

// runMode runs a checking or writing mode and returns its exit status.
func runMode(o *cliOptions) int {
	//: refuse a contradiction before reading anything.
	if problem, ok := o.usage(); !ok {
		fmt.Fprintln(os.Stderr, "genindex: "+problem)
		//: a usage error.
		return 1
	}
	//: the error codes are read from docs/api, which names its own cells.
	if m := o.mode(); m == modeWriteErrorCodes || m == modeCheckErrorCodes {
		//: their own exit status.
		return runErrorCodesMode(o, os.Stderr)
	}
	cells, err := readPlatforms(o.platforms)
	//: without the cells nothing can be judged.
	if err != nil {
		fmt.Fprintf(os.Stderr, "genindex: %v\n", err)
		//: a failed run.
		return 1
	}
	//: the doc-link check reads its directories from the arguments.
	if o.mode() == modeDocLinks {
		//: its own exit status.
		return runDocLinkCheck(o.args, cells, os.Stderr)
	}
	//: the API modes.
	return runAPIMode(o, cells, os.Stderr)
}

// runAPIMode resolves the repository and runs -write-api or -check-api.
func runAPIMode(o *cliOptions, cells []platform, out io.Writer) int {
	root, ok := defaultRepoRoot(o.repoRoot, o.input)
	//: an API is read from a repository, which must resolve.
	if !ok {
		fmt.Fprintf(out, "genindex: cannot resolve the repository root %q\n", root)
		//: a failed run.
		return 1
	}
	ao := apiOptions{root: root, cells: cells, markers: o.markers, digests: o.digests}
	//: writing.
	if o.mode() == modeWriteAPI {
		//: its exit status.
		return runWriteAPI(ao, out)
	}
	//: checking.
	return runCheckAPI(ao, out)
}

// runErrorCodesMode resolves the repository and runs -write-error-codes or
// -check-error-codes.
func runErrorCodesMode(o *cliOptions, out io.Writer) int {
	root, ok := defaultRepoRoot(o.repoRoot, o.input)
	//: the file is the repository's, which must resolve.
	if !ok {
		fmt.Fprintf(out, "genindex: cannot resolve the repository root %q\n", root)
		//: a failed run.
		return 1
	}
	//: writing.
	if o.mode() == modeWriteErrorCodes {
		//: its exit status.
		return runWriteErrorCodes(root, out)
	}
	//: checking.
	return runCheckErrorCodes(root, out)
}
