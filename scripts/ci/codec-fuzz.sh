#!/usr/bin/env bash
# scripts/ci/codec-fuzz.sh — the fuzz lane of the codec and transform families
# (#897), on consensus-fuzz.sh's model.
#
# Runs every fuzz target of the format codecs, the facade's compressed frame,
# the transform engines and the vendor parsers for a bounded time each. A
# crash, a hang or a broken property fails the run; go writes the failing
# input under the package's testdata/fuzz/<Target>/, which a fix commits as a
# regression case. Local: scripts/ci/codec-fuzz.sh 30s. A new Fuzz function in
# these trees gets its line here; this grep lists them all:
#   grep -rn '^func Fuzz' --include='*_test.go' internal/service/data/codec \
#     internal/service/data/transform pkg/v1/data/codec third-party/codec third-party/transform
set -euo pipefail
per="${1:-${FUZZ_TIME:-1m}}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
targets=(
	"./internal/service/data/codec/asn1 FuzzUnmarshal"
	"./internal/service/data/codec/baseenc FuzzBase45Decode"
	"./internal/service/data/codec/baseenc FuzzBase45RoundTrip"
	"./internal/service/data/codec/baseenc FuzzBaseConvDecode"
	"./internal/service/data/codec/baseenc FuzzBaseConvRoundTrip"
	"./internal/service/data/codec/bson FuzzUnmarshal"
	"./internal/service/data/codec/cbor FuzzNewDeterministic"
	"./internal/service/data/codec/cbor FuzzUnmarshal"
	"./internal/service/data/codec/csv FuzzAppendRecordsIsEncodingCSV"
	"./internal/service/data/codec/csv FuzzUnmarshalRoundTrip"
	"./internal/service/data/codec/flatbuffers FuzzUnmarshal"
	"./internal/service/data/codec/form FuzzRoundTrip"
	"./internal/service/data/codec/json FuzzUnmarshal"
	"./internal/service/data/codec/jsonpatch FuzzDiffApply"
	"./internal/service/data/codec/jsonschema FuzzNewValidator"
	"./internal/service/data/codec/jsonshape FuzzOfAgreesWithMarshal"
	"./internal/service/data/codec/msgpack FuzzUnmarshal"
	"./internal/service/data/codec/multipart FuzzUnmarshal"
	"./internal/service/data/codec/ndjson FuzzUnmarshalCountsRecords"
	"./internal/service/data/codec/pem FuzzAppendBlockIsEncodingPEM"
	"./internal/service/data/codec/pem FuzzStreamFindsWhatADecodeLoopFinds"
	"./internal/service/data/codec/strictjson FuzzDecodeAgreesWithJSONv2"
	"./internal/service/data/codec/tlv FuzzTLVDecodeStream"
	"./internal/service/data/codec/tlv FuzzTLVUnmarshalAny"
	"./internal/service/data/codec/tlv FuzzTLVUnmarshalTyped"
	"./internal/service/data/codec/toml FuzzUnmarshal"
	"./internal/service/data/codec/toml FuzzUnmarshalTyped"
	"./internal/service/data/codec/xml FuzzUnmarshal"
	"./internal/service/data/codec/yaml FuzzUnmarshal"
	"./internal/service/data/transform FuzzDecompressBounded"
	"./pkg/v1/data/codec FuzzUnmarshalCompressed"
	"./third-party/codec/hcl FuzzUnmarshalNeverPanics"
	"./third-party/codec/protobuf FuzzUnmarshalStructpb"
	"./third-party/codec/yaml FuzzNativeAgreesWithYAMLv3"
	"./third-party/codec/yaml FuzzNativeOutputReadsTheSame"
	"./third-party/transform FuzzDecompressNeverExceedsCeiling"
	"./third-party/transform FuzzRoundTripIsExact"
)
failed=0
for t in "${targets[@]}"; do
	pkg="${t% *}"
	name="${t#* }"
	echo "::group::$name ($pkg, $per)"
	if ! go test -run '^$' -fuzz "^${name}\$" -fuzztime "$per" "$pkg"; then
		echo "::error title=fuzz crasher::$name in $pkg; the input is under $pkg/testdata/fuzz/$name/"
		failed=1
	fi
	echo "::endgroup::"
done
exit "$failed"
