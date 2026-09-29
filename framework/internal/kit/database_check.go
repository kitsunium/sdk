// Package kit — the start's judgement of the declared databases.
package kit

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/secret"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// The databases' declaration problems (ADR 0004), every one at once, each at
// its kit.Database: a name out of the grammar, taken twice, or one of whose
// derived names a setting or a secret already has; no engine; two defaults;
// a thing two databases keep, or that the app does not mount; a store kept
// in memory that a database keeps by name; migrations on SQLite, which the
// SDK cannot lock yet. Then, once the environment's stores are open, each
// URL: set nowhere outside dev, unreadable, not one its engine reads, or
// leaving TLS to the driver outside dev.

// databaseCheck is one judgement of the declared databases: what it found,
// and what it learns as it goes.
type databaseCheck struct {
	a   *App
	out []model.Diagnostic
	// mounted and stores are the app's services and their stores.
	mounted map[*Service]bool
	stores  map[*nodeBase]placementSource
	// owners take the names a database may not; seen, defaults and keptBy
	// are what the databases judged so far declared.
	owners   map[string]phrase
	seen     map[string]bool
	defaults []string
	keptBy   map[kept]string
}

// databaseProblems judges the declared databases and their URLs.
func (a *App) databaseProblems() []model.Diagnostic {
	if len(a.opts.databases) == 0 {
		return nil
	}
	c := a.newDatabaseCheck()
	for _, d := range a.opts.databases {
		c.name(d)
		c.engine(d)
		c.keeps(d)
	}
	return append(c.out, a.databaseURLProblems()...)
}

// newDatabaseCheck knows what the app mounts, and the names its settings
// and secrets take.
func (a *App) newDatabaseCheck() *databaseCheck {
	c := &databaseCheck{
		a: a, mounted: map[*Service]bool{}, stores: map[*nodeBase]placementSource{},
		owners: map[string]phrase{}, seen: map[string]bool{}, keptBy: map[kept]string{},
	}
	for _, svc := range a.services {
		if svc != nil {
			c.mounted[asKept(svc)] = true
		}
	}
	for _, s := range a.placedStores() {
		c.stores[s.base()] = s
	}
	for name, owner := range a.derivedOwners() {
		c.owners[name] = plain(owner)
	}
	return c
}

// add says a problem of d, at its kit.Database.
func (c *databaseCheck) add(d *database, said phrase) {
	c.out = append(c.out, diagnosticOf("error", "", c.a.source(&d.decl), said))
}

// name judges d's name: its grammar, kit's prefix, a second use, and the
// names it derives.
func (c *databaseCheck) name(d *database) {
	switch {
	case secret.ValidateName(d.name) != nil || len(d.name) > maxDatabaseName:
		c.add(d, say("database.name", "name", d.name, "max", maxDatabaseName))
	case strings.HasPrefix(d.name, kitSecretPrefix):
		c.add(d, say("database.kit-prefix", "name", d.name, "prefix", kitSecretPrefix))
	case c.seen[d.name]:
		c.add(d, say("database.twice", "name", d.name))
	default:
		c.derived(d)
	}
	c.seen[d.name] = true
}

// derived judges the names d derives — its URL's, its settings' —: none may
// be taken already.
func (c *databaseCheck) derived(d *database) {
	for _, name := range d.derivedNames() {
		if owner, taken := c.owners[name]; taken {
			c.add(d, say("database.name-taken", "database", d.name, "name", name, "owner", owner))
			continue
		}
		c.owners[name] = say("database.owner", "name", d.name)
	}
}

// engine judges d's engine and its migrations, and whether it is a second
// default.
func (c *databaseCheck) engine(d *database) {
	if d.engine == nil {
		c.add(d, say("database.nil-engine", "name", d.name))
	}
	if !d.opts.keepsGiven {
		c.defaults = append(c.defaults, d.name)
		if len(c.defaults) == 2 {
			c.add(d, say("database.two-defaults", "first", c.defaults[0], "second", d.name))
		}
	}
	if len(d.opts.migrations) > 0 && d.engine != nil && d.dialect() == sql.DialectSQLite {
		c.add(d, say("database.sqlite-migrations", "name", d.name))
	}
}

// keeps judges what d keeps: each thing mounted, and kept by one database.
func (c *databaseCheck) keeps(d *database) {
	for _, k := range d.opts.keeps {
		key := safeKept(k)
		what, ok := c.kept(d, key)
		if !ok {
			continue
		}
		if first, twice := c.keptBy[key]; twice && first != d.name {
			c.add(d, say("database.kept-twice", "what", what, "first", first, "second", d.name))
			continue
		}
		c.keptBy[key] = d.name
	}
}

