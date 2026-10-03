// Package view — the engine-neutral construction parameters.
package view

import "io/fs"

// DefaultMaxBytes is the ceiling [Config.MaxBytes] clamps to when it is not
// positive: 8 MiB.
//
// A rendered HTML document larger than this is a defect or an attack. The value
// is generous by an order of magnitude for a page a browser is meant to parse,
// and it is a CLAMP rather than a refusal because — unlike a lock's TTL, whose
// two readings are opposites (ADR 0031, ADR 0052) — there is a defensible
// universal answer here, and refusing view.Config{FS: templates} would make the
// obvious spelling unusable for no safety gain.
const DefaultMaxBytes int = 8 << 20

// MaxPooledBytes is the largest scratch buffer the engine keeps for reuse.
//
// A single 8 MiB render must not leave 8 MiB pinned in a pool for the life of
// the process; anything above this is returned to the collector instead.
const MaxPooledBytes int = 1 << 20

// Config parameterises every [Factory]. It is deliberately small: this domain
// ships an extension point, not a template language.
type Config struct {
	// FS is the tree the templates are read from. Required.
	//
	// It is an fs.FS rather than a directory path so that embed.FS, os.DirFS
	// and fstest.MapFS are all first-class, and so that the domain never
	// touches the filesystem API. It also removes path traversal from the
	// problem statement: fs.ValidPath rejects "..", so no template name can
	// address a file outside the tree the caller handed over.
	//
	// Every regular file under FS is parsed, and each template is named by its
	// SLASH PATH relative to the root — "admin/page.html", not "page.html".
	// See [Factory] for why that is not a cosmetic choice.
	FS fs.FS

	// Ext optionally restricts which files are parsed, by extension including
	// the dot (".html", ".gohtml").
	//
	// Empty means every regular file in FS, which is the honest default: the
	// caller already chose the tree, and second-guessing its contents is the
	// SDK inventing a naming convention. Set it when templates share a
	// directory with assets.
	Ext []string

	// MaxBytes caps a single render's output. Not positive clamps to
	// [DefaultMaxBytes]; there is deliberately no spelling for "unlimited".
	//
	// The cap is the only bound that exists. text/template's Execute takes no
	// context, so a render cannot be cancelled, and the stdlib's own guard is a
	// recursion DEPTH limit (100000 frames) that says nothing about output
	// size: a {{range}} over an attacker-influenced collection has no depth at
	// all and will write until the machine stops. The same "no unbounded
	// setting" reasoning applies here as to the WebSocket ceilings in ADR 0047.
	MaxBytes int
}
