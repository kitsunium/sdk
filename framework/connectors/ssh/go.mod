module github.com/kitsunium/sdk/framework/connectors/ssh

go 1.27.2

require (
	github.com/kitsunium/sdk v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/internal v0.0.0-00010101000000-000000000000
	golang.org/x/crypto v0.55.0
)

require (
	github.com/kitsunium/sdk/pkg/v1/app v0.0.0-00010101000000-000000000000 // indirect
	github.com/kitsunium/sdk/pkg/v1/crypto v0.0.0-00010101000000-000000000000 // indirect
	github.com/kitsunium/sdk/pkg/v1/data v0.0.0-00010101000000-000000000000 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/kitsunium/sdk => ../../..

replace github.com/kitsunium/sdk/internal => ../../../internal

replace github.com/kitsunium/sdk/pkg/v1/app => ../../../pkg/v1/app

replace github.com/kitsunium/sdk/pkg/v1/crypto => ../../../pkg/v1/crypto

replace github.com/kitsunium/sdk/pkg/v1/data => ../../../pkg/v1/data
