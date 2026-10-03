module github.com/kitsunium/sdk/framework/connectors/ssh

go 1.27.1

require (
	github.com/kitsunium/sdk/framework v0.11.0
	github.com/kitsunium/sdk/internal/kernel v0.11.0
	golang.org/x/crypto v0.55.0
)

require (
	github.com/kitsunium/sdk/internal/core v0.1.16 // indirect
	github.com/kitsunium/sdk/internal/service v0.1.16 // indirect
	github.com/kitsunium/sdk/pkg v0.11.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/kitsunium/sdk/framework => ../..

replace github.com/kitsunium/sdk/pkg => ../../../pkg

replace github.com/kitsunium/sdk/internal/core => ../../../internal/core

replace github.com/kitsunium/sdk/internal/kernel => ../../../internal/kernel

replace github.com/kitsunium/sdk/internal/service => ../../../internal/service
