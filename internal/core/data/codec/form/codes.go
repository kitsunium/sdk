// Package form declares the error codes and the sentinels of the
// application/x-www-form-urlencoded codec, internal/service/data/codec/form —
// range 0.3.40.* (ADR 0005 service/data/codec/form block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
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
