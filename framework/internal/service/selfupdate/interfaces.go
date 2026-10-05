package selfupdate

import coreupd "github.com/kitsunium/sdk/framework/internal/core/selfupdate"

// Getter performs the HTTP GETs a self-update needs. It aliases the core port.
type Getter = coreupd.Getter

// FileSystem is the disk half of replacing a running binary. It aliases the
// core port.
type FileSystem = coreupd.FileSystem

// Copier streams the verified archive to its destination. It aliases the core
// port.
type Copier = coreupd.Copier
