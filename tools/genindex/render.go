// Package main — rendering a declaration into the fields a search row carries.
package main

import (
	"fmt"
	"go/ast"
	"go/doc"
	"go/printer"
	"go/token"
	"strings"
)

// signatureTabwidth is the indentation go/printer uses while rendering a
// declaration. The output is collapsed to one line immediately afterwards, so
// the value only decides how the intermediate form looks.
const signatureTabwidth int = 4

// deprecatedMarker is the godoc convention for marking an identifier obsolete:
// a paragraph beginning with it.
const deprecatedMarker string = "Deprecated:"

// renderDecl produces a single-line signature such as
// "func Marshal(format Format, v any) ([]byte, error)".
//
// ok is false when there is nothing to render or the printer refused, which the
// caller turns into an empty Signature rather than a row that claims a
// signature it does not have.
func renderDecl(fset *token.FileSet, node ast.Node) (signature string, ok bool) {
	//: a declaration go/doc did not attach has nothing to print.
	if node == nil {
		//: no signature to offer.
		return "", false
	}
	var sb strings.Builder
	cfg := printer.Config{Mode: printer.UseSpaces, Tabwidth: signatureTabwidth}
	//: the printer only fails on a writer fault, which a strings.Builder cannot
	//: produce — but a refused render must not become a fabricated signature.
	if err := cfg.Fprint(&sb, fset, node); err != nil {
		//: no signature to offer.
		return "", false
	}
	//: collapse multi-line declarations to one line so the index UI can render
	//: the signature without overflowing horizontally.
	return strings.Join(strings.Fields(sb.String()), " "), true
}

// signatureOf renders a declaration, yielding an empty string when it cannot be
// rendered. It exists so a row builder does not have to branch on every field.
func signatureOf(fset *token.FileSet, node ast.Node) string {
	//: an unrenderable declaration carries no signature rather than a wrong one.
	if signature, ok := renderDecl(fset, node); ok {
		//: the collapsed single-line form.
		return signature
	}
	//: nothing to show.
	return ""
}

// synopsis extracts the first sentence per godoc convention.
//
// doc.Synopsis is deprecated in newer Go and doc.Package.Synopsis needs a
// package, which this call site does not have — so the portable equivalent is
// implemented here rather than pulling in additional API surface.
//
// It is deliberately simpler than go/doc's own firstSentence, which inspects
// what PRECEDES a terminator to skip an abbreviation. The cost is that a doc
// comment containing "e.g." yields a slightly short summary; the benefit is a
// rule a reader can hold in their head, for a field that is a search preview
// rather than a contract.
func synopsis(s string) string {
	s = strings.TrimSpace(s)
	//: an undocumented identifier has no synopsis, which is a legitimate value
	//: rather than a failure.
	if s == "" {
		//: nothing to summarise.
		return ""
	}
	//: godoc's "first sentence" ends at the first '.', '!' or '?' that is
	//: followed by whitespace or by the end of the text.
	for i := range len(s) {
		//: only sentence-ending punctuation can close the synopsis.
		if !isSentenceEnd(s[i]) {
			continue
		}
		next := i + 1
		//: a terminator followed by anything else is part of a word — "e.g."
		//: and "v1.2" both rely on this.
		if next == len(s) || isSpace(s[next]) {
			//: the first sentence, trailing space removed.
			return strings.TrimSpace(s[:next])
		}
	}
	//: no sentence terminator at all, so the whole text is the synopsis.
	return strings.TrimSpace(s)
}

// isSentenceEnd reports whether c can close a godoc sentence.
func isSentenceEnd(c byte) bool {
	//: the three terminators godoc recognises.
	return c == '.' || c == '!' || c == '?'
}

// isSpace reports whether c separates words in a doc comment.
func isSpace(c byte) bool {
	//: a doc comment is already normalised, so these three are enough.
	return c == ' ' || c == '\n' || c == '\t'
}

// isDeprecated reports whether a doc comment marks its identifier obsolete.
//
// The marker has to start a paragraph, so a mention of the word inside a
// sentence — "Deprecated: use X" quoted in prose — does not silently mark a
// live symbol as dead.
func isDeprecated(s string) bool {
	//: either the doc opens with the marker, or a later paragraph does.
	return strings.HasPrefix(s, deprecatedMarker) || strings.Contains(s, "\n"+deprecatedMarker)
}

// exampleNames lists the names of the Example functions go/doc attached.
func exampleNames(exs []*doc.Example) []string {
	//: an identifier with no examples carries no field at all, which is what
	//: keeps the shipped JSON small.
	if len(exs) == 0 {
		//: omitempty drops the field entirely.
		return nil
	}
	out := make([]string, 0, len(exs))
	//: one name per attached example, in the order go/doc found them.
	for _, e := range exs {
		out = append(out, e.Name)
	}
	//: the attached example names.
	return out
}

// lastPathSegment returns the final element of a module-relative package path.
//
// ok is false for the module root, whose path has no segment at all — the
// caller substitutes the package's own name there, so a root symbol reads
// "Name" rather than ".Name".
func lastPathSegment(rel string) (segment string, ok bool) {
	//: the module root is spelled "." or "" depending on the caller.
	if rel == "." || rel == "" {
		//: no segment to take.
		return "", false
	}
	idx := strings.LastIndex(rel, "/")
	//: a single-segment path is its own last segment.
	if idx < 0 {
		//: the whole path is the segment.
		return rel, true
	}
	//: everything after the final separator.
	return rel[idx+1:], true
}

// sourceURL builds the forge deep-link for a declaration.
//
// ok is false when no prefix was supplied, when go/doc attached no declaration,
// or when the file lies outside the repository root — a link to a path outside
// the repo would 404 on the forge, which is worse than no link at all.
func sourceURL(opts *indexOptions, fset *token.FileSet, node ast.Node) (link string, ok bool) {
	//: source links are opt-in, and there is nothing to link without a node.
	if opts.sourceURLPrefix == "" || opts.repoRoot == "" || node == nil {
		//: no link for this declaration.
		return "", false
	}
	pos := fset.Position(node.Pos())
	//: a declaration with no position cannot be pointed at.
	if !pos.IsValid() {
		//: no link for this declaration.
		return "", false
	}
	rel, err := filepathRel(opts.repoRoot, pos.Filename)
	//: a path outside the repository would 404 on the forge.
	if err != nil || strings.HasPrefix(rel, "..") {
		//: no link for this declaration.
		return "", false
	}
	//: prefix + repo-relative path + the line anchor the forge understands.
	return strings.TrimSuffix(opts.sourceURLPrefix, "/") + "/" + rel + fmt.Sprintf("#L%d", pos.Line), true
}

// sourceLinkOf builds the forge deep-link, yielding an empty string when there
// is none. It exists so a row builder does not have to branch on every field.
func sourceLinkOf(opts *indexOptions, fset *token.FileSet, node ast.Node) string {
	//: a declaration we cannot point at carries no link rather than a wrong one.
	if link, ok := sourceURL(opts, fset, node); ok {
		//: the forge deep-link.
		return link
	}
	//: nothing to link to.
	return ""
}
