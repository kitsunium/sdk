// Package form — range 0.3.40.* (ADR 0005 service/codec/form block).
package form

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.40.0 - 0.3.40.255

// CodeFormValueInvalid identifies a Marshal / Append / Unmarshal call whose
// argument is not one of the shapes the urlencoded wire format models
// (url.Values, map[string][]string, map[string]string, or a pointer to one).
const CodeFormValueInvalid errs.Code = 0x00_03_28_01 // 0.3.40.1

// CodeFormUnmarshalFailed identifies a decode refusal: a malformed
// percent-escape, the ';' separator net/url rejects since Go 1.17, or an
// input that trips one of the package's decode bounds.
const CodeFormUnmarshalFailed errs.Code = 0x00_03_28_02 // 0.3.40.2

// CodeFormMultiValue identifies a decode into a single-valued target
// (map[string]string) of a body that repeats a key — the one case where the
// requested Go shape cannot hold what the wire carries.
const CodeFormMultiValue errs.Code = 0x00_03_28_03 // 0.3.40.3
