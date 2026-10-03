module github.com/kitsunium/sdk/framework/connectors/sqlite

go 1.27.1

require (
	github.com/kitsunium/sdk/framework v0.11.0
	github.com/kitsunium/sdk/pkg v0.11.0
	modernc.org/sqlite v1.59.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kitsunium/sdk/internal/core v0.1.16 // indirect
	github.com/kitsunium/sdk/internal/kernel v0.11.0 // indirect
	github.com/kitsunium/sdk/internal/service v0.1.16 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace github.com/kitsunium/sdk/framework => ../..

replace github.com/kitsunium/sdk/pkg => ../../../pkg

replace github.com/kitsunium/sdk/internal/core => ../../../internal/core

replace github.com/kitsunium/sdk/internal/kernel => ../../../internal/kernel

replace github.com/kitsunium/sdk/internal/service => ../../../internal/service
