// Package main — -check-api: docs/api regenerated in memory and compared byte
// for byte with what is committed, each difference named by symbol.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// maxDriftLines bounds how many differing symbols one document reports.
const maxDriftLines int = 20

// runCheckAPI regenerates docs/api in memory and fails on any difference with
// the committed documents, then on the pin markers and the digests when asked.
// It returns the process's exit status.
func runCheckAPI(o apiOptions, out io.Writer) int {
	root, rerr := realRoot(o.root)
	//: a repository that does not resolve has no API.
	if rerr != nil {
		fmt.Fprintf(out, "genindex: %v\n", rerr)
		//: a failed check.
		return 1
	}
	o.root = root
	api, err := buildAPI(o)
	//: an API that could not be read cannot vouch for docs/api.
	if err != nil {
		fmt.Fprintf(out, "genindex: %v\n", err)
		//: a failed check.
		return 1
	}
	findings, ferr := documentFindings(o.root, api)
	//: a committed document that could not be read.
	if ferr != nil {
		fmt.Fprintf(out, "genindex: %v\n", ferr)
		//: a failed check.
		return 1
	}
	findings = append(findings, surfaceFindings(o, api)...)
	//: nothing differs.
	if len(findings) == 0 {
		fmt.Fprintf(out, "genindex: %s holds the code's API on %d cells (%d documents)%s\n",
			apiDir, len(o.cells), len(api.names), armedSuffix(armedChecks(o)))
		//: a passing check.
		return 0
	}
	//: one line per finding, the remedy last.
	for _, f := range findings {
		fmt.Fprintln(out, f)
	}
	fmt.Fprintf(out, "genindex: %d finding(s)\n", len(findings))
	//: a failing check.
	return 1
}

// surfaceFindings runs the two checks that hold the code to the design: the
// pin markers and the generated files' digests, each when asked.
func surfaceFindings(o apiOptions, api *builtAPI) []string {
	var out []string
	//: the code's symbols per cell against the pins' markers.
	if o.markers {
		out = append(out, markerFindings(api.cells, api.pkgDocs, o.cells, api.dirs, o.root)...)
	}
	//: the generated files against the design files' bytes.
	if o.digests {
		out = append(out, digestFindings(o.root, api.dirs)...)
	}
	//: whatever they found.
	return out
}

// armedChecks names the checks a run makes besides the documents.
func armedChecks(o apiOptions) []string {
	var armed []string
	//: the markers.
	if o.markers {
		armed = append(armed, "pin markers")
	}
	//: the digests.
	if o.digests {
		armed = append(armed, "generated-file digests")
	}
	//: the checks made.
	return armed
}

// armedSuffix says, after a passing run's summary, which other checks held.
func armedSuffix(armed []string) string {
	var b strings.Builder
	//: only when there are some.
	if len(armed) > 0 {
		b.WriteString("; ")
		b.WriteString(strings.Join(armed, " and "))
		b.WriteString(" hold")
	}
	//: the suffix, possibly empty.
	return b.String()
}

// documentFindings compares every regenerated document with the committed
// one: missing, differing or stale.
func documentFindings(root string, api *builtAPI) ([]string, error) {
	var out []string
	//: every document the code writes.
	for _, name := range api.names {
		found, err := documentDrift(root, name, api.docs[name])
		//: a document that could not be compared.
		if err != nil {
			//: as it failed.
			return nil, err
		}
		out = append(out, found...)
	}
	existing, err := existingDocuments(root)
	//: a docs/api that cannot be listed.
	if err != nil {
		//: as the walk said it.
		return nil, err
	}
	//: a committed document no module writes any more.
	for _, name := range existing {
		//: a current module's document was compared above.
		if !slices.Contains(api.names, name) {
			out = append(out, fmt.Sprintf("%s/%s%s: no module writes it any more; %s", apiDir, name, apiExt, apiCheckAdvice))
		}
	}
	//: every difference.
	return out, nil
}

