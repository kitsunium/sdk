// Package yaml wraps gopkg.in/yaml.v3 as a codec.Codec implementation
// registered under the Format "yaml-full". It is the whole of YAML 1.2 as
// yaml.v3 reads it — anchors and aliases, tags, merge keys, complex keys,
// multi-document streams — for the program that needs a construct the SDK's
// own codec refuses by name.
//
// It is a module of its own under third-party/ (ADR 0157), NOT
// internal/service/data/codec, because the SDK's own "yaml" Format is a native,
// standard-library-only reader of a named subset (internal/service/data/codec/yaml)
// and the public module stays free of gopkg.in/yaml.v3. It is opt-in: a consumer blank-imports this package
// to register "yaml-full"; pkg/v1/data/codec does NOT pull it.
//
// It claims NO MIME type and NO file extension. ".yaml", ".yml" and
// application/yaml stay the native codec's, whatever else a program imports, so
// importing this package never changes what an extension lookup returns; a
// caller reaches the full reader by naming "yaml-full".
//
// Streaming is supported: yaml.v3 exposes Encoder/Decoder types that serialise
// a sequence of documents separated by `---` markers.
//
// Package yaml — range 0.3.77.* (third-party/codec/yaml block).
//
// Package yaml — adapts yaml.v3's *Decoder to codec.Decoder.
//
// Package yaml — adapts yaml.v3's *Encoder to codec.Encoder.
//
// Package yaml — declares the sentinel *errs.Error values for the full YAML codec.
package yaml
