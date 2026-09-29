package kit_test

import (
	// The suite's products read YAML and TOML configuration files, as a
	// product does: by importing the packages that read them.
	_ "github.com/kitsunium/sdk/framework/kit/config/toml"
	_ "github.com/kitsunium/sdk/framework/kit/config/yaml"
	// The suite's servers serve HTTP, as a product that imports it does.
	_ "github.com/kitsunium/sdk/framework/kit/server"
	// The suite's dev apps serve the Studio's API, as a product that imports
	// it does.
	_ "github.com/kitsunium/sdk/framework/kit/studio"
)
