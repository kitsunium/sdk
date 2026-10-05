// Modules: the services a module adopts, their qualified names, and the files
// of a module.

package core

import (
	"cmp"
	"slices"
	"strings"
)

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
