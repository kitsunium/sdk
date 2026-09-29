//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/kit/config/toml .

// Package toml lets a kit product read TOML configuration files —
// config/config.toml, config/<env>.toml — as it reads JSON ones. kit reads
// JSON itself; importing this package is the whole of what a product does to
// read TOML, and what links the TOML codec into it:
//
//	import _ "github.com/kitsunium/sdk/framework/kit/config/toml"
//
// A product with a TOML file and without this import is refused at the start,
// naming the import.
package toml

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	tomlcodec "github.com/kitsunium/sdk/pkg/v1/codec/toml"
)

// Importing the package gives kit the TOML codec: a blank package-level value
// rather than an init, the SDK's convention.
var _ = register()

// register gives kit the TOML codec.
func register() bool {
	ikit.RegisterConfigFormat("toml", tomlcodec.Format)
	return true
}
