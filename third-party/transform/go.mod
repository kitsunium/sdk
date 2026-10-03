module github.com/kitsunium/sdk/third-party/transform

go 1.27.1

require (
	github.com/kitsunium/sdk/internal/core v0.1.16
	github.com/kitsunium/sdk/internal/kernel v0.1.16
	github.com/kitsunium/sdk/internal/service v0.1.16
	github.com/klauspost/compress v1.19.2
)

replace github.com/kitsunium/sdk/internal/core => ../../internal/core

replace github.com/kitsunium/sdk/internal/kernel => ../../internal/kernel

replace github.com/kitsunium/sdk/internal/service => ../../internal/service
