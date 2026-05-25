<!--
  Per-package "Use cases" — INTERACTIVE tabs (HTML). The narrative
  "Goals" + "What's shipped" tables live in the Go doc comment (so
  pkg.go.dev + README.md show them too); only the HTML tabs need a
  hand-authored file because raw HTML inside a Go doc comment renders
  as literal text on pkg.go.dev.

  Every tab shows the SAME Marshal/Unmarshal pair — only the Format
  constant changes. That's the codec's core promise: format-swap at
  the call site is a single string change. Code blocks inside each
  tabpanel use markdown fences with blank lines around them so
  astro-expressive-code processes them at build time (Shiki
  github-dark + copy-icon button — same as Quick start).
-->

## Use cases

Same `Marshal` + `Unmarshal` pair across every Format — only the constant changes. Declare the type once:

```go
type User struct {
    Name string `json:"name" cbor:"name" yaml:"name"`
    Age  int    `json:"age"  cbor:"age"  yaml:"age"`
}

u := User{Name: "Ada", Age: 36}
```

Then pick the wire format — the call site stays identical:

<div class="tabs" data-tabs>
<div class="tab-strip" role="tablist">
<button type="button" role="tab" id="uc-json-btn" aria-controls="uc-json" aria-selected="true" tabindex="0" class="active">json</button>
<button type="button" role="tab" id="uc-ndjson-btn" aria-controls="uc-ndjson" aria-selected="false" tabindex="-1">ndjson</button>
<button type="button" role="tab" id="uc-xml-btn" aria-controls="uc-xml" aria-selected="false" tabindex="-1">xml</button>
<button type="button" role="tab" id="uc-csv-btn" aria-controls="uc-csv" aria-selected="false" tabindex="-1">csv</button>
<button type="button" role="tab" id="uc-asn1der-btn" aria-controls="uc-asn1der" aria-selected="false" tabindex="-1">asn1-der</button>
<button type="button" role="tab" id="uc-pem-btn" aria-controls="uc-pem" aria-selected="false" tabindex="-1">pem</button>
<button type="button" role="tab" id="uc-yaml-btn" aria-controls="uc-yaml" aria-selected="false" tabindex="-1">yaml</button>
<button type="button" role="tab" id="uc-toml-btn" aria-controls="uc-toml" aria-selected="false" tabindex="-1">toml</button>
<button type="button" role="tab" id="uc-cbor-btn" aria-controls="uc-cbor" aria-selected="false" tabindex="-1">cbor</button>
<button type="button" role="tab" id="uc-msgpack-btn" aria-controls="uc-msgpack" aria-selected="false" tabindex="-1">msgpack</button>
<button type="button" role="tab" id="uc-tlv-btn" aria-controls="uc-tlv" aria-selected="false" tabindex="-1">tlv</button>
<button type="button" role="tab" id="uc-flatbuffers-btn" aria-controls="uc-flatbuffers" aria-selected="false" tabindex="-1">flatbuffers</button>
<button type="button" role="tab" id="uc-base64-btn" aria-controls="uc-base64" aria-selected="false" tabindex="-1">base64</button>
<button type="button" role="tab" id="uc-base64url-btn" aria-controls="uc-base64url" aria-selected="false" tabindex="-1">base64url</button>
<button type="button" role="tab" id="uc-base32-btn" aria-controls="uc-base32" aria-selected="false" tabindex="-1">base32</button>
<button type="button" role="tab" id="uc-base16-btn" aria-controls="uc-base16" aria-selected="false" tabindex="-1">base16</button>
<button type="button" role="tab" id="uc-hex-btn" aria-controls="uc-hex" aria-selected="false" tabindex="-1">hex</button>
<button type="button" role="tab" id="uc-ascii85-btn" aria-controls="uc-ascii85" aria-selected="false" tabindex="-1">ascii85</button>
</div>

<div role="tabpanel" id="uc-json" aria-labelledby="uc-json-btn">

```go
data, err := codec.Marshal(codec.JSON, u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal(codec.JSON, data, &back)
```

