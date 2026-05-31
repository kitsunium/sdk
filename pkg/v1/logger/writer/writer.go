//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/logger/writer .

// Package writer activates the dependency-free logger writers — "console",
// "file", and "rotfile" — by blank-importing their service factory packages. A
// single
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/logger/writer"
//
// registers all three factories so logger.NewMulti can resolve the "console",
// "file", and "rotfile" names. It pulls NO third-party dependencies; the AWS
// writers (s3, cloudwatch) live in separate third-party/aws/writer packages
// that a consumer imports individually when — and only when — they want the AWS
// SDK in their build (ADR 0012).
package writer

import (
	// Registers the "console" writer factory on import.
	_ "github.com/kitsunium/sdk/internal/service/writer/console"
	// Registers the "file" writer factory on import.
	_ "github.com/kitsunium/sdk/internal/service/writer/file"
	// Registers the "rotfile" writer factory on import (ADR 0014).
	_ "github.com/kitsunium/sdk/internal/service/writer/rotfile"
)