// kept judges one thing d keeps: a service or a store the app mounts, and a
// store not kept in memory.
func (c *databaseCheck) kept(d *database, k kept) (what phrase, ok bool) {
	switch {
	case k.svc != nil:
		what, ok = say("database.what-service", "service", k.svc.name), c.mounted[k.svc]
	case k.store != nil:
		s, found := c.stores[k.store]
		what, ok = say("database.what-store", "store", k.store.id), found
		if found && s.memoryOnly() {
			c.add(d, say("database.memory-store", "database", d.name, "store", k.store.id))
		}
	case k.module != nil:
		what, ok = say("database.what-module", "module", k.module.name), c.a.moduleOf(k.module) != nil
	default:
		c.add(d, say("database.nil-keep", "database", d.name))
		return what, false
	}
	if !ok {
		c.add(d, say("database.unmounted", "database", d.name, "what", what))
	}
	return what, ok
}

// derivedOwners are the names the product's settings and secrets take, and
// who takes each: a database may take none of them.
func (a *App) derivedOwners() map[string]string {
	owners := map[string]string{}
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		svc.mu.Lock()
		list := slices.Clone(svc.settings)
		svc.mu.Unlock()
		for _, s := range list {
			owners[flatKey(s.base().key())] = svc.name + "/setting/" + s.base().name
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if s, ok := n.(*Secret); ok {
				owners[s.stored()] = s.id
			}
		}
	}
	return owners
}

// derivedNames are the names the app's databases take, whatever their
// problems: a setting or a secret of the product under one of them is
// judged by databaseProblems, not twice.
func (a *App) derivedNames() map[string]bool {
	out := map[string]bool{}
	for _, d := range a.opts.databases {
		for _, name := range d.derivedNames() {
			out[name] = true
		}
	}
	return out
}

// databaseURLProblems judges each database's URL where the environment keeps
// it, and says that the stores a database keeps stay in the data directory
// in this version of kit. The URL is never quoted. With the app in memory no
// URL is read: no database opens.
func (a *App) databaseURLProblems() []model.Diagnostic {
	if a.opts.memory {
		return nil
	}
	var out []model.Diagnostic
	for _, d := range a.opts.databases {
		if d.engine == nil {
			continue
		}
		at := a.source(&d.decl)
		if p := a.urlProblem(d, at); p != nil {
			out = append(out, *p)
		}
		if n := a.storesKeptBy(d); n > 0 {
			out = append(out, diagnosticOf("warning", "", at, say("database.stores-on-files", "database", d.name, "count", n)))
		}
	}
	return out
}

// urlProblem is what is wrong with d's URL, or nil: set nowhere — an error
// naming the variable outside dev, a warning in dev, where its stores stay
// in the data directory —, unreadable, not one its engine reads, or leaving
// TLS to the driver outside dev.
func (a *App) urlProblem(d *database, at *model.Source) *model.Diagnostic {
	variable := a.urlVariable(d)
	u, _, err := a.databaseURL(context.Background(), d)
	var p model.Diagnostic
	switch {
	case errors.Is(err, secret.NotFound) && a.cfg.env == EnvDev:
		p = diagnosticOf("warning", "", at, say("database.url-dev", "database", d.name, "variable", variable))
	case errors.Is(err, secret.NotFound):
		p = diagnosticOf("error", "", at, say("database.url-missing", "database", d.name, "variable", variable, "app", a.name, "secret", d.urlSecret()))
	case err != nil:
		p = diagnosticOf("error", "", at, say("database.url-unreadable", "database", d.name, "detail", errs.PublicOf(err)))
	default:
		refused := a.urlRefusal(d, u, variable)
		if refused.empty() {
			return nil
		}
		p = diagnosticOf("error", "", at, refused)
	}
	return &p
}

// urlRefusal is what d's engine, or the TLS rule, refuses of its URL; the
// empty phrase when nothing.
func (a *App) urlRefusal(d *database, u secret.Value, variable string) phrase {
	desc, err := d.engine.Describe(u)
	switch {
	case err != nil:
		return say("database.url-invalid", "database", d.name, "variable", variable, "engine", engineTitle(d.engineName()))
	case a.tlsLeft(&desc):
		return say("database.tls", "database", d.name, "variable", variable, "engine", engineTitle(d.engineName()))
	}
	return phrase{}
}

// tlsLeft reports a URL that leaves TLS to the driver where kit asks it
// written: over a network, outside dev.
func (a *App) tlsLeft(u *DatabaseURLValue) bool {
	return u.Networked && u.TLS == "" && !u.Plaintext && a.cfg.env != EnvDev
}
