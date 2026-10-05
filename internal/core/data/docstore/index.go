package docstore

// IndexSpec declares one secondary index: a name to read it by, whether it is
// unique, and the function that gives a document its keys in it. Build one
// with [Unique] or [Index], and give it to an engine's constructor — Open or
// OpenSQL in internal/service/data/docstore, which refuse the same
// declarations alike. The file engine rebuilds its indexes when the store
// opens; both keep them in step with every write.
type IndexSpec[T any] struct {
	// Keys returns a document's keys in the index. Empty keys are not
	// indexed, and a key listed twice is filed once.
	Keys func(T) []string
	// Name reads the index in Lookup and Find, and names it in a refusal.
	Name string
	// Unique refuses a write that would file two documents under one key.
	Unique bool
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
