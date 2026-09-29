// Package kit — the architecture the graph draws: where each store keeps its
// data.
package kit

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
)

// The architecture's own elements: the product's user, and its process.
const (
	// archUser is the person who uses the product.
	archUser string = "person:user"
	// archProcess is the container that runs the product.
	archProcess string = "container:process"
)

// The architecture: the product at the two outer levels of the C4 model,
// derived from the declarations like the rest of the graph. A frontend or an
// auth handler implies a person; a mailer, the relay it sends through; public
// endpoints, the clients that call them; the data directory, a volume; a
// database, its own container. Locations are shown in dev only, and a
// location is never a credential.

// memoryOnly reports whether the store keeps its data in memory whatever
// the app's data directory.
func (s *StoreService[T]) memoryOnly() bool { return s.inMemory }

// architecture derives the architecture of g, the graph App.Graph built.
func (a *App) architecture(g *model.Graph) *model.Architecture {
	arch := &model.Architecture{People: []model.Person{}, Systems: []model.System{}, Containers: []model.Container{}, Links: []model.Link{}}
	dev := a.cfg.env == EnvDev
	an := archNodesOf(g)
	if p, ok := an.user(a.name); ok {
		arch.People = append(arch.People, p)
	}
	arch.Containers = append(arch.Containers, a.processContainer(g, an, dev))
	for _, id := range an.frontends {
		arch.Containers = append(arch.Containers, a.spaContainer(g.Node(id), dev))
	}
	volume, memory := a.dataNodes(g)
	arch.Containers = append(arch.Containers, a.dataContainers(volume, memory, dev)...)
	databases, databaseLinks := a.databaseContainers(g, archProcess)
	arch.Containers = append(arch.Containers, databases...)
	// Systems, and the links to them.
	an.addMail(arch, dev)
	an.addClients(arch)
	slices.SortStableFunc(arch.Systems, func(x, y model.System) int { return cmp.Compare(x.ID, y.ID) })
	// People and frontends to the process, then the process to its data.
	an.addUserLinks(arch, g)
	arch.Links = append(arch.Links, dataLinks(g, volume, memory)...)
	arch.Links = append(arch.Links, databaseLinks...)
	return arch
}

// archNodes are the nodes of a graph the architecture draws, by the part
// they play in it.
type archNodes struct {
	services, frontends, publicEndpoints, auths, captured []string
	// smtp are the mailers by the relay they send through, and smtpTLS the
	// relays' TLS modes.
	smtp    map[string][]string
	smtpTLS map[string]string
}

// archNodesOf sorts g's nodes by the part they play in the architecture.
func archNodesOf(g *model.Graph) *archNodes {
	an := &archNodes{smtp: map[string][]string{}, smtpTLS: map[string]string{}}
	for i := range g.Nodes {
		an.add(&g.Nodes[i])
	}
	return an
}

// add files n by the part it plays.
func (an *archNodes) add(n *model.Node) {
	switch n.Kind {
	case model.KindService:
		an.services = append(an.services, n.ID)
	case model.KindFrontend:
		an.frontends = append(an.frontends, n.ID)
	case model.KindEndpoint:
		if n.Endpoint != nil && n.Endpoint.Expose == model.ExposePublic {
			an.publicEndpoints = append(an.publicEndpoints, n.ID)
		}
	case model.KindAuth:
		an.auths = append(an.auths, n.ID)
	case model.KindMailer:
		an.addMailer(n)
	default:
		// Every other kind plays no part in the architecture drawn here.
	}
}

// addMailer files a mailer by where its mail goes: an SMTP relay, or the
// capture.
func (an *archNodes) addMailer(n *model.Node) {
	if n.Mailer == nil || n.Mailer.Transport != model.TransportSMTP {
		an.captured = append(an.captured, n.ID)
		return
	}
	an.smtp[n.Mailer.Server] = append(an.smtp[n.Mailer.Server], n.ID)
	an.smtpTLS[n.Mailer.Server] = n.Mailer.TLS
}

