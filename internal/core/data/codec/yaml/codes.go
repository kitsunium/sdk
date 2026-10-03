// Package yaml declares the error codes and the sentinels of the YAML codec,
// internal/service/data/codec/yaml — range 0.3.4.* (ADR 0005
// service/data/codec/yaml block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
package yaml

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.4.0 - 0.3.4.255

// CodeYAMLMarshalFailed identifies a value the encoder cannot write as YAML:
// an unsupported Go kind, a string that is not UTF-8, a key that is not a
// scalar, nesting past the bound, a MarshalYAML hook that failed, or a stream
// whose writer failed.
const CodeYAMLMarshalFailed errs.Code = 0x00_03_04_01 // 0.3.4.1

// CodeYAMLUnmarshalFailed identifies a document the decoder cannot read: a
// syntax error, input past a bound, or a value its Go target cannot hold.
// Every refusal below carries it in its trail, so errs.HasCode answers for
// all of them.
const CodeYAMLUnmarshalFailed errs.Code = 0x00_03_04_02 // 0.3.4.2

// CodeYAMLAnchorRefused identifies an anchor (&name): outside the subset.
const CodeYAMLAnchorRefused errs.Code = 0x00_03_04_03 // 0.3.4.3

// CodeYAMLAliasRefused identifies an alias (*name): outside the subset.
const CodeYAMLAliasRefused errs.Code = 0x00_03_04_04 // 0.3.4.4

// CodeYAMLTagRefused identifies an explicit tag (!local, !!str, !<uri>):
// outside the subset.
const CodeYAMLTagRefused errs.Code = 0x00_03_04_05 // 0.3.4.5

// CodeYAMLMergeKeyRefused identifies a merge key (<<): outside the subset.
const CodeYAMLMergeKeyRefused errs.Code = 0x00_03_04_06 // 0.3.4.6

// CodeYAMLMultiDocRefused identifies a second document where one was
// expected.
const CodeYAMLMultiDocRefused errs.Code = 0x00_03_04_07 // 0.3.4.7

// CodeYAMLComplexKeyRefused identifies a complex mapping key (the ? indicator,
// or a collection used as a key): outside the subset.
const CodeYAMLComplexKeyRefused errs.Code = 0x00_03_04_08 // 0.3.4.8

// CodeYAMLDirectiveRefused identifies a directive (%YAML, %TAG): outside the
// subset.
const CodeYAMLDirectiveRefused errs.Code = 0x00_03_04_09 // 0.3.4.9

// CodeYAMLDuplicateKey identifies a mapping that holds the same key twice.
const CodeYAMLDuplicateKey errs.Code = 0x00_03_04_0A // 0.3.4.10

// CodeYAMLLeadingZeroRefused identifies an integer written with a leading zero
// read into a target whose value depends on it: YAML 1.1 reads 0644 as octal
// 420, YAML 1.2 as decimal 644.
const CodeYAMLLeadingZeroRefused errs.Code = 0x00_03_04_0B // 0.3.4.11
