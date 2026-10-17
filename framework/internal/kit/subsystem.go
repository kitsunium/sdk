package kit

import (
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A subsystem is in a program if and only if the program imports its
// registration package (ADR 0179 §1). At the start, after the declarations
// and before any component, kit asks of each declaration whether the
// subsystem it needs is linked; every one that is not is a diagnostic naming
// the declaration and the package to import, all of them at once, and the
// refusal carries CodeSubsystemMissing (§6). The check runs at the start, not
// at the declaration: a declaration made in a package-level variable runs at
// its package's init, which Go may order before the registration package's.

// storePackage is the package a program imports for its stores and
// databases.
const storePackage = "github.com/kitsunium/sdk/framework/kit/store"

// subsystemPublic is the refusal's public text (ADR 0179 §6).
const subsystemPublic = "a declaration needs a subsystem this program does not import"

// subsystem is one of kit's subsystems as the start's check sees it: its
// name, its registration package, the declarations that need it, and whether
// the program links it.
type subsystem struct {
	// name is the subsystem's, as a diagnostic says it.
	name string
	// pkg is its registration package.
	pkg string
	// kinds are the nodes that need it; databases says whether a database
	// does, and sealing whether a node that seals what it keeps at rest
	// does — a topic, a queued command or a mailer whose values are sealed
	// on disk, under data keys kit keeps in a store of its own.
	kinds     []model.NodeKind
	databases bool
	sealing   bool
	// needs, when set, reports whether the node n, of no kind of kinds,
	// needs the subsystem all the same.
	needs func(a *App, n node) bool
	// linked reports whether the program links the subsystem.
	linked func() bool
}

// subsystems are the subsystems the start checks, one per registration
// package of ADR 0179 §1, each added by the step that takes its subsystem out
// of the core: the stores' since step 3, linked when framework/kit/store set
// plug.Stores and kit's own store code (EnableStores, step 3b); the queues'
// since step 4, linked when framework/kit/queue set plug.Queues. A test sets
// the hooks as a program that does not import the package would see them.
var subsystems = []subsystem{{
	name: "store", pkg: storePackage,
	kinds: []model.NodeKind{model.KindStore}, databases: true, sealing: true,
	linked: storesLinked,
}, {
	name: "queue", pkg: queuePackage,
	kinds: []model.NodeKind{model.KindTopic, model.KindSubscription},
	needs: needsQueue, linked: queuesLinked,
}}

// needsQueue reports whether n needs the queue subsystem beside the topics
// and the subscriptions — a watch is one —: a queued command, and a mailer
// whose outbox is on a database, the queues' SQL broker (ADR 0178 C1).
func needsQueue(a *App, n node) bool {
	switch q := n.(type) {
	case *Mailer:
		return queueWhere(a, &q.nodeBase) == "sql"
	case interface{ queued() bool }:
		return q.queued()
	}
	return false
}

// storesLinked reports whether the program imports framework/kit/store: the
// store subsystem is plugged in, its engines and kit's own store code.
func storesLinked() bool { return plug.Stores.Load() != nil && stores() != nil }

// unlinkedDecl is one declaration whose subsystem the program does not link.
type unlinkedDecl struct {
	node string
	at   *model.Source
	sub  subsystem
}

// unlinkedSubsystems are the subsystems of the table the program does not
// link.
func unlinkedSubsystems() []subsystem {
	var out []subsystem
	for _, s := range subsystems {
		if !s.linked() {
			out = append(out, s)
		}
	}
	return out
}

// unlinkedDeclarations are the declarations of the app whose subsystem the
// program does not link, in declaration order: the nodes of the services it
// mounts — kit's own privacy service apart, which only a product's own
// declarations bring —, then its databases.
func (a *App) unlinkedDeclarations() []unlinkedDecl {
	missing := unlinkedSubsystems()
	if len(missing) == 0 {
		return nil
	}
	a.privacy.mu.Lock()
	own := a.privacy.svc
	a.privacy.mu.Unlock()
	var out []unlinkedDecl
	for _, svc := range a.services {
		if svc != nil && svc != own {
			out = append(out, a.unlinkedNodes(svc, missing)...)
		}
	}
	for _, d := range a.opts.databases {
		for _, s := range missing {
			if s.databases {
				out = append(out, unlinkedDecl{node: "database:" + d.name, at: a.source(&d.decl), sub: s})
			}
		}
	}
	return out
}

// unlinkedNodes are the nodes of svc that need one of the missing
// subsystems.
func (a *App) unlinkedNodes(svc *Service, missing []subsystem) []unlinkedDecl {
	var out []unlinkedDecl
	nodes, _ := svc.snapshot()
	for _, n := range nodes {
		b := n.base()
		for _, s := range missing {
			if slices.Contains(s.kinds, b.kind) || s.sealing && sealsAtRest(a, n) || s.needs != nil && s.needs(a, n) {
				out = append(out, unlinkedDecl{node: b.id, at: a.source(&b.decl), sub: s})
			}
		}
	}
	return out
}

// sealsAtRest reports whether n seals what it keeps at rest.
func sealsAtRest(a *App, n node) bool {
	s, ok := n.(sealingSource)
	return ok && s.sealsAtRest(a)
}

// subsystemProblems are the start's diagnostics for the declarations whose
// subsystem the program does not link.
func (a *App) subsystemProblems() []model.Diagnostic {
	var out []model.Diagnostic
	for _, u := range a.unlinkedDeclarations() {
		out = append(out, diagnosticOf("error", u.node, u.at,
			say("subsystem.missing", "node", u.node, "subsystem", u.sub.name, "package", u.sub.pkg)))
	}
	return out
}

// missingPackages are the registration packages the start's refusal names —
// a server without framework/kit/server one of them —, sorted, nil when
// none: the refusal's Missing.
func (a *App) missingPackages() []string {
	var pkgs []string
	for _, u := range a.unlinkedDeclarations() {
		pkgs = append(pkgs, u.sub.pkg)
	}
	if len(a.serverProblems()) > 0 {
		pkgs = append(pkgs, serverPackage)
	}
	slices.Sort(pkgs)
	return slices.Compact(pkgs)
}

// Unwrap is what the refusal is, when kit has a code for it: with
// packages in Missing, an error carrying [CodeSubsystemMissing] (ADR
// 0179 §6), which errs.HasCode reads through the refusal; nil
// otherwise.
func (e *DiagnosticsError) Unwrap() error {
	if len(e.Missing) == 0 {
		return nil
	}
	list := strings.Join(e.Missing, ", ")
	return errs.New(CodeSubsystemMissing, "SUBSYSTEM_NOT_IMPORTED", subsystemPublic,
		"kit: the product declares what needs a subsystem whose registration package the program does not import: "+list,
		errs.String("packages", list))
}
