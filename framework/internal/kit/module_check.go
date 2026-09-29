// Package kit — the start's checks of the modules an app mounts.
package kit

import (
	"fmt"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/secret"
)

// What the start refuses of the modules an app mounts, every problem at
// once, both declarations named: collisions are refused, never resolved.

// moduleProblems is every problem with the modules the app mounts: their
// own declarations, two with one name, a prefix, a service the product
// mounts itself or a name the product took, and a declaration made on a
// module's service from outside its Go module.
func (a *App) moduleProblems() []model.Diagnostic {
	var out []model.Diagnostic
	for _, d := range a.opts.mounts {
		if d.module == nil {
			out = append(out, diagnosticOf("error", "", a.source(&d.at), say("mount.nil")))
		}
	}
	names := map[string]*Module{}
	for _, mm := range a.modules {
		m := mm.module
		for _, d := range m.diags {
			out = append(out, diagnosticOf(d.severity, d.node, a.source(&d.at), d.message))
		}
		if first, dup := names[m.name]; dup {
			out = append(out, diagnosticOf("error", "", a.source(&m.decl),
				say("module.twice", "module", m.name, "first", a.where(&first.decl), "second", a.where(&m.decl))))
		}
		names[m.name] = m
		out = append(out, a.prefixProblems(mm)...)
		out = append(out, a.foreignDeclarations(m)...)
		out = append(out, a.secretNameProblems(m)...)
	}
	out = append(out, a.moduleMigrationProblems()...)
	return append(out, a.ownProblems(names)...)
}

// where says a position in a sentence: its file and line.
func (a *App) where(p *pos) string {
	s := a.source(p)
	if s == nil {
		return "?"
	}
	if s.GoModule != "" {
		return fmt.Sprintf("%s/%s:%d", s.GoModule, s.File, s.Line)
	}
	return fmt.Sprintf("%s:%d", s.File, s.Line)
}

// prefixProblems judges the prefix a mount gives: '/' first and last, and
// nowhere under /_kit/.
func (a *App) prefixProblems(mm *mountedModule) []model.Diagnostic {
	p := mm.prefix
	if strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/") && !strings.HasPrefix(p, "/_kit/") && !strings.ContainsAny(p, " \t?#{}") {
		return nil
	}
	return []model.Diagnostic{diagnosticOf("error", "", a.source(&mm.prefixAt), say("mount.prefix", "module", mm.module.name, "prefix", p))}
}

// ownProblems judges the product's own services against the mounted
// modules: one a module lists is mounted by mounting the module, and a
// service or a setting of the product never takes a mounted module's name —
// its configuration section, its services' IDs.
func (a *App) ownProblems(modules map[string]*Module) []model.Diagnostic {
	var out []model.Diagnostic
	for _, s := range a.own {
		switch {
		case s == nil:
		case s.module != nil:
			out = append(out, diagnosticOf("error", s.name, a.source(&a.decl),
				say("module.service-own", "service", s.name, "module", s.module.name, "at", a.where(&s.module.decl))))
		case modules[s.name] != nil:
			out = append(out, diagnosticOf("error", s.name, a.source(&s.decl),
				say("module.name-service", "service", s.name, "at", a.where(&modules[s.name].decl))))
		default:
			out = append(out, a.settingNamedLikeAModule(s, modules)...)
		}
	}
	return out
}

// settingNamedLikeAModule refuses a setting of the product's service s
// named like a mounted module: its key would be the module's section.
func (a *App) settingNamedLikeAModule(s *Service, modules map[string]*Module) []model.Diagnostic {
	var out []model.Diagnostic
	s.mu.Lock()
	settings := slices.Clone(s.settings)
	s.mu.Unlock()
	for _, d := range settings {
		b := d.base()
		if m := modules[b.name]; m != nil {
			out = append(out, diagnosticOf("error", s.name, a.source(&b.decl),
				say("module.name-setting", "key", b.name, "service", s.name, "at", a.where(&m.decl))))
		}
	}
	return out
}

// foreignBuilders refuses a builder called on one of m's operations from
// outside m's Go module — a permission, a rule, a key given by a product: a
// product neither re-authorizes nor keys a module's command; it dispatches
// it, or asks the query, from an endpoint of its own (ADR 0005).
func (a *App) foreignBuilders(m *Module, home goModule, n node) []model.Diagnostic {
	b, ok := n.(interface{ builders() []builderCall })
	if !ok {
		return nil
	}
	var out []model.Diagnostic
	calls := b.builders()
	for i := range calls {
		at := &calls[i].at
		if from, ok := goModuleOf(at.pkg); ok && from.path != home.path {
			out = append(out, diagnosticOf("error", n.base().id, a.source(at),
				say("module.foreign-builder", "node", n.base().id, "builder", calls[i].name, "module", m.name, "gomodule", home.path)))
		}
	}
	return out
}

// foreignPolicies refuses a password policy put on one of m's stores from
// outside m's Go module: a policy says what the store's field holds and how
// it changes, so a module's store takes the policies of its own Go module
// only (ADR 0007, ADR 0008). A product sets, changes and verifies a
// module's passwords through a policy the module declares.
func (a *App) foreignPolicies(m *Module, home goModule, n node) []model.Diagnostic {
	st, ok := n.(interface{ policies() []pos })
	if !ok {
		return nil
	}
	var out []model.Diagnostic
	for _, at := range st.policies() {
		if from, ok := goModuleOf(at.pkg); ok && from.path != home.path {
			out = append(out, diagnosticOf("error", n.base().id, a.source(&at),
				say("module.foreign-passwords", "store", n.base().id, "at", a.where(&n.base().decl), "module", m.name, "gomodule", home.path)))
		}
	}
	return out
}

// foreignDeclarations refuses what is declared on one of m's services from
// outside m's Go module: a product reaches a module through its ports,
// commands, topics and settings, never by adding to its services. Nothing is
// judged when the binary does not say its Go modules.
func (a *App) foreignDeclarations(m *Module) []model.Diagnostic {
	home, ok := goModuleOf(m.decl.pkg)
	if !ok {
		return nil
	}
	var out []model.Diagnostic
	foreign := func(at *pos, what string, s *Service) {
		if from, ok := goModuleOf(at.pkg); ok && from.path != home.path {
			out = append(out, diagnosticOf("error", what, a.source(at),
				say("module.foreign", "node", what, "service", s.name, "module", m.name, "gomodule", home.path)))
		}
	}
	for _, s := range m.services {
		foreign(&s.decl, s.name, s)
		nodes, _ := s.snapshot()
		for _, n := range nodes {
			foreign(&n.base().decl, n.base().id, s)
			out = append(out, a.foreignBuilders(m, home, n)...)
			out = append(out, a.foreignPolicies(m, home, n)...)
		}
		s.mu.Lock()
		settings := slices.Clone(s.settings)
		s.mu.Unlock()
		for _, d := range settings {
			foreign(&d.base().decl, d.base().id, s)
		}
	}
	return out
}

// secretNameProblems refuses a module's secret whose qualified name is too
// long to be kept: "<module>-<name>" is its name in a store.
func (a *App) secretNameProblems(m *Module) []model.Diagnostic {
	var out []model.Diagnostic
	for _, svc := range m.services {
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			s, ok := n.(*Secret)
			if ok && secret.ValidateName(s.name) == nil && secret.ValidateName(s.stored()) != nil {
				out = append(out, diagnosticOf("error", s.id, a.source(&s.decl),
					say("secret.qualified-name", "key", s.key(), "stored", s.stored(), "max", secret.MaxNameLen)))
			}
		}
	}
	return out
}
