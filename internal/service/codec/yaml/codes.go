// Package yaml: codes.go — range 0.3.4.* (ADR 0005 service/codec/yaml block).
package yaml

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.4.0 - 0.3.4.255

// CodeYAMLMarshalFailed identifies a failure inside gopkg.in/yaml.v3.Marshal.
const CodeYAMLMarshalFailed errs.Code = 0x00_03_04_01 // 0.3.4.1

// CodeYAMLUnmarshalFailed identifies a failure inside gopkg.in/yaml.v3.Unmarshal.
const CodeYAMLUnmarshalFailed errs.Code = 0x00_03_04_02 // 0.3.4.2
