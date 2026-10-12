module github.com/kitsunium/sdk/pkg/v1/proc

go 1.27.2

replace github.com/kitsunium/sdk/internal => ../../../internal

replace github.com/kitsunium/sdk/pkg/v1/errs => ../errs

require (
	github.com/kitsunium/sdk/internal v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/pkg/v1/errs v0.0.0-00010101000000-000000000000
)
