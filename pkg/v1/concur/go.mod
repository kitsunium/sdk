module github.com/kitsunium/sdk/pkg/v1/concur

go 1.27.3

replace github.com/kitsunium/sdk/internal => ../../../internal

replace github.com/kitsunium/sdk/pkg/v1/clock => ../clock

replace github.com/kitsunium/sdk/pkg/v1/errs => ../errs

require (
	github.com/kitsunium/sdk/internal v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/clock v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/errs v0.0.0-00010101000000-000000000000
)
