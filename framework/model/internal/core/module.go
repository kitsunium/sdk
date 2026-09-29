// Modules: the services a module adopts, their qualified names, and the files
// of a module.

package core

import (
	"cmp"
	"slices"
	"strings"
)

// ModuleMessage is a module the app mounts: a Go module's services, released and
// mounted together like a bundle (ADR 0008). Everything a module declares is
// qualified with its name: its services are "<module>.<service>" — the one
// named like the module is "<module>" —, so its nodes are
// "<module>.<service>/<kind>/<name>" and carry [NodeEntity.Module].
type ModuleMessage struct {
	// Name is the name kit.NewModule gives it.
	Name string `json:"name"`
	// Doc is its description in the product's default language; Docs the
	// same in other languages, by tag, as a node's.
	Doc  string            `json:"doc,omitempty"`
	Docs map[string]string `json:"docs,omitempty"`
	// Package is the Go package that declares it: the one a product imports.
	Package string `json:"package,omitempty"`
	// Build is the Go module it comes from, and its version.
	Build *ModuleVersionMessage `json:"build,omitempty"`
	// Services are its services' IDs, in the order it lists them.
	Services []string `json:"services"`
	// Requires are the modules it requires (kit.Requires), by name.
	Requires []string `json:"requires,omitempty"`
	// RequiredBy are the mounted modules that require it, by name, sorted.
	RequiredBy []string `json:"requiredBy,omitempty"`
	// Prefix is where its routes are served: "/<name>/" unless the mount
	// says otherwise (kit.Prefix); "/" shares the product's route space.
	Prefix string `json:"prefix"`
	// Source is where kit.NewModule declares it.
	Source *SourceMessage `json:"source,omitempty"`
	// Mount is where the app mounts it — App.With or kit.Mount —; absent
	// when only another module's kit.Requires mounted it, at its defaults.
	Mount *SourceMessage `json:"mount,omitempty"`
}

// FileMessage is one file a graph points at: its path, relative to its root — the
// product's module, or the Go module [SourceMessage.GoModule] names.
type FileMessage struct {
	// GoModule is empty for a file of the product's own module.
	GoModule string `json:"goModule,omitempty"`
	File     string `json:"file"`
}

// ModuleOf returns the module named name, or nil.
func (g *GraphMessage) ModuleOf(name string) *ModuleMessage {
	for i := range g.Modules {
		if g.Modules[i].Name == name {
			return &g.Modules[i]
		}
	}
	return nil
}

// QualifiedService is the name a module gives a service it lists: the
// module's own name for the service named like it, "<module>.<service>"
// otherwise. The runtime and the analyzer both qualify with it.
func QualifiedService(module, service string) string {
	switch {
	case module == "":
		return service
	case service == module:
		return module
	}
	return module + "." + service
}

// ModulePrefix is a module's default mount prefix: "/<name>/".
func ModulePrefix(name string) string { return "/" + name + "/" }

// UnderPrefix is a route declared at path, served under a module's mount
// prefix: "/reports" under "/moderation/" is "/moderation/reports", and
// under "/" it stays "/reports".
func UnderPrefix(prefix, path string) string {
	if prefix == "" || prefix == "/" {
		return path
	}
	return strings.TrimSuffix(prefix, "/") + path
}

// compareFiles orders files by root, then path.
func compareFiles(a, b FileMessage) int {
	return cmp.Or(cmp.Compare(a.GoModule, b.GoModule), cmp.Compare(a.File, b.File))
}

// normalizeModules sorts the modules by name, and what each lists that has
// no order of its own. A module's services are a list, never null: a
// module that lists none serves an empty one.
func normalizeModules(modules []ModuleMessage) {
	slices.SortFunc(modules, func(a, b ModuleMessage) int { return cmp.Compare(a.Name, b.Name) })
	for i := range modules {
		slices.Sort(modules[i].RequiredBy)
		if modules[i].Services == nil {
			modules[i].Services = []string{}
		}
	}
}

// unmountedNodes are the nodes extra finds on a module's service that base
// mounts and lacks: declared by a package the binary does not link — one
// no module or product imports. The merge says so rather than dropping them
// unsaid.
func unmountedNodes(known map[string]bool, extra *GraphMessage) []DiagnosticMessage {
	var out []DiagnosticMessage
	for _, n := range extra.Nodes {
		if known[n.ID] || n.Module == "" || n.Service == "" || !known[n.Service] {
			continue
		}
		out = append(out, DiagnosticMessage{
			Severity: "warning", Node: n.Service, Source: n.Source,
			Message: n.ID + " is declared on module " + n.Module + "'s service " + n.Service +
				" by a package the binary does not link: import it where the module is declared, or it is not part of the product",
		})
	}
	return out
}
