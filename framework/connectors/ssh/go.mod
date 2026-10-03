module github.com/kitsunium/sdk/framework/connectors/ssh

go 1.27.1

require (
	github.com/kitsunium/sdk v0.0.0-00010101000000-000000000000
	golang.org/x/crypto v0.55.0
)

require golang.org/x/sys v0.47.0 // indirect

replace github.com/kitsunium/sdk => ../../..