// documentDrift compares one regenerated document with the committed file.
func documentDrift(root, name string, doc *apiDocument) ([]string, error) {
	want, err := encodeDocument(doc)
	//: a document that cannot be encoded.
	if err != nil {
		//: as the encoder said it.
		return nil, err
	}
	rel := apiDir + "/" + name + apiExt
	got, rerr := os.ReadFile(documentPath(root, name))
	//: a module whose document was never written.
	if os.IsNotExist(rerr) {
		//: one finding.
		return []string{fmt.Sprintf("%s: missing; %s", rel, apiCheckAdvice)}, nil
	}
	//: any other failure to read it.
	if rerr != nil {
		//: name the file.
		return nil, fmt.Errorf("read %s: %w", rel, rerr)
	}
	//: byte for byte: anything else is drift.
	if bytes.Equal(got, want) {
		//: no finding.
		return nil, nil
	}
	lines := symbolDrift(got, doc)
	header := fmt.Sprintf("%s: differs from the code; %s", rel, apiCheckAdvice)
	//: the header, then which symbols differ.
	return append([]string{header}, lines...), nil
}

// symbolDrift names the records that differ between a committed document and
// the code's: each one added, removed or changed, up to maxDriftLines, by id
// and the cells it holds on. A committed file that is no document is said to
// be one.
func symbolDrift(committed []byte, doc *apiDocument) []string {
	var old apiDocument
	//: a committed file that does not decode cannot be compared record by
	//: record.
	if err := json.Unmarshal(committed, &old); err != nil {
		//: say so.
		return []string{"  the committed file is no docs/api document: " + err.Error()}
	}
	before := recordsByKey(&old)
	after := recordsByKey(doc)
	var lines []string
	//: every record of the code, against the committed one.
	for _, key := range sortedKeys(after) {
		//: a record that differs or is new.
		if prev, ok := before[key]; !ok || prev != after[key] {
			lines = append(lines, "  "+changeWord(ok)+" "+key)
		}
	}
	//: every committed record the code no longer has.
	for _, key := range sortedKeys(before) {
		//: one the code has was handled above.
		if _, ok := after[key]; !ok {
			lines = append(lines, "  removed "+key)
		}
	}
	//: the document's header fields, when they are what differs.
	if len(lines) == 0 {
		lines = append(lines, "  the records are the same; the header, the order or the formatting differs")
	}
	//: bounded.
	return boundLines(lines)
}

// changeWord says whether a record was changed or is new.
func changeWord(existed bool) string {
	//: a key the committed document has.
	if existed {
		//: its content differs.
		return "changed"
	}
	//: a key it has not.
	return "added  "
}

// recordsByKey maps each record of a document — package or symbol — to its
// encoding, keyed by its id or path and the cells it holds on.
func recordsByKey(doc *apiDocument) map[string]string {
	out := make(map[string]string, len(doc.Packages)+len(doc.Symbols))
	//: every package record.
	for _, p := range doc.Packages {
		out[recordKey("package "+p.Path, p.Platforms)] = encodedRecord(p)
	}
	//: every symbol record.
	for _, s := range doc.Symbols {
		out[recordKey(s.ID, s.Platforms)] = encodedRecord(s)
	}
	//: the records.
	return out
}

// recordKey names a record by what identifies it and its cells.
func recordKey(id string, platforms []string) string {
	//: a record of every cell.
	if len(platforms) == 0 {
		//: its id alone.
		return id
	}
	//: its id and its cells.
	return id + " on " + strings.Join(platforms, ", ")
}

// encodedRecord is a record's encoding, which two records are compared by.
func encodedRecord(rec any) string {
	raw, err := json.Marshal(rec)
	//: a record that was decoded encodes again; a failure is still a value.
	if err != nil {
		//: the failure stands in for the record.
		return "unencodable: " + err.Error()
	}
	//: the encoding.
	return string(raw)
}

// boundLines keeps the first maxDriftLines lines and says how many it left.
func boundLines(lines []string) []string {
	//: few enough to show them all.
	if len(lines) <= maxDriftLines {
		//: all of them.
		return lines
	}
	rest := len(lines) - maxDriftLines
	//: the first ones, then the count of the others.
	return append(lines[:maxDriftLines:maxDriftLines], fmt.Sprintf("  … and %d more", rest))
}
