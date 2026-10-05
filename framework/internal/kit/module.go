package kit

import (
	"strings"

	"github.com/kitsunium/sdk/framework/model"
)

// Modules (ADR 0008). A module is a Go module's services, released together
// and mounted by a product like a bundle: one line in its main, nothing in
// the products that do not want it.
//
//	var Module = kit.NewModule("moderation", "Reports and moderation.", intake.Service, desk.Service)
//
//	var App = kit.NewApp("shop", catalog.Service).With(moderation.Module)
//
// Everything a module declares is qualified with its name, by construction:
// its services are "<module>.<service>" — the one named like the module is
// "<module>" —, so are its nodes and its data directories; its settings and
// secrets are "<module>.<name>" (the variable <APP>_<MODULE>_<NAME>, the
// section "<module>:" of a configuration file); its routes are served under
// its prefix, "/<module>/" unless [Mount] says otherwise ([Prefix]).

// ModuleConfigurer is what a module is made of: its services, the modules it
// requires ([Requires]), and the migrations of the data it keeps outside
// kit's stores ([Migrations]).
type ModuleConfigurer interface {
	moduleConfigure(m *Module)
}

// reservedModules are names no module may take: kit's own, the node
// standing for the outside world, and "schema", whose version table would
// be the product's own, schema_migrations (module_database.go).
var reservedModules = map[string]bool{"kit": true, model.ExternalID: true, "schema": true}

// NewModule declares a module named name — lower-case letters, digits and
// dashes, starting with a letter; "kit", "external" and "schema" are kit's — that doc
// describes, made of parts: its services, which it adopts, the modules it
// requires ([Requires]), and the migrations of the data it keeps outside
// kit's stores ([Migrations]), run on the database that keeps it. Declare it
// as a package-level variable of the one package a product imports:
//
//	var Module = kit.NewModule("moderation",
//		"Reports and moderation under the DSA.\n\nfr: Signalements et modération selon le DSA.",
//		intake.Service, desk.Service, kit.Requires(identity.Module))
//
// A service a module lists is its own: what it declared so far is renamed,
// and what it declares later is born qualified. Listing a service links its
// package; a service is a module's only, and a module's services take
// declarations from the module's own Go module only.
//
//go:noinline
func NewModule(name, doc string, parts ...ModuleConfigurer) *Module {
	m := &Module{name: name, doc: doc, decl: callerPos()}
	switch {
	case !model.ValidSegment(name):
		m.problem(m.decl, "", "module.name", "name", name)
	case reservedModules[name]:
		m.problem(m.decl, "", "module.reserved", "name", name)
	}
	for _, p := range parts {
		if p == nil {
			m.problem(m.decl, "", "module.nil-part", "module", name)
			continue
		}
		p.moduleConfigure(m)
	}
	return m
}

// Name returns the module's name.
func (m *Module) Name() string { return m.name }

// Prefix is where the app running the module serves its routes — "/<name>/"
// unless the mount says otherwise — and its default before an app runs it.
// A module's user interface, built with relative URLs, finds its API there.
func (m *Module) Prefix() string {
	if p := m.prefix.Load(); p != nil {
		return *p
	}
	return model.ModulePrefix(m.name)
}

// problem records what is wrong with the module's declaration, said at at,
// about node when there is one.
func (m *Module) problem(at pos, node, key string, args ...any) {
	m.diags = append(m.diags, diagnostic{severity: "error", node: node, message: say(key, args...), at: at})
}

// moduleConfigure makes the service one of m's.
func (s *Service) moduleConfigure(m *Module) { m.adopt(s) }

// adopt makes s one of m's services: its name is qualified, and so are the
// IDs of what it declared so far; what it declares later is born qualified.
// A service is one module's only: another's claim is its problem.
func (m *Module) adopt(s *Service) {
	if s == nil {
		m.problem(m.decl, "", "module.nil-part", "module", m.name)
		return
	}
	if s == Privacy || kitOwn(s) {
		m.problem(m.decl, "", "module.kit-service", "module", m.name, "service", s.name)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.module != nil {
		m.problem(m.decl, "", "module.service-taken", "module", m.name, "service", s.name, "other", s.module.name)
		return
	}
	s.module = m
	s.rename(model.QualifiedService(m.name, s.name))
	m.services = append(m.services, s)
}

// rename gives the service its qualified name, and every ID it made so far
// with the old one: its nodes', its settings', its problems'. s.mu is held.
func (s *Service) rename(name string) {
	old := s.name
	s.name = name
	s.ids = make(map[string]bool, len(s.nodes))
	for _, n := range s.nodes {
		b := n.base()
		b.id = model.NodeID(name, b.kind, b.name)
		s.ids[b.id] = true
	}
	for _, st := range s.settings {
		st.base().id = name + "/setting/" + st.base().name
	}
	for i := range s.diags {
		s.diags[i].node = renamed(s.diags[i].node, old, name)
	}
}

// renamed is id with the service old renamed to name.
func renamed(id, old, name string) string {
	if id == old {
		return name
	}
	if rest, ok := strings.CutPrefix(id, old+"/"); ok {
		return name + "/" + rest
	}
	return id
}

// requirement is Requires' part.
type requirement struct{ module *Module }

// moduleConfigure adds the required module to m, or records that it is nil.
func (r requirement) moduleConfigure(m *Module) {
	if r.module == nil {
		m.problem(m.decl, "", "module.nil-requires", "module", m.name)
		return
	}
	m.requires = append(m.requires, r.module)
}

// requires is Requires's body: decl_gen.go writes Requires, from the
// design, as one call of it.
func requires(other *Module) ModuleConfigurer { return requirement{module: other} }

// qualifiedKey is a setting's or a secret's name as a product says it: in a
// configuration file's section, to kit.Set, to the secrets command —
// "<module>.<name>" for a module's service's, name for the product's.
func qualifiedKey(svc *Service, name string) string {
	if svc == nil || svc.module == nil {
		return name
	}
	return svc.module.name + "." + name
}

// flatKey is a qualified key as a store and a variable hold it: the module's
// dot a dash — "moderation-openai-key". Two keys that flatten alike share
// their variable.
func flatKey(key string) string { return strings.ReplaceAll(key, ".", "-") }

// variableOf is the environment variable that carries the setting or the
// secret key under prefix: SHOP_MODERATION_TDB_URL for "moderation.tdb-url".
func variableOf(prefix, key string) string { return secretVariable(prefix, flatKey(key)) }