</div>

<div role="tabpanel" id="uc-ndjson" aria-labelledby="uc-ndjson-btn" hidden>

```go
data, err := codec.Marshal(codec.NDJSON, u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal(codec.NDJSON, data, &back)
```

</div>

<div role="tabpanel" id="uc-xml" aria-labelledby="uc-xml-btn" hidden>

```go
data, err := codec.Marshal(codec.XML, u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal(codec.XML, data, &back)
```

</div>

<div role="tabpanel" id="uc-csv" aria-labelledby="uc-csv-btn" hidden>

```go
data, err := codec.Marshal(codec.CSV, u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal(codec.CSV, data, &back)
```

</div>

<div role="tabpanel" id="uc-asn1der" aria-labelledby="uc-asn1der-btn" hidden>

```go
data, err := codec.Marshal(codec.ASN1DER, u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal(codec.ASN1DER, data, &back)
```

</div>

<div role="tabpanel" id="uc-pem" aria-labelledby="uc-pem-btn" hidden>

```go
data, err := codec.Marshal(codec.PEM, u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal(codec.PEM, data, &back)
```

</div>

<div role="tabpanel" id="uc-yaml" aria-labelledby="uc-yaml-btn" hidden>

```go
data, err := codec.Marshal(codec.YAML, u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal(codec.YAML, data, &back)
```

</div>

<div role="tabpanel" id="uc-toml" aria-labelledby="uc-toml-btn" hidden>

```go
data, err := codec.Marshal(codec.TOML, u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal(codec.TOML, data, &back)
```

</div>

<div role="tabpanel" id="uc-cbor" aria-labelledby="uc-cbor-btn" hidden>

```go
data, err := codec.Marshal(codec.CBOR, u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal(codec.CBOR, data, &back)
```

</div>

<div role="tabpanel" id="uc-msgpack" aria-labelledby="uc-msgpack-btn" hidden>

```go
data, err := codec.Marshal(codec.MsgPack, u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal(codec.MsgPack, data, &back)
```

</div>

<div role="tabpanel" id="uc-tlv" aria-labelledby="uc-tlv-btn" hidden>

```go
data, err := codec.Marshal("tlv", u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal("tlv", data, &back)
```

</div>

<div role="tabpanel" id="uc-flatbuffers" aria-labelledby="uc-flatbuffers-btn" hidden>

```go
data, err := codec.Marshal("flatbuffers", u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal("flatbuffers", data, &back)
```

</div>

<div role="tabpanel" id="uc-base64" aria-labelledby="uc-base64-btn" hidden>

```go
data, err := codec.Marshal("base64", u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal("base64", data, &back)
```

</div>

<div role="tabpanel" id="uc-base64url" aria-labelledby="uc-base64url-btn" hidden>

```go
data, err := codec.Marshal("base64url", u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal("base64url", data, &back)
```

</div>

<div role="tabpanel" id="uc-base32" aria-labelledby="uc-base32-btn" hidden>

```go
data, err := codec.Marshal("base32", u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal("base32", data, &back)
```

</div>

<div role="tabpanel" id="uc-base16" aria-labelledby="uc-base16-btn" hidden>

```go
data, err := codec.Marshal("base16", u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal("base16", data, &back)
```

</div>

<div role="tabpanel" id="uc-hex" aria-labelledby="uc-hex-btn" hidden>

```go
data, err := codec.Marshal("hex", u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal("hex", data, &back)
```

</div>

<div role="tabpanel" id="uc-ascii85" aria-labelledby="uc-ascii85-btn" hidden>

```go
data, err := codec.Marshal("ascii85", u)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var back User
err = codec.Unmarshal("ascii85", data, &back)
```

</div>

</div>

Need to broadcast the same value to N formats in one call (HTTP content negotiation, multi-protocol buses)? Use `codec.MarshalMany(u, codec.JSON, codec.CBOR, "base64", …)` — see the API reference below.

> Need a full perf comparison across all 18 codecs? See the <a href="#benchmarks">Benchmarks</a> section at the bottom of this page.
