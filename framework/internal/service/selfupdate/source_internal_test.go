// Internal test fixtures shared by the suite: the release source every case
// constructs a Service with.
package selfupdate

// testSource is the SourceValue the suite injects wherever the source
// implementation read package constants.
//
// Its values are deliberately NOT the ones the original hard-coded: a suite that
// reproduced "kodflow"/"ktn"/testSource.Product would pass just as well against a
// package that still ignored SourceValue and used its constants. These differ on
// every field, so an assertion on a built URL or an env name fails the moment
// the parameterisation stops being honoured.
var testSource = SourceValue{
	Owner:      "acme",
	StableRepo: "widget-dist",
	DevRepo:    "widget",
	Product:    "widget",
}