// user is the person who uses the product, when it has a frontend or signs
// its callers in.
func (an *archNodes) user(product string) (model.Person, bool) {
	switch {
	case len(an.frontends) > 0:
		return model.Person{ID: archUser, Name: "User", Doc: "Uses " + product + " in a web browser."}, true
	case len(an.auths) > 0:
		return model.Person{ID: archUser, Name: "User", Doc: "Signs in and uses " + product + " through its API."}, true
	default:
		return model.Person{}, false
	}
}

// processContainer is the process, first of the containers. kit's own
// listener is kit's; the process speaks HTTP for the product only when the
// product declares it.
func (a *App) processContainer(g *model.Graph, an *archNodes, dev bool) model.Container {
	productHTTP := slices.ContainsFunc(g.Connectors, func(c model.Connector) bool { return c.ID == model.ConnectorHTTP })
	technology := goRelease() + " · kit"
	if productHTTP {
		technology += " · net/http"
	}
	proc := model.Container{
		ID: archProcess, Name: a.name, Kind: model.ContainerProcess,
		Technology: technology,
		Doc:        processDoc(len(an.services), len(an.publicEndpoints), len(an.frontends) > 0),
		Nodes:      an.services,
		Ports:      a.ports(productHTTP),
		Source:     a.source(&a.decl),
	}
	if dev {
		proc.Location = a.listenAddr()
	}
	return proc
}

// spaContainer is the single-page app the frontend n serves.
func (a *App) spaContainer(n *model.Node, dev bool) model.Container {
	spa := model.Container{
		ID: "container:spa:" + n.ID, Name: n.Name, Kind: model.ContainerSPA,
		Technology: "single-page app · static files",
		Doc:        "Runs in the user's browser; the process serves its files and its API.",
		Nodes:      []string{n.ID},
		Source:     n.Source,
	}
	if n.Frontend == nil {
		return spa
	}
	spa.Doc = fmt.Sprintf("Runs in the user's browser; the process serves its %d files under %s, and its API.", n.Frontend.Files, n.Frontend.Prefix)
	if dev && a.listenAddr() != "" {
		spa.Location = "http://" + a.listenAddr() + n.Frontend.Prefix
	}
	return spa
}

// dataContainers are where the stores keep their data: the data directory,
// and the process's memory.
func (a *App) dataContainers(volume, memory []string, dev bool) []model.Container {
	var out []model.Container
	if len(volume) > 0 {
		c := model.Container{
			ID: "container:volume", Name: "data", Kind: model.ContainerVolume,
			Technology: "JSON files · atomic writes · file queues",
			Doc:        "The data directory: each store is a JSON document replaced atomically on every write; each subscription and outbox is a file queue.",
			Nodes:      volume,
			Settings:   []string{model.VarDataDir},
		}
		if dev {
			c.Location = a.nearRoot(a.dataDir)
		}
		out = append(out, c)
	}
	if len(memory) > 0 {
		out = append(out, model.Container{
			ID: "container:memory", Name: "memory", Kind: model.ContainerMemory,
			Technology: "in-process maps · memory queues",
			Doc:        "Data kept in the process's memory only: lost when it stops.",
			Nodes:      memory,
		})
	}
	return out
}

