module github.com/kitsunium/sdk/pkg/v1/security

go 1.27.2

replace github.com/kitsunium/sdk/internal => ../../../internal

replace github.com/kitsunium/sdk/pkg/v1/clock => ../clock

replace github.com/kitsunium/sdk/pkg/v1/crypto => ../crypto

replace github.com/kitsunium/sdk/pkg/v1/errs => ../errs

replace github.com/kitsunium/sdk/pkg/v1/observe => ../observe

replace github.com/kitsunium/sdk/pkg/v1/proc => ../proc

require (
	github.com/kitsunium/sdk/internal v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/clock v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/crypto v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/errs v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/observe v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/proc v0.0.0-00010101000000-000000000000
)
