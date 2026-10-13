module github.com/kitsunium/sdk/third-party/db/writer/mysql

go 1.27.2

require (
	github.com/go-sql-driver/mysql v1.10.0
	github.com/kitsunium/sdk/internal v0.0.0-00010101000000-000000000000
)

require filippo.io/edwards25519 v1.2.0 // indirect

replace github.com/kitsunium/sdk/internal => ../../../../internal