// addMail adds the SMTP relays the mailers send through, and the capture
// that keeps what the others send, with the process's links to them.
func (an *archNodes) addMail(arch *model.Architecture, dev bool) {
	relays := slices.Sorted(maps.Keys(an.smtp))
	for _, relay := range relays {
		id := "system:smtp"
		if len(relays) > 1 {
			id = "system:smtp:" + relay
		}
		technology := "SMTP" + tlsText(an.smtpTLS[relay])
		arch.Systems = append(arch.Systems, model.System{
			ID: id, Name: "SMTP relay", Doc: "Delivers the mail the product sends.",
			Technology: technology, Location: relay, Nodes: an.smtp[relay],
		})
		arch.Links = append(arch.Links, model.Link{From: archProcess, To: id, Label: "Sends mail through", Technology: technology, Nodes: an.smtp[relay], Connectors: []string{model.ConnectorMail}})
	}
	if len(an.captured) == 0 {
		return
	}
	name, doc := "Mail capture", "Keeps every mail the product sends and delivers none: set KIT_SMTP_URL to deliver."
	if dev {
		name, doc = "Studio mailbox (dev capture)", "Keeps every mail the product sends, for the Studio's mailbox; delivers none."
	}
	arch.Systems = append(arch.Systems, model.System{ID: "system:mailbox", Name: name, Doc: doc, Technology: "in-memory capture", Nodes: an.captured})
	arch.Links = append(arch.Links, model.Link{From: archProcess, To: "system:mailbox", Label: "Captures mail in", Technology: "in-process", Nodes: an.captured, Connectors: []string{model.ConnectorMail}})
}

// addClients adds the programs that call the product's public API.
func (an *archNodes) addClients(arch *model.Architecture) {
	if len(an.publicEndpoints) == 0 {
		return
	}
	arch.Systems = append(arch.Systems, model.System{
		ID: "system:clients", Name: "API clients", Doc: "Programs that call the product's public HTTP API.",
		Technology: "JSON over HTTP", Nodes: an.publicEndpoints,
	})
	arch.Links = append(arch.Links, model.Link{From: "system:clients", To: archProcess, Label: "Calls", Technology: "JSON over HTTP", Nodes: an.publicEndpoints, Connectors: []string{model.ConnectorHTTP}})
}

// addUserLinks links the user to the frontends, and them to the process — or
// the user to the process straight, when there is no frontend.
func (an *archNodes) addUserLinks(arch *model.Architecture, g *model.Graph) {
	for _, id := range an.frontends {
		spa := "container:spa:" + id
		arch.Links = append(arch.Links, model.Link{From: archUser, To: spa, Label: "Uses", Technology: "web browser", Nodes: []string{id}})
		arch.Links = append(arch.Links, model.Link{From: spa, To: archProcess, Label: "Calls the API of", Technology: "JSON over HTTP", Nodes: calledFrom(g, id), Connectors: []string{model.ConnectorHTTP}})
	}
	if len(an.frontends) == 0 && len(an.auths) > 0 {
		arch.Links = append(arch.Links, model.Link{From: archUser, To: archProcess, Label: "Signs in and calls", Technology: "JSON over HTTP", Nodes: an.auths, Connectors: []string{model.ConnectorHTTP}})
	}
}

// dataLinks link the process to where it keeps data.
func dataLinks(g *model.Graph, volume, memory []string) []model.Link {
	var out []model.Link
	if len(volume) > 0 {
		out = append(out, model.Link{From: archProcess, To: "container:volume", Label: "Reads and writes", Technology: "file I/O · atomic rename", Nodes: volume, Connectors: connectorsOn(g, volume)})
	}
	if len(memory) > 0 {
		out = append(out, model.Link{From: archProcess, To: "container:memory", Label: "Keeps data in", Technology: "in-process", Nodes: memory, Connectors: connectorsOn(g, memory)})
	}
	return out
}

// databaseContainers are the app's databases, a container each —
// container:database:<name>, drawn as a cylinder — and the process's link to
// each, "Reads and writes". A store is drawn where its data is: on its
// database once kit keeps stores on SQL (ADR 0004, step 2); until then, and
// in dev without the database's URL, in the data directory. The link carries
// the database connector, and the store connector once stores live on it.
func (a *App) databaseContainers(g *model.Graph, process string) ([]model.Container, []model.Link) {
	runs := map[string]*databaseRun{}
	for _, r := range a.databaseRuns() {
		runs[r.d.name] = r
	}
	var containers []model.Container
	var links []model.Link
	for _, d := range a.opts.databases {
		var state string
		var url DatabaseURLValue
		if r := runs[d.name]; r != nil {
			state = r.describe().State
			r.mu.Lock()
			url = r.url
			r.mu.Unlock()
		}
		on := storesOn(g, d)
		containers = append(containers, a.databaseContainer(d, &url, state, on))
		links = append(links, databaseLink(process, d, &url, on))
	}
	return containers, links
}

