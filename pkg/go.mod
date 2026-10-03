module github.com/kitsunium/sdk/pkg

go 1.27.1

require (
	github.com/kitsunium/sdk/internal/core v0.1.16
	github.com/kitsunium/sdk/internal/kernel v0.1.16
	github.com/kitsunium/sdk/internal/service v0.1.16
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	golang.org/x/mod v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/kitsunium/sdk/internal/core => ../internal/core

replace github.com/kitsunium/sdk/internal/kernel => ../internal/kernel

replace github.com/kitsunium/sdk/internal/service => ../internal/service
