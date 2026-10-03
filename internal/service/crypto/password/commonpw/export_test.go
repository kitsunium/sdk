// Package commonpw — hands the external suite the list as embedded, so it can
// check the bytes against the digest the package documents.
package commonpw

// ListForTest returns the embedded list, as SecLists publishes it.
func ListForTest() string {
	return listText
}

// CompareFoldedForTest is compareFolded, for the suite to hold against
// strings.Compare over strings.ToLower.
func CompareFoldedForTest(entry string, password []byte) int {
	return compareFolded(entry, password)
}
