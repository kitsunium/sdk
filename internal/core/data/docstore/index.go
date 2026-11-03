package docstore

// IndexSpec declares one secondary index: a name to read it by, whether it is
// unique, whether it keeps its keys in order, and the function that gives a
// document its keys in it. Build one with [Unique], [Index] or [Ordered], and
// give it to an engine's constructor — Open or OpenSQL in
// internal/service/data/docstore, which refuse the same declarations alike.
// The file engine rebuilds its indexes when the store opens; both keep them
// in step with every write. A field added here is added at the end, so a
// declaration written field by field keeps compiling.
type IndexSpec[T any] struct {
	// Keys returns a document's keys in the index. Empty keys are not
	// indexed, and a key listed twice is filed once.
	Keys func(T) []string
	// Name reads the index in Lookup and Find, and names it in a refusal.
	Name string
	// Unique refuses a write that would file two documents under one key.
	Unique bool
	// Ordered keeps the index's keys in order, for [Ranged].Range. The SQL
	// engine files an ordered index's rows as any other's, so declaring
	// one changes no table; the file engine keeps a sorted list beside the
	// index it rebuilds at Open.
	Ordered bool
}

// Unique declares a unique index over the one key key returns: no two
// documents may share it, and a write that would is refused with
// [UniqueKeyTaken]. An empty key is not indexed, so any number of documents
// may have none.
func Unique[T any](name string, key func(T) string) IndexSpec[T] {
	//: a nil function stays nil, for the engine's constructor to refuse by
	//: name.
	if key == nil {
		//: the declaration, without a way to compute its keys.
		return IndexSpec[T]{Name: name, Unique: true}
	}
	//: one key, or none when it is empty.
	return IndexSpec[T]{Name: name, Unique: true, Keys: func(v T) []string {
		//: the empty key is not a key.
		if k := key(v); k != "" {
			//: the one key.
			return []string{k}
		}
		//: nothing to file.
		return nil
	}}
}

// Index declares an index where a document may have several keys and a key
// several documents — the members a task is shared with. Find reads it.
func Index[T any](name string, keys func(T) []string) IndexSpec[T] {
	//: the declaration as given; the engine's constructor checks it.
	return IndexSpec[T]{Name: name, Keys: keys}
}

// Ordered declares an index over the one key key returns that keeps its
// keys in order, for [Ranged].Range: a key may file several documents, and
// an empty key is not indexed. Keys compare as bytes, as Go compares
// strings, so a number is padded to a fixed width and an instant written
// in RFC 3339 at one offset — UTC — to sort as it reads. Find and FindAfter
// read it as any index.
func Ordered[T any](name string, key func(T) string) IndexSpec[T] {
	//: a nil function stays nil, for the engine's constructor to refuse by
	//: name.
	if key == nil {
		//: the declaration, without a way to compute its keys.
		return IndexSpec[T]{Name: name, Ordered: true}
	}
	//: one key, or none when it is empty.
	return IndexSpec[T]{Name: name, Ordered: true, Keys: func(v T) []string {
		//: the empty key is not a key.
		if k := key(v); k != "" {
			//: the one key.
			return []string{k}
		}
		//: nothing to file.
		return nil
	}}
}
