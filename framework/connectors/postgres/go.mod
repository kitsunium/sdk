module github.com/kitsunium/sdk/framework/connectors/postgres

go 1.27.2

require (
	github.com/jackc/pgx/v5 v5.11.0
	github.com/kitsunium/sdk v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/data v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/errs v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/security v0.0.0-00010101000000-000000000000
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/kitsunium/sdk/internal v0.0.0-00010101000000-000000000000 // indirect
	github.com/kitsunium/sdk/pkg/v1/app v0.0.0-00010101000000-000000000000 // indirect
	github.com/kitsunium/sdk/pkg/v1/clock v0.0.0-00010101000000-000000000000 // indirect
	github.com/kitsunium/sdk/pkg/v1/concur v0.0.0-00010101000000-000000000000 // indirect
	github.com/kitsunium/sdk/pkg/v1/crypto v0.0.0-00010101000000-000000000000 // indirect
	github.com/kitsunium/sdk/pkg/v1/net v0.0.0-00010101000000-000000000000 // indirect
	github.com/kitsunium/sdk/pkg/v1/observe v0.0.0-00010101000000-000000000000 // indirect
	github.com/kitsunium/sdk/pkg/v1/proc v0.0.0-00010101000000-000000000000 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

replace github.com/kitsunium/sdk => ../../..

replace github.com/kitsunium/sdk/internal => ../../../internal

replace github.com/kitsunium/sdk/pkg/v1/app => ../../../pkg/v1/app

replace github.com/kitsunium/sdk/pkg/v1/clock => ../../../pkg/v1/clock

replace github.com/kitsunium/sdk/pkg/v1/concur => ../../../pkg/v1/concur

replace github.com/kitsunium/sdk/pkg/v1/crypto => ../../../pkg/v1/crypto

replace github.com/kitsunium/sdk/pkg/v1/data => ../../../pkg/v1/data

replace github.com/kitsunium/sdk/pkg/v1/errs => ../../../pkg/v1/errs

replace github.com/kitsunium/sdk/pkg/v1/net => ../../../pkg/v1/net

replace github.com/kitsunium/sdk/pkg/v1/observe => ../../../pkg/v1/observe

replace github.com/kitsunium/sdk/pkg/v1/proc => ../../../pkg/v1/proc

replace github.com/kitsunium/sdk/pkg/v1/security => ../../../pkg/v1/security
