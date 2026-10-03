module github.com/kitsunium/sdk/third-party/db/writer/mysql

go 1.27.1

require (
	github.com/go-sql-driver/mysql v1.10.0
	github.com/kitsunium/sdk/internal/core v0.1.16
	github.com/kitsunium/sdk/internal/kernel v0.1.16
	github.com/kitsunium/sdk/internal/service v0.1.16
)

require filippo.io/edwards25519 v1.2.0 // indirect

replace github.com/kitsunium/sdk/internal/core => ../../../../internal/core

replace github.com/kitsunium/sdk/internal/kernel => ../../../../internal/kernel

replace github.com/kitsunium/sdk/internal/service => ../../../../internal/service
