// A doc comment's default text and its translations.

// Package core is the Product Graph under the names the role rule asks for:
// every struct carries its role (NodeEntity, GraphMessage, EndpointSpec, …).
// framework/model is its public face — an alias per type under the graph's
// own names (Node, Graph, EndpointInfo, …), its constants and its functions —
// and the one package a caller imports.
package core

import (
	"regexp"
	"strings"
	"sync"
)

// docTag starts a paragraph of a description in another language: a
// language tag the SDK's i18n domain supports, then a colon. It is matched on
// a line's start only, so "fr:" inside a sentence changes nothing. It is
// compiled at its first use, not at every start of every product.
var docTag = sync.OnceValue(func() *regexp.Regexp {
	return regexp.MustCompile(`^(ar|de|en|es|fr|it|ja|nl|pl|pt|ru|zh):\s*(.*)$`)
})

// taggedLines gathers a comment's lines by language as [splitTagged] reads
// them: lang is the tag in force, "" before the first one.
type taggedLines struct {
	lang string
	def  []string
	docs map[string][]string
}

// SplitDoc splits a description into its default text and its translations.
//
// A product documents a building block once, in Go, in its default language;
// a paragraph that starts with a language tag and a colon — "fr: Le cycle
// de vie d'un compte…" — is the same description in that language, until
// the next tag or the end:
//
//	// AccountLifecycle is the life of an account…
//	//
//	// fr: AccountLifecycle est la vie d'un compte…
//
// It keeps Go's convention (the comment still starts with the name) and
// go doc prints both. Each text is joined into one line. A description whose
// only tagged paragraph is "en" uses it as the default.
func SplitDoc(text string) (string, map[string]string) {
	def, docs := splitTagged(text)
	out := map[string]string{}
	for tag, lines := range docs {
		if s := joinLines(lines); s != "" {
			out[tag] = s
		}
	}
	d := joinLines(def)
	if d == "" {
		d = out["en"]
	}
	delete(out, "en")
	if len(out) == 0 {
		out = nil
	}
	return d, out
}

// splitTagged sorts a comment's lines: those before the first tag are the
// default text, each tagged paragraph and what follows it its language's.
func splitTagged(text string) (def []string, docs map[string][]string) {
	var parts taggedLines
	for line := range strings.SplitSeq(text, "\n") {
		parts.add(line)
	}
	return parts.def, parts.docs
}

// add files one line under the tag in force, or under the tag it opens.
func (p *taggedLines) add(line string) {
	line = strings.TrimSpace(line)
	if m := docTag().FindStringSubmatch(line); m != nil {
		p.lang, line = m[1], m[2]
	}
	if p.lang == "" {
		p.def = append(p.def, line)
		return
	}
	if p.docs == nil {
		p.docs = map[string][]string{}
	}
	p.docs[p.lang] = append(p.docs[p.lang], line)
}

// joinLines joins lines into one, every run of white space one space.
func joinLines(lines []string) string {
	return strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
}
