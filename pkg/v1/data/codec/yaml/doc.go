// Package yaml registers the YAML codec with the SDK's codec registry — and
// no other codec — when it is imported (ADR 0134):
//
//	import _ "github.com/kitsunium/sdk/pkg/v1/data/codec/yaml"
//
// Everything that dispatches through the registry by format name then reads
// and writes YAML: config.FileSource and config.FSSource, i18n.LoadFS, and
// the codec package's Marshal and Unmarshal. Importing
// github.com/kitsunium/sdk/pkg/v1/data/codec instead registers every format the SDK
// ships — BSON, CBOR, MessagePack and the rest — which
// a program that only reads YAML does not need to link. This package links
// the YAML codec and nothing else: the codec is the SDK's own, written on the
// standard library alone.
//
// # A named subset of YAML 1.2.2
//
// The codec reads the YAML a configuration file is written in: block
// mappings and sequences, flow collections, plain, single- and double-quoted
// scalars, literal (|) and folded (>) blocks, comments, and one document with
// an optional "---" and "...". Plain scalars resolve by the YAML 1.2 core
// schema: true and false are the booleans — yes, no, on and off are text —
// and 0o and 0x prefix octal and hexadecimal integers.
//
// It refuses BY NAME, each with its own error code and the line and column
// it starts at, what a configuration does not need and what two YAML readers
// read differently: anchors and aliases, tags, merge keys, complex keys,
// directives, a second document, a mapping holding a key twice, and an
// integer written with a leading zero (0644 is octal to YAML 1.1 and decimal
// to YAML 1.2) wherever its value matters. A refusal never quotes the
// document. A document is bounded at 10 MiB, a million nodes and a hundred
// levels of nesting.
//
// Writing is deterministic block style, and a string is written plain only
// when every YAML reader reads it back as the same string.
//
// The whole of YAML stays available, opt-in, through the module
// github.com/kitsunium/sdk/third-party/codec/yaml, which registers
// gopkg.in/yaml.v3 as the Format "yaml-full"; it claims no extension, so
// ".yaml" keeps meaning this codec.
//
// Importing both this package and pkg/v1/data/codec is harmless: a format is
// registered by the package that implements it, which Go initialises once
// however many packages import it.
//
// # Errors
//
// Failures carry the range 0.3.4.*; match them with errs.HasCode, or the
// sentinels with errors.Is. Every refusal by name also carries
// CodeUnmarshalFailed in its trail, so testing for that code answers for all
// of them:
//
//	CodeMarshalFailed          0.3.4.1   a value the encoder cannot write in the subset
//	CodeUnmarshalFailed        0.3.4.2   not YAML of the subset, past a bound, or a value its target cannot hold
//	CodeAnchorRefused          0.3.4.3   an anchor, &name
//	CodeAliasRefused           0.3.4.4   an alias, *name
//	CodeTagRefused             0.3.4.5   a tag, !x, !!str, !<uri>
//	CodeMergeKeyRefused        0.3.4.6   a merge key, <<
//	CodeMultiDocRefused        0.3.4.7   a second document where one was expected
//	CodeComplexKeyRefused      0.3.4.8   a complex key, ? k, or a collection as a key
//	CodeDirectiveRefused       0.3.4.9   a directive, %YAML, %TAG
//	CodeDuplicateKey           0.3.4.10  a mapping holding one key twice
//	CodeLeadingZeroRefused     0.3.4.11  an integer written with a leading zero where its value matters
package yaml
