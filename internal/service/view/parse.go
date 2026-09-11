// Package view — construction: the FS walk, full-path template naming, the
// MaxBytes clamp, and the escaping probe that moves a lazily-detected failure
// to boot time.
package view

import (
	"errors"
	"html/template"
	"io"
	"io/fs"
	"path"
	"slices"
	"text/template/parse"

	coreview "github.com/kitsunium/sdk/internal/core/view"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// rootName is the name of the set's anchor template, which is never parsed and
// never executed.
//
// The empty string is chosen because fs.WalkDir cannot produce it: fs.ValidPath
// rejects "" and the walk root arrives as ".", so no file can ever collide with
// the anchor. Render refuses the empty name explicitly all the same, so the
// anchor is unreachable by two independent mechanisms rather than by one
// argument about walk semantics.
const rootName string = ""

// newRenderer parses cfg.FS in full and returns a ready renderer.
func newRenderer(cfg coreview.Config) (built *renderer, err error) {
	//: a nil FS is the one Config field with no defensible default: there is
	//: nothing to render and no tree to guess at (ADR 0031's refuse half).
	if cfg.FS == nil {
		//: refuse at construction; there is no tree to fall back to.
		return nil, raise(coreview.ViewMisconfigured, nil, errs.String("option", "FS"))
	}
	tree := &parser{set: template.New(rootName), cfg: cfg}
	//: a tree that will not read or will not parse is a permanent fault.
	if walkErr := tree.parseTree(); walkErr != nil {
		//: hand the typed verdict straight back; it already names the file.
		return nil, walkErr
	}
	//: a template that calls itself with no condition on the way recurses
	//: forever on every render; refused before the probe would execute it.
	if loopErr := tree.refuseEndlessRecursion(); loopErr != nil {
		//: it names the template the cycle was found at.
		return nil, loopErr
	}
	//: force the escaping plan NOW; see probeEscaping for what that buys.
	if probeErr := tree.probeEscaping(); probeErr != nil {
		//: an unresolvable escaping context is refused, not deferred.
		return nil, probeErr
	}
	//: everything permanent has been decided; the renderer is now immutable.
	return &renderer{set: tree.set, maxBytes: clampMaxBytes(cfg.MaxBytes), count: tree.count}, nil
}

// clampMaxBytes applies the ADR 0031 clamp half: a non-positive ceiling becomes
// the default rather than an error, because unlike a lease TTL there is a
// defensible universal answer for "an HTML document should not exceed N".
func clampMaxBytes(configured int) int {
	//: zero is what a caller who has not met the question yet writes.
	if configured <= 0 {
		//: clamp rather than refuse; the obvious spelling stays usable.
		return coreview.DefaultMaxBytes
	}
	//: the caller stated a ceiling; honour it exactly.
	return configured
}

// parser is the construction-time state: the template set being filled and the
// configuration filling it.
//
// It exists so the walk and the probe are METHODS rather than functions taking
// a *template.Template. That is not cosmetic: a parameter of that type invites
// the suggestion to narrow it to an interface, and text/template.Template
// satisfies every such interface exactly — which is the substitution ADR 0058
// §D2 exists to make impossible.
type parser struct {
	// set is the tree under construction, anchored at the unnamed root.
	set *template.Template
	// cfg is the configuration being honoured.
	cfg coreview.Config
	// count is how many templates have been parsed so far.
	count int
}

// parseTree walks cfg.FS and parses every selected file into the set, naming
// each template by its SLASH PATH relative to the root.
//
// The naming is the load-bearing part. html/template's own ParseFS names each
// template by filepath.Base, so a tree holding admin/page.html and
// user/page.html ends up with ONE template called "page.html" — measured, the
// second parse silently wins, and every request for the admin page renders the
// user page with the admin page's model. There is no error, no warning and no
// way to ask which one you got.
func (p *parser) parseTree() error {
	//: the walk's own error is already typed by parseEntry.
	return fs.WalkDir(p.cfg.FS, ".", p.parseEntry)
}

// parseEntry is fs.WalkDir's callback: one file, parsed under its full path.
func (p *parser) parseEntry(entry string, dirEntry fs.DirEntry, walkErr error) error {
	//: a walk error is the FS refusing to describe itself; it is not a
	//: template problem and is reported as a source failure.
	if walkErr != nil {
		//: name the path that failed; the usual cause is a bad embed pattern.
		return raise(TemplateSourceFailed, walkErr, errs.String("path", entry))
	}
	//: directories carry no template text, and Ext filters the rest.
	if dirEntry.IsDir() || !selected(entry, p.cfg.Ext) {
		//: skip without touching the file.
		return nil
	}
	source, readErr := fs.ReadFile(p.cfg.FS, entry)
	//: a file the FS listed but will not open is still a source failure.
	if readErr != nil {
		//: name the path that failed.
		return raise(TemplateSourceFailed, readErr, errs.String("path", entry))
	}
	//: set.New(entry) creates an ASSOCIATED template, so {{template
	//: "partial/row.html"}} resolves across files in one namespace.
	if _, parseErr := p.set.New(entry).Parse(string(source)); parseErr != nil {
		//: name the template, never its source — the diagnostic is a Field.
		return raise(TemplateParseFailed, parseErr, errs.String("template", entry))
	}
	p.count++
	//: parsed; continue the walk.
	return nil
}

// selected reports whether entry is one of the files to parse. An empty ext
// list means every regular file: the caller already chose the tree, and
// second-guessing its contents is the SDK inventing a naming convention.
func selected(entry string, ext []string) bool {
	//: no filter configured — take the tree as given.
	if len(ext) == 0 {
		//: every regular file is a template.
		return true
	}
	//: path.Ext, not filepath.Ext: an fs.FS path is always slash-separated.
	return slices.Contains(ext, path.Ext(entry))
}

// refuseEndlessRecursion refuses a template that reaches itself again through
// {{template}} calls with no condition on the path.
//
// Such a template recurses on every render until text/template stops it at
// its depth limit, 100 000 calls down, and the render fails — so it is a
// defect of the TREE, and it is refused with the tree's other defects. Only a
// call at a template's top level counts: one inside an {{if}}, a {{range}} or a
// {{with}} may never run, which is how a legitimate recursion over a nested
// model ends. The graph is read from the parse trees before anything
// executes, rather than recognised by the stdlib's error text afterwards.
func (p *parser) refuseEndlessRecursion() error {
	calls := make(map[string][]string, p.count)
	names := make([]string, 0, p.count)
	//: one edge list per template: the calls it makes whatever its data.
	for _, each := range p.set.Templates() {
		//: the anchor, and a template with no body, call nothing.
		if each.Tree == nil || each.Tree.Root == nil {
			continue
		}
		calls[each.Name()] = unconditionalCalls(each.Tree.Root)
		names = append(names, each.Name())
	}
	//: sorted, so the template a refusal names does not depend on map order.
	slices.Sort(names)
	//: a cycle anywhere in the graph is a template that never returns.
	for _, name := range names {
		//: depth-first from each template, the path so far kept on the side.
		if reachesItself(name, name, calls, make(map[string]bool, len(names))) {
			//: the template the cycle was found at; its members are its calls.
			return raise(TemplateParseFailed, nil, errs.String("template", name),
				errs.String("why", "it calls itself through {{template}} with no condition on the way"))
		}
	}
	//: every call chain ends.
	return nil
}

// unconditionalCalls lists the templates list calls at its own top level —
// the calls that happen whatever the data is.
func unconditionalCalls(list *parse.ListNode) []string {
	var called []string
	//: nested lists belong to {{if}}, {{range}} and {{with}}, and are skipped.
	for _, node := range list.Nodes {
		//: a {{template}} (or a {{block}}, parsed into one) calls by name.
		if call, isCall := node.(*parse.TemplateNode); isCall {
			called = append(called, call.Name)
		}
	}
	//: the unconditional callees, in document order.
	return called
}

// reachesItself reports whether following calls from current leads back to
// origin. seen stops a walk from re-entering a template it already expanded.
func reachesItself(origin, current string, calls map[string][]string, seen map[string]bool) bool {
	//: each callee is one more step down the recursion.
	for _, callee := range calls[current] {
		//: back at the start: the recursion has no way out.
		if callee == origin {
			//: a cycle through origin.
			return true
		}
		//: a template already expanded on this walk adds nothing new.
		if seen[callee] {
			continue
		}
		seen[callee] = true
		//: follow the callee's own unconditional calls.
		if reachesItself(origin, callee, calls, seen) {
			//: the cycle closes further down.
			return true
		}
	}
	//: every path from here ends.
	return false
}

// probeEscaping forces html/template to build each template's contextual
// escaping plan at CONSTRUCTION, and turns a failure into a refused Renderer.
//
// This is not belt-and-braces. html/template resolves escaping lazily, at a
// template's first Execute, and three failure classes only exist at that
// moment — measured, with their ErrorCode:
//
//   - ErrEndContext (4) — a template that ends inside an unterminated tag or
//     attribute, e.g. `<a href="{{.}}`.
//   - ErrBranchEnd (3) — an {{if}} whose branches end in different contexts,
//     e.g. `<script>{{if .}}var x = "{{end}}</script>`.
//   - ErrNoSuchTemplate (5) — a {{template "x"}} naming something absent from
//     the set, which is what a renamed partial looks like.
//
// Left alone, all three are a 500 on whichever request first reaches that page,
// and the third is the everyday one: a partial gets renamed, every test that
// exercises another page passes, and the failure ships.
//
// Executing against nil data is deliberately harmless. No custom functions are
// registered (helpers are out of scope for the domain), so nothing user-written
// runs; a nil model calls no methods, {{range}} yields zero iterations, and the
// output goes to io.Discard. Escaping is idempotent and cached, so the probe
// leaves every template ready for its first real render.
func (p *parser) probeEscaping() error {
	//: every parsed template gets its plan built now, once.
	for _, each := range p.set.Templates() {
		//: the anchor has no body and is never rendered.
		if each.Name() == rootName {
			continue
		}
		execErr := each.Execute(io.Discard, nil)
		//: ONLY an escaping failure is fatal. Executing with a nil model can
		//: legitimately fail — a field chain on nothing — and refusing on that
		//: would make every template that reads its model unusable.
		if escapeErr, isEscape := errors.AsType[*template.Error](execErr); isEscape {
			//: name the template; the escaper's own state machine is a Field.
			return raise(TemplateParseFailed, escapeErr, errs.String("template", each.Name()))
		}
	}
	//: every template now carries a resolved escaping plan.
	return nil
}
