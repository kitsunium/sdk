module github.com/kitsunium/sdk/framework/connectors/mysql

go 1.27.1

require (
	github.com/go-sql-driver/mysql v1.10.0
	github.com/kitsunium/sdk/framework v0.11.0
	github.com/kitsunium/sdk/pkg v0.11.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/kitsunium/sdk/internal/core v0.1.16 // indirect
	github.com/kitsunium/sdk/internal/kernel v0.11.0 // indirect
	github.com/kitsunium/sdk/internal/service v0.1.16 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	golang.org/x/mod v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/kitsunium/sdk/framework => ../..

replace github.com/kitsunium/sdk/pkg => ../../../pkg

replace github.com/kitsunium/sdk/internal/core => ../../../internal/core

replace github.com/kitsunium/sdk/internal/kernel => ../../../internal/kernel

replace github.com/kitsunium/sdk/internal/service => ../../../internal/service
