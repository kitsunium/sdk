module github.com/kitsunium/sdk/internal/service

go 1.27.1

require (
	github.com/kitsunium/sdk/internal/core v0.1.16
	github.com/kitsunium/sdk/internal/kernel v0.1.16
)

replace (
	github.com/kitsunium/sdk/internal/core => ../core
	github.com/kitsunium/sdk/internal/kernel => ../kernel
)
