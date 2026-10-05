// Package yaml lets a kit product read YAML configuration files —
// config/config.yaml, config/<env>.yaml, and .yml — as it reads JSON ones.
// kit reads JSON itself; importing this package is the whole of what a
// product does to read YAML, and what links the YAML codec into it:
//
//	import _ "github.com/kitsunium/sdk/framework/kit/config/yaml"
//
// A product with a YAML file and without this import is refused at the start,
// naming the import.
package yaml

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	yamlcodec "github.com/kitsunium/sdk/pkg/v1/data/codec/yaml"
)

// Importing the package gives kit the YAML codec: a blank package-level value
// rather than an init, the SDK's convention.
var _ = register()

// register gives kit the YAML codec for both of its extensions.
func register() bool {
	ikit.RegisterConfigFormat("yaml", yamlcodec.Format)
	ikit.RegisterConfigFormat("yml", yamlcodec.Format)
	return true
}
