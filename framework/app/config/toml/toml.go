// Package toml lets a kit product read TOML configuration files —
// config/config.toml, config/<env>.toml — as it reads JSON ones. kit reads
// JSON itself; importing this package is the whole of what a product does to
// read TOML, and what links the TOML codec into it:
//
//	import _ "github.com/kitsunium/sdk/framework/app/config/toml"
//
// A product with a TOML file and without this import is refused at the start,
// naming the import.
package toml

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
	tomlcodec "github.com/kitsunium/sdk/pkg/v1/data/codec/toml"
)

// Importing the package gives kit the TOML codec: a blank package-level value
// rather than an init, the SDK's convention.
var _ = register()

// register gives kit the TOML codec.
func register() bool {
	iapp.RegisterConfigFormat("toml", tomlcodec.Format)
	return true
}
