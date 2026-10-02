module github.com/kitsunium/sdk/framework/connectors/postgres

go 1.27.1

require (
	github.com/jackc/pgx/v5 v5.11.0
	github.com/kitsunium/sdk/framework v0.11.0
	github.com/kitsunium/sdk/pkg v0.11.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kitsunium/sdk/internal/core v0.1.16 // indirect
	github.com/kitsunium/sdk/internal/kernel v0.11.0 // indirect
	github.com/kitsunium/sdk/internal/service v0.1.16 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/kitsunium/sdk/framework => ../..

replace github.com/kitsunium/sdk/pkg => ../../../pkg

replace github.com/kitsunium/sdk/internal/core => ../../../internal/core

replace github.com/kitsunium/sdk/internal/kernel => ../../../internal/kernel

replace github.com/kitsunium/sdk/internal/service => ../../../internal/service
