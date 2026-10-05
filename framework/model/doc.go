// A doc comment's default text and its translations.

// Package model is the Product Graph: the one data shape every part of the
// framework agrees on. The runtime serves it, a static analyzer produces it,
// a CLI prints it, a Studio draws it, and an AI agent or an editor extension
// reads it.
//
// A graph is a projection: of the code for what runs, and — since the
// platform's ADR 0010 — of the design (a product's design/ directory) for
// what its structure must be. The design, not the graph, is authoritative on
// structure; the graph shows what the code is and where it differs from the
// design. Every node says where it was declared ([Source]) and every edge
// says how it is known: declared by construction ([Edge].Declared), found in
// a handler body by a static analyzer ([Edge].Static), or seen at runtime
// ([Edge].Observed). A diagram that cannot say why an arrow exists is a
// drawing; this one can.
//
// # Identity
//
// Every node has an ID, and the grammar of IDs is written here once — as
// patterns a schema generator can read ([IDPattern], [SegmentPattern],
// [NamePattern]) and as a parser ([ParseID]):
//
//	todos                              a service
//	moderation.intake                  a module's service
//	todos/endpoint/Create              a node of a service
//	todos/endpoint/GET /todos/{id}     an endpoint named by its route
//	binary:statusline                  a binary
//	binary:statusline/role/daemon      one of its process roles
//	library:textwidth                  a library shared between components
//	external                           the world outside the product
//
// The runtime, the analyzer, the design files and the generator derive names
// with the same functions ([NodeID], [QualifiedService], [RoleID]), so two
// sides never disagree about what a declaration is called.
//
// # A contract
//
// The JSON shape is a contract: [Version] is bumped on any change an older
// reader would misread.
package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

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
	return core.SplitDoc(text)
}
