package app

import (
	"slices"

	"github.com/kitsunium/sdk/framework/model"
)

// Mounting a module. A *Module is an AppOption: the app mounts it at its
// defaults. [Mount] mounts it with options — where its routes are served
// ([Prefix]), what its ports call ([Bind]), in which environments the mount
// holds ([Only]). A module the mounted ones require is mounted too, at its
// defaults, when the app does not mount it itself.

// MountConfigurer configures the mount of a module: [Prefix], [Bind], [Only].
type MountConfigurer interface {
	mountConfigure(d *mountDecl)
}

// mountDecl is one mount of a module the app was given.
type mountDecl struct {
	module *Module
	// prefix is Prefix's, "" without one.
	prefix   string
	prefixAt pos
	binds    []bindDecl
	// at is where the app mounts it: the app.Mount call, or the App.With
	// that was given the module.
	at pos
	// limited says Only was given: the mount holds only in the
	// environments only names, none when only is empty. onlyAt is where the
	// first Only was given.
	limited bool
	only    []string
	onlyAt  pos
}

// mountOption is Mount's AppOption.
type mountOption struct{ decl mountDecl }

// appConfigure sets the option on what it configures.
func (o *mountOption) appConfigure(ao *appOptions) {
	ao.mounts = append(ao.mounts, o.decl)
	// The module's bindings join the app's in the order given: the last of
	// a port wins, wherever it was given.
	ao.binds = append(ao.binds, o.decl.binds...)
}

// appConfigure mounts the module at its defaults.
func (m *Module) appConfigure(o *appOptions) {
	o.mounts = append(o.mounts, mountDecl{module: m, at: o.withAt})
}

// Mount mounts module m with options: [Prefix] serves its routes elsewhere
// than "/<module>/", [Bind] chooses what one of its ports calls.
//
//	a := app.NewApp("vigie", identity.Service).
//		With(app.Mount(moderation.Module, app.Prefix("/"), app.Bind(moderation.Enforcer, posts.EnforceAPI)))
//
// A module is mounted once per app: mounted again, its prefix is the last
// one given, and its bindings join the others, the last of a port winning.
//
//go:noinline
func Mount(m *Module, opts ...MountConfigurer) AppConfigurer {
	d := mountDecl{module: m, at: callerPos()}
	for _, o := range opts {
		if o != nil {
			o.mountConfigure(&d)
		}
	}
	for i := range d.binds {
		d.binds[i].scope = m
		d.binds[i].limited, d.binds[i].only = d.limited, d.only
	}
	return &mountOption{decl: d}
}

type prefixOption struct {
	prefix string
	at     pos
}

// mountConfigure sets the option on what it configures.
func (p *prefixOption) mountConfigure(d *mountDecl) { d.prefix, d.prefixAt = p.prefix, p.at }

// Prefix serves a module's routes under prefix — "/trust/" — instead of
// "/<module>/"; "/" shares the product's route space, where a route both
// declare is refused as any two are. A prefix starts and ends with '/', and
// never lies under /_app/, nor under /_kit/ while its probes answer there.
//
//go:noinline
func Prefix(prefix string) MountConfigurer { return &prefixOption{prefix: prefix, at: callerPos()} }

type onlyOption struct {
	envs []string
	at   pos
}

// mountConfigure sets the option on what it configures: the environments of
// every Only given to one mount add up.
func (o *onlyOption) mountConfigure(d *mountDecl) {
	if !d.limited {
		d.limited, d.onlyAt = true, o.at
	}
	d.only = append(d.only, o.envs...)
}

