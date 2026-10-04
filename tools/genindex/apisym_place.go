// Package main — where a package sits in the SDK: its layer, and its family
// within the layer (ADR 0155).
package main

import (
	"slices"
	"strings"
)

// familyRoot is the family of a layer's root packages: the single packages
// that sit at the layer's top with no family directory above them.
const familyRoot string = "root"

// layerRule places the packages under one directory of the repository.
type layerRule struct {
	// dir is the layer's directory, relative to the repository's root.
	dir string
	// name is the layer's name, as the SDK's design writes it.
	name string
	// roots are the layer's root packages: their family is familyRoot.
	roots []string
	// families is set for a layer grouped by family: the first directory
	// below the layer names a package's family.
	families bool
}

// pkgPlace is a package's layer and family; both empty outside every layer.
type pkgPlace struct {
	// layer is the layer's name.
	layer string
	// family is the family's name, or familyRoot.
	family string
}

// layerRules are the SDK's layers, in the dependency order the layer firewall
// checks (ADR 0068, ADR 0147, ADR 0157): kernel → core → service → pkg/v1 →
// framework, third-party above all but the framework. The kernel and pkg/v1
// name their root packages (ADR 0155, ADR 0159 §4); every other first
// directory of a grouped layer is a family. The connectors under framework/
// are the framework's.
var layerRules = []layerRule{
	{dir: "internal/kernel", name: "kernel", roots: []string{"backoff", "clock", "errs", "plugin", "semver"}, families: true},
	{dir: "internal/core", name: "core", families: true},
	{dir: "internal/service", name: "service", families: true},
	{dir: "pkg/v1", name: "public", roots: []string{"clock", "errs"}, families: true},
	{dir: "third-party", name: "thirdparty"},
	{dir: "framework", name: "framework"},
}

// placeOf places a package by its directory relative to the repository's
// root, in slashes. A package outside every layer has no place.
func placeOf(repoDir string) pkgPlace {
	//: the first layer whose directory holds the package.
	for _, rule := range layerRules {
		rest, ok := strings.CutPrefix(repoDir, rule.dir)
		//: not this layer, or a directory that merely shares its prefix.
		if !ok || rest != "" && !strings.HasPrefix(rest, "/") {
			continue
		}
		place := pkgPlace{layer: rule.name}
		//: a grouped layer gives its packages a family.
		if family, grouped := familyOf(rule, strings.TrimPrefix(rest, "/")); grouped {
			place.family = family
		}
		//: the layer, and its family when it has one.
		return place
	}
	//: outside every layer.
	return pkgPlace{}
}

// familyOf is the family of a package at rest below a layer's directory: the
// first directory, or familyRoot for a root package. ok is false for a layer
// not grouped by family, and for the layer's own directory.
func familyOf(rule layerRule, rest string) (family string, ok bool) {
	//: a layer without families, or a package at the layer's top.
	if !rule.families || rest == "" {
		//: no family.
		return "", false
	}
	first, _, _ := strings.Cut(rest, "/")
	//: a root package is its own family's single member.
	if slices.Contains(rule.roots, first) {
		//: the root family.
		return familyRoot, true
	}
	//: the family's directory.
	return first, true
}
