module github.com/kitsunium/sdk/third-party/x-crypto

go 1.27.1

require (
	github.com/kitsunium/sdk/internal/core v0.1.16
	github.com/kitsunium/sdk/internal/kernel v0.1.16
	github.com/kitsunium/sdk/internal/service v0.1.16
	golang.org/x/crypto v0.55.0
)

require golang.org/x/sys v0.47.0 // indirect

replace github.com/kitsunium/sdk/internal/core => ../../internal/core

replace github.com/kitsunium/sdk/internal/kernel => ../../internal/kernel

replace github.com/kitsunium/sdk/internal/service => ../../internal/service
