module github.com/kitsunium/sdk

go 1.26

// The root umbrella module also hosts the opt-in, vendor-dependent integrations
// under third-party/* (ADR 0012). The AWS SDK requires below live HERE, not in
// pkg/v1, so consumers of pkg/v1 (logger / codec / errs) never pull AWS into
// their module graph — nothing requires this root module.
require (
	github.com/aws/aws-sdk-go-v2 v1.41.8
	github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs v1.74.1
	github.com/aws/aws-sdk-go-v2/service/s3 v1.102.1
	github.com/kitsunium/sdk/internal/core v0.0.0
	github.com/kitsunium/sdk/internal/kernel v0.0.0
	github.com/kitsunium/sdk/internal/service v0.0.0
)

require (
	github.com/aws/smithy-go v1.25.1
	golang.org/x/crypto v0.52.0
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.24 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.24 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.24 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.24 // indirect
	golang.org/x/sys v0.45.0 // indirect
)

replace github.com/kitsunium/sdk/internal/kernel => ./internal/kernel

replace github.com/kitsunium/sdk/internal/core => ./internal/core

replace github.com/kitsunium/sdk/internal/service => ./internal/service