// Only makes a mount hold in the environments envs names alone — the name
// <PRODUCT>_ENV or [Env] gives, lower-cased, "production" when neither does:
//
//	a := app.NewApp("vigie", identity.Service).
//		With(app.Mount(debugbar.Module, app.Only("dev", "staging")))
//
// Elsewhere the mount is as if it had not been given, its [Prefix] and its
// [Bind] with it; another mount of the module without Only still mounts it.
// Several Only on one mount add up. The start refuses an Only that names no
// environment, a name that cannot be one — lower-case letters, digits and
// dashes inside, 63 at most —, and a module a mounted module requires
// ([Requires]) that its mount leaves out, rather than mounting it at its
// defaults behind the product's back.
//
//go:noinline
func Only(envs ...string) MountConfigurer {
	return &onlyOption{envs: slices.Clone(envs), at: callerPos()}
}

// holdsIn reports whether a mount limited to only holds in env.
func holdsIn(limited bool, only []string, env string) bool {
	return !limited || slices.Contains(only, env)
}

// onlyOf are the environments the mounts of m among decls hold in, sorted
// and once each — whatever environment the app runs in —; nil when one of
// them holds in every environment, or none mounts m.
func onlyOf(decls []mountDecl, m *Module) []string {
	var envs []string
	for _, d := range decls {
		if d.module != m {
			continue
		}
		if !d.limited {
			return nil
		}
		envs = append(envs, d.only...)
	}
	slices.Sort(envs)
	return slices.Compact(envs)
}

// mountsIn are the mounts of decls that hold in env, in their order.
func mountsIn(decls []mountDecl, env string) []mountDecl {
	out := make([]mountDecl, 0, len(decls))
	for _, d := range decls {
		if holdsIn(d.limited, d.only, env) {
			out = append(out, d)
		}
	}
	return out
}

// mountEnv is the environment's name the mounts are resolved for: app.Env's,
// else <PRODUCT>_ENV's for the product named name, as resolveConfig reads
// them.
func mountEnv(o *appOptions, product string, getenv func(string) string) string {
	env := ownEnv(product, getenv)(varEnv)
	if o.env != nil {
		env = *o.env
	}
	_, name := resolveEnv(env)
	return name
}

// remount resolves the mounts again for the environment the configuration
// names, when an Only makes it matter and it is not the one App.With read:
// kit's own services the app already runs stay first.
func (a *App) remount(env string) {
	if a.mountEnv == env || !slices.ContainsFunc(a.opts.mounts, func(d mountDecl) bool { return d.limited }) {
		a.mountEnv = env
		return
	}
	own := slices.DeleteFunc(slices.Clone(a.services), func(s *Service) bool { return s == Privacy || !frameworkOwn(s) })
	a.mountEnv = env
	a.modules, a.services = mounts(a.own, mountsIn(a.opts.mounts, env))
	a.services = slices.Concat(own, a.services)
}

// mounts resolves the modules an app mounts, in the order they start —
// the modules others require first, needs before those who need them, then
// the others in the order the app mounts them — and the services the app
// runs, in the same order: the modules', then the product's own.
func mounts(own []*Service, decls []mountDecl) ([]*mountedModule, []*Service) {
	explicit, byModule := explicitMounts(decls)
	ordered := orderModules(explicit, byModule)
	var services []*Service
	for _, mm := range ordered {
		for _, s := range mm.module.services {
			if !slices.Contains(services, s) {
				services = append(services, s)
			}
		}
	}
	for _, s := range own {
		if s == nil || s.module == nil || !slices.Contains(services, s) {
			services = append(services, s)
		}
	}
	return ordered, services
}

// explicitMounts are the modules the app mounts itself, in the order it
// first mounts each: mounted again, a module takes the prefix given last,
// and its last mount's position.
func explicitMounts(decls []mountDecl) ([]*mountedModule, map[*Module]*mountedModule) {
	byModule := map[*Module]*mountedModule{}
	var explicit []*mountedModule
	for _, d := range decls {
		if d.module == nil {
			continue
		}
		mm, seen := byModule[d.module]
		if !seen {
			mm = &mountedModule{module: d.module, prefix: model.ModulePrefix(d.module.name)}
			byModule[d.module] = mm
			explicit = append(explicit, mm)
		}
		if d.prefix != "" {
			mm.prefix, mm.prefixAt = d.prefix, d.prefixAt
		}
		at := d.at
		mm.at = &at
	}
	return explicit, byModule
}