// storesOn are the stores whose data lives on d: kept by it, on its
// engine.
func storesOn(g *model.Graph, d *database) []string {
	var on []string
	for i := range g.Nodes {
		if n := &g.Nodes[i]; n.Store != nil && n.Store.Database == d.name && model.IsEngine(n.Store.Backend) {
			on = append(on, n.ID)
		}
	}
	return on
}

// databaseContainer is d's container: its engine and, once it is open, its
// driver, its TLS mode and — in dev — where it is.
func (a *App) databaseContainer(d *database, url *DatabaseURLValue, state string, on []string) model.Container {
	engine := d.engineName()
	c := model.Container{
		ID: "container:database:" + d.name, Name: d.name, Kind: model.ContainerDatabase, Engine: engine,
		Technology: engineTitle(engine),
		Doc:        databaseDoc(d.name, a.storesKeptBy(d), len(on), state),
		Nodes:      on,
		Settings:   a.databaseVariables(d),
		Source:     a.source(&d.decl),
	}
	if url.Driver == "" {
		return c
	}
	c.Technology += " · " + url.Driver
	if tls := tlsWords(url); tls != "" {
		c.Technology += " · " + tls
	}
	if a.cfg.env == EnvDev {
		c.Location = url.location()
	}
	return c
}

// databaseLink is the process's link to d, "Reads and writes": the
// database connector, and the store connector once stores live on it.
func databaseLink(process string, d *database, url *DatabaseURLValue, on []string) model.Link {
	technology := "SQL"
	if url.Driver != "" {
		technology += " · " + url.Driver
	}
	connectors := []string{model.ConnectorDatabase}
	if len(on) > 0 {
		connectors = append(connectors, model.ConnectorStore)
	}
	return model.Link{From: process, To: "container:database:" + d.name, Label: "Reads and writes", Technology: technology, Nodes: on, Connectors: connectors}
}

// databaseVariables are the variables that configure a database: its URL
// and its tuning.
func (a *App) databaseVariables(d *database) []string {
	prefix := appPrefix(a.name)
	out := []string{a.urlVariable(d)}
	for _, s := range d.settingDecls() {
		out = append(out, secretVariable(prefix, s.base().name))
	}
	return out
}

// databaseDoc says what a database is to the product.
func databaseDoc(name string, kept, on int, state string) string {
	switch {
	case state == model.DatabaseUnset:
		return fmt.Sprintf("Database %q has no URL in dev: the %s it keeps stay in the data directory.", name, plural(kept, "store"))
	case kept > on:
		return fmt.Sprintf("Database %q: its migrations and its check run on it; the %s it keeps stay in the data directory until kit keeps stores on SQL.", name, plural(kept-on, "store"))
	}
	return fmt.Sprintf("Database %q keeps %s.", name, plural(on, "store"))
}

// ports are what the process listens on: kit's own listener, which answers
// the health probes, the Studio in dev, and the product's HTTP when it
// declares any. Its address is drawn in dev only, like every location: a
// production graph says what the process exposes, never where.
func (a *App) ports(productHTTP bool) []model.Port {
	serves := []string{model.ServesHealth}
	if a.cfg.studio.on {
		serves = append(serves, model.ServesStudio)
	}
	if productHTTP {
		serves = append(serves, model.ServesHTTP)
	}
	port := model.Port{
		Owner: model.PortOwnerKit, Serves: serves,
		Settings: []string{model.VarAddr, model.VarAllowedHosts, model.VarTrustProxy},
	}
	if a.cfg.env == EnvDev {
		port.Address = a.listenAddr()
	}
	return []model.Port{port}
}

