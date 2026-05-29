module github.com/kitsunium/sdk/internal/service

go 1.26

require (
	github.com/kitsunium/sdk/internal/core v0.0.0-00010101000000-000000000000
	github.com/kitsunium/sdk/internal/kernel v0.0.0-00010101000000-000000000000
)

require gopkg.in/yaml.v3 v3.0.1

require (
	github.com/fxamacker/cbor/v2 v2.9.1
	github.com/pelletier/go-toml/v2 v2.3.0
	github.com/vmihailenco/msgpack/v5 v5.4.1
)

require (
	github.com/kr/text v0.2.0 // indirect
	github.com/stretchr/testify v1.8.0 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace (
	github.com/kitsunium/sdk/internal/core => ../core
	github.com/kitsunium/sdk/internal/kernel => ../kernel
)
