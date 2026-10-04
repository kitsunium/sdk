module github.com/kitsunium/sdk/third-party/db/writer/redis

go 1.27.1

require (
	github.com/kitsunium/sdk v0.0.0-00010101000000-000000000000
	github.com/redis/go-redis/v9 v9.22.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/kitsunium/sdk => ../../../..