// orderModules adds the modules the mounted ones require, mounted at their
// defaults, and puts every module in start order: the required ones first,
// each after what it requires, then the others in mount order.
func orderModules(explicit []*mountedModule, byModule map[*Module]*mountedModule) []*mountedModule {
	required(explicit, byModule)
	var out []*mountedModule
	placed := map[*Module]bool{}
	var place func(mm *mountedModule)
	place = func(mm *mountedModule) {
		if placed[mm.module] {
			return
		}
		placed[mm.module] = true
		for _, r := range mm.module.requires {
			place(byModule[r])
		}
		if len(mm.requiredBy) > 0 {
			out = append(out, mm)
		}
	}
	for _, mm := range explicit {
		place(mm)
	}
	for _, mm := range explicit {
		if len(mm.requiredBy) == 0 {
			out = append(out, mm)
		}
	}
	return out
}

// required mounts, at its defaults, every module the mounted ones require
// and the app does not mount, and says which mounted modules require each.
func required(explicit []*mountedModule, byModule map[*Module]*mountedModule) {
	pending := slices.Clone(explicit)
	for len(pending) > 0 {
		mm := pending[0]
		pending = pending[1:]
		for _, r := range mm.module.requires {
			req, ok := byModule[r]
			if !ok {
				req = &mountedModule{module: r, prefix: model.ModulePrefix(r.name)}
				byModule[r] = req
				pending = append(pending, req)
			}
			if !slices.Contains(req.requiredBy, mm.module) {
				req.requiredBy = append(req.requiredBy, mm.module)
			}
		}
	}
}

// moduleOf is the module the app mounts that m is, or nil.
func (a *App) moduleOf(m *Module) *mountedModule {
	for _, mm := range a.modules {
		if mm.module == m {
			return mm
		}
	}
	return nil
}

// prefixOf is where the app serves the routes of svc: its module's prefix,
// "" for the product's own.
func (a *App) prefixOf(svc *Service) (string, bool) {
	if a == nil || svc == nil || svc.module == nil {
		return "", false
	}
	if mm := a.moduleOf(svc.module); mm != nil {
		return mm.prefix, true
	}
	return model.ModulePrefix(svc.module.name), true
}

// mountModules tells each module where the app serves it, once its
// services are the app's.
func (a *App) mountModules() {
	for _, mm := range a.modules {
		prefix := mm.prefix
		mm.running = &prefix
		mm.module.prefix.Store(mm.running)
	}
}

// unmountModules forgets it — only what this app said: a module another app
// runs keeps its prefix.
func (a *App) unmountModules() {
	for _, mm := range a.modules {
		if mm.running != nil {
			mm.module.prefix.CompareAndSwap(mm.running, nil)
		}
		mm.running = nil
	}
}

// moduleName is the name of the module that lists the service, "" for the
// product's own.
func (s *Service) moduleName() string {
	if s == nil || s.module == nil {
		return ""
	}
	return s.module.name
}

// describeModules is the modules the app mounts, as the graph says them.
func (a *App) describeModules() []model.Module {
	out := make([]model.Module, 0, len(a.modules))
	for _, mm := range a.modules {
		m := mm.module
		doc, docs := model.SplitDoc(m.doc)
		d := model.Module{
			Name: m.name, Doc: doc, Docs: docs, Package: m.decl.pkg(), Build: a.moduleBuild(m.decl.pkg()),
			Prefix: mm.prefix, Source: a.source(&m.decl), Mount: a.source(mm.at),
		}
		if mm.at != nil {
			d.Only = onlyOf(a.opts.mounts, m)
		}
		for _, s := range m.services {
			d.Services = append(d.Services, s.name)
		}
		for _, r := range m.requires {
			d.Requires = append(d.Requires, r.name)
		}
		for _, r := range mm.requiredBy {
			d.RequiredBy = append(d.RequiredBy, r.name)
		}
		out = append(out, d)
	}
	return out
}
