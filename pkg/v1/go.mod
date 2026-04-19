module github.com/kitsunium/sdk/pkg/v1

go 1.26

require (
	github.com/kitsunium/sdk/internal/core v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/internal/kernel v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/internal/service v0.0.0-00010101000000-000000000000
)

require (
	github.com/pelletier/go-toml/v2 v2.3.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/kitsunium/sdk/internal/core => ../../internal/core
	github.com/kitsunium/sdk/internal/kernel => ../../internal/kernel
	github.com/kitsunium/sdk/internal/service => ../../internal/service
)
