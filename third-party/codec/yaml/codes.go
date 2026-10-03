// Package yaml — range 0.3.77.* (third-party/codec/yaml block).
package yaml

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.77.0 - 0.3.77.255

// CodeYAMLFullMarshalFailed identifies a failure encoding a value through
// gopkg.in/yaml.v3 (a MarshalYAML hook that failed, or a stream whose writer
// failed).
const CodeYAMLFullMarshalFailed errs.Code = 0x00_03_4D_01 // 0.3.77.1

// CodeYAMLFullUnmarshalFailed identifies a failure decoding through
// gopkg.in/yaml.v3: a syntax error, a value its target cannot hold, or an
// input over the 10 MiB cap.
const CodeYAMLFullUnmarshalFailed errs.Code = 0x00_03_4D_02 // 0.3.77.2
