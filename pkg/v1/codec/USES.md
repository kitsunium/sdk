<!--
  Per-package "Use cases" section. Injected by docs/site/scripts/sync-versions.mjs
  AFTER the narrative pulled from the Go doc comment and BEFORE the
  auto-generated gomarkdoc symbol dump (Constants / Variables / Functions
  / Types) which is wrapped in a collapsible <details>.

  Authoring rule (replicable across packages — see pkg/v1/errs/USES.md,
  pkg/v1/logger/USES.md): keep each tab to "Best for:" one-liner + a
  5-10 line Go snippet. The tab strip uses the [data-tabs] markup that
  the Default.astro `initTabs` script wires up — same accessibility
  shape as the Tabs.astro component.
-->

## Use cases

Pick the Format that fits the wire constraint — the call site stays identical.

<div class="tabs" data-tabs>
<div class="tab-strip" role="tablist">
<button type="button" role="tab" id="uc-json-btn" aria-controls="uc-json" aria-selected="true" tabindex="0" class="active">JSON</button>
<button type="button" role="tab" id="uc-cbor-btn" aria-controls="uc-cbor" aria-selected="false" tabindex="-1">CBOR</button>
<button type="button" role="tab" id="uc-msgpack-btn" aria-controls="uc-msgpack" aria-selected="false" tabindex="-1">MsgPack</button>
<button type="button" role="tab" id="uc-yaml-btn" aria-controls="uc-yaml" aria-selected="false" tabindex="-1">YAML</button>
<button type="button" role="tab" id="uc-ndjson-btn" aria-controls="uc-ndjson" aria-selected="false" tabindex="-1">NDJSON</button>
<button type="button" role="tab" id="uc-flatbuffers-btn" aria-controls="uc-flatbuffers" aria-selected="false" tabindex="-1">FlatBuffers</button>
<button type="button" role="tab" id="uc-base64-btn" aria-controls="uc-base64" aria-selected="false" tabindex="-1">base64</button>
</div>

<div role="tabpanel" id="uc-json" aria-labelledby="uc-json-btn">
<p><strong>Best for:</strong> API responses, public configs, anywhere "readable" matters. Most-supported format on the planet.</p>
<pre><code class="language-go">data, err := codec.Marshal(codec.JSON, payload)
if err != nil { /* errs.HasCode(err, codec.CodeUnknownFormat) etc. */ }

var out MyType
err = codec.Unmarshal(codec.JSON, data, &amp;out)</code></pre>
</div>

<div role="tabpanel" id="uc-cbor" aria-labelledby="uc-cbor-btn" hidden>
<p><strong>Best for:</strong> IoT / mobile / embedded — compact binary, fast, RFC 8949 standard.</p>
<pre><code class="language-go">data, err := codec.Marshal(codec.CBOR, payload)
// data is ~30-50% smaller than equivalent JSON, decodes faster too.

var out MyType
err = codec.Unmarshal(codec.CBOR, data, &amp;out)</code></pre>
</div>

<div role="tabpanel" id="uc-msgpack" aria-labelledby="uc-msgpack-btn" hidden>
<p><strong>Best for:</strong> RPC payloads. Compact, ecosystem-wide adoption (Python, Ruby, Java …), faster than JSON.</p>
<pre><code class="language-go">data, err := codec.Marshal(codec.MsgPack, payload)
var out MyType
err = codec.Unmarshal(codec.MsgPack, data, &amp;out)</code></pre>
</div>

<div role="tabpanel" id="uc-yaml" aria-labelledby="uc-yaml-btn" hidden>
<p><strong>Best for:</strong> human-edited configs (Kubernetes manifests, CI files). <em>Slower than JSON / CBOR</em> — don't use it on the hot path.</p>
<pre><code class="language-go">data, err := codec.Marshal(codec.YAML, payload)
var cfg MyConfig
err = codec.Unmarshal(codec.YAML, data, &amp;cfg)</code></pre>
</div>

<div role="tabpanel" id="uc-ndjson" aria-labelledby="uc-ndjson-btn" hidden>
<p><strong>Best for:</strong> streaming logs, line-oriented batches. Each record is a self-contained JSON object on its own line — readable by <code>jq</code>, <code>grep</code>, log aggregators.</p>
<pre><code class="language-go">// Streaming-friendly — use NewEncoder for a long-lived writer.
enc, err := codec.NewEncoder(codec.NDJSON, os.Stdout)
for _, rec := range records { _ = enc.Encode(rec) }</code></pre>
</div>

<div role="tabpanel" id="uc-flatbuffers" aria-labelledby="uc-flatbuffers-btn" hidden>
<p><strong>Best for:</strong> zero-copy reads (game state, ML tensors). Schema lives outside the codec — you generate Go types from <code>.fbs</code> and pass raw bytes through.</p>
<pre><code class="language-go">// Passthrough — the FlatBuffers codec does no encoding work
// because the schema-generated code already produces a flat byte
// buffer. codec.Marshal / Unmarshal route the bytes verbatim.
data, _ := codec.Marshal("flatbuffers", builder.FinishedBytes())</code></pre>
</div>

<div role="tabpanel" id="uc-base64" aria-labelledby="uc-base64-btn" hidden>
<p><strong>Best for:</strong> text-safe wrap of any structure. The pipeline is JSON-encode → base-N. For raw bytes already in hand, reach for stdlib <code>encoding/base64</code> directly.</p>
<pre><code class="language-go">data, err := codec.Marshal("base64", payload)
// Six base-N flavours work the same way: "base64", "base64url",
// "base32", "base16", "hex", "ascii85".</code></pre>
</div>

</div>

> Need a full perf comparison across all 18 codecs? See the <a href="#benchmarks">Benchmarks</a> section at the bottom of this page.
