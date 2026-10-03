module github.com/kitsunium/sdk/third-party/codec/protobuf

go 1.27.1

require (
	github.com/kitsunium/sdk/internal/core v0.1.16
	github.com/kitsunium/sdk/internal/kernel v0.1.16
	github.com/kitsunium/sdk/internal/service v0.1.16
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/fxamacker/cbor/v2 v2.9.3 // indirect
	github.com/x448/float16 v0.8.4 // indirect
)

replace github.com/kitsunium/sdk/internal/core => ../../../internal/core

replace github.com/kitsunium/sdk/internal/kernel => ../../../internal/kernel

replace github.com/kitsunium/sdk/internal/service => ../../../internal/service