// connectorsOn names the connectors that bring any of nodes: what a link to
// the resource holding them is made of. Sorted, as g.Connectors is. The
// database connector is a database's link's alone: a store a database keeps
// whose data stays in the data directory does not make the directory a
// database.
func connectorsOn(g *model.Graph, nodes []string) []string {
	var out []string
	for _, c := range g.Connectors {
		if c.ID == model.ConnectorDatabase {
			continue
		}
		if slices.ContainsFunc(c.Nodes, func(n string) bool { return slices.Contains(nodes, n) }) {
			out = append(out, c.ID)
		}
	}
	return out
}

// nearRoot spells a path relative to the product when it lies inside it —
// ".kit/data" rather than a home directory.
func (a *App) nearRoot(p string) string {
	if a.root == "" || p == "" {
		return p
	}
	if rel, err := filepath.Rel(a.root, p); err == nil && filepath.IsLocal(rel) {
		return rel
	}
	return p
}

// dataNodes splits the nodes that keep data between the data directory and
// memory: stores, subscriptions and queued commands (their queues) and
// mailers (their outbox).
func (a *App) dataNodes(g *model.Graph) (volume, memory []string) {
	for i := range g.Nodes {
		n := &g.Nodes[i]
		switch {
		case !keepsData(n):
		case a.onDisk(n):
			volume = append(volume, n.ID)
		default:
			memory = append(memory, n.ID)
		}
	}
	return volume, memory
}

// keepsData reports whether n keeps data of its own: a store, a
// subscription's queue or a queued command's, a mailer's outbox.
func keepsData(n *model.Node) bool {
	switch n.Kind {
	case model.KindStore, model.KindSubscription, model.KindMailer:
		return true
	case model.KindCommand:
		return n.Command != nil && n.Command.Mode == model.ModeQueued
	default:
		// Every other kind plays no part in the architecture drawn here.
	}
	return false
}

// onDisk reports whether what n keeps is in the data directory: there is
// one, and n is neither a store kept in memory nor a mailer whose outbox
// says otherwise.
func (a *App) onDisk(n *model.Node) bool {
	if n.Kind == model.KindStore {
		if s, ok := a.findNode(n.ID).(interface{ memoryOnly() bool }); ok && s.memoryOnly() {
			return false
		}
	}
	if n.Kind == model.KindMailer && n.Mailer != nil && n.Mailer.Outbox != "" {
		return n.Mailer.Outbox == "file"
	}
	return a.dataDir != ""
}

// calledFrom lists the endpoints a frontend's pages were seen calling; the
// frontend itself when none was seen yet.
func calledFrom(g *model.Graph, frontend string) []string {
	var out []string
	for _, e := range g.Edges {
		if e.From == frontend && e.Kind == model.EdgeCalls {
			out = append(out, e.To)
		}
	}
	if len(out) == 0 {
		return []string{frontend}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// listenAddr is where the app listens, or will.
func (a *App) listenAddr() string {
	a.mu.Lock()
	addr := a.addr
	a.mu.Unlock()
	return cmp.Or(addr, a.cfg.addr)
}

// goRelease spells the Go version the product was built with: "Go 1.27.1".
func goRelease() string {
	v := runtime.Version()
	if rest, ok := strings.CutPrefix(v, "go"); ok {
		return "Go " + rest
	}
	return v
}

// processDoc says what the process is.
func processDoc(services, endpoints int, web bool) string {
	s := fmt.Sprintf("One Go binary running %s: it serves %s", plural(services, "service"), plural(endpoints, "public endpoint"))
	if web {
		s += " and the web app"
	}
	return s + ", and runs the daemon's loops."
}

// plural spells n of word, adding an s unless n is one.
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// tlsText spells an SMTP relay's TLS mode.
func tlsText(mode string) string {
	switch mode {
	case "starttls":
		return " · STARTTLS"
	case "implicit":
		return " · TLS"
	case "none":
		return " · no TLS"
	default:
		// A mode kit does not know says nothing of TLS.
	}
	return ""
}
