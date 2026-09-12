// Package selfupdate — the ports, aliased from core.
//
// They are ALIASES rather than a second declaration: a service-local copy of a
// contract the core layer owns compiles fine and drifts silently, and a caller
// holding one of each would find them interchangeable right up until a method
// is added to one of them.
package selfupdate

import coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"

// Getter performs the HTTP GETs a self-update needs. It aliases the core port.
type Getter = coreupd.Getter

// FileSystem is the disk half of replacing a running binary. It aliases the
// core port.
type FileSystem = coreupd.FileSystem

// Copier streams the verified archive to its destination. It aliases the core
// port.
type Copier = coreupd.Copier
