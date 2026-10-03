module github.com/kitsunium/sdk/internal/service

go 1.27.1

require (
	github.com/kitsunium/sdk/internal/core v0.1.16
	github.com/kitsunium/sdk/internal/kernel v0.1.16
	github.com/pelletier/go-toml/v2 v2.4.3
	golang.org/x/mod v0.41.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace (
	github.com/kitsunium/sdk/internal/core => ../core
	github.com/kitsunium/sdk/internal/kernel => ../kernel
)
